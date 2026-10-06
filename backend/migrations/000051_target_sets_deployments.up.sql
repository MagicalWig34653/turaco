-- Endpoints (F9 slice G2): Target Sets and Deployment planning (docs/product/f9-software-lifecycle-design.md,
-- ADR-0027). A Target Set is a saved, bounded device query evaluated on demand; its definition is validated JSON
-- that is never executed as SQL. A Deployment is the Desired Software State of one Software Version with one
-- intent, rolled out in ordered Deployment Rings, each targeting one Target Set with its promotion gate
-- configuration. G2 covers planning only: draft -> pending_approval -> approved -> scheduled, draft -> scheduled
-- (plans that are not high impact), and cancelled. Execution states arrive with G3.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

CREATE SEQUENCE IF NOT EXISTS endpoints.target_set_number_seq;
CREATE SEQUENCE IF NOT EXISTS endpoints.deployment_number_seq;

-- Human references TS-000001 and DEP-000001; the width grows instead of truncating.
CREATE OR REPLACE FUNCTION endpoints.next_target_set_reference() RETURNS text
LANGUAGE sql
AS $$
    SELECT 'TS-' || CASE WHEN n < 1000000 THEN lpad(n::text, 6, '0') ELSE n::text END
    FROM (SELECT nextval('endpoints.target_set_number_seq') AS n) AS s
$$;

CREATE OR REPLACE FUNCTION endpoints.next_deployment_reference() RETURNS text
LANGUAGE sql
AS $$
    SELECT 'DEP-' || CASE WHEN n < 1000000 THEN lpad(n::text, 6, '0') ELSE n::text END
    FROM (SELECT nextval('endpoints.deployment_number_seq') AS n) AS s
$$;

-- definition is the canonical JSON the application validated (filters, explicit include/exclude Device ids).
-- all_devices and high_impact_reason are derived from it when it is saved: all_devices means the definition selects
-- every live Device (no effective filter, a filter list covering its whole enum counts as unset); high_impact_reason
-- is all_devices or nested_root_group (an includeNested group without a parent). They are a snapshot used while a
-- plan is a draft; validation, submission and scheduling recompute high impact from the evaluated targets.
CREATE TABLE IF NOT EXISTS endpoints.target_sets (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    reference text NOT NULL DEFAULT endpoints.next_target_set_reference() UNIQUE,
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 150),
    description text CHECK (description IS NULL OR length(description) BETWEEN 1 AND 1000),
    owner_user_id uuid NOT NULL,
    definition jsonb NOT NULL CHECK (jsonb_typeof(definition) = 'object' AND octet_length(definition::text) <= 65536),
    all_devices boolean NOT NULL,
    high_impact_reason text CHECK (high_impact_reason IS NULL OR high_impact_reason IN ('all_devices', 'nested_root_group')),
    archived_at timestamptz,
    archived_by uuid,
    created_by uuid NOT NULL,
    -- The last person who changed the definition (created_by at first); excluded from approving plans using it.
    updated_by uuid NOT NULL,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT target_sets_all_devices_reason CHECK (NOT all_devices OR high_impact_reason = 'all_devices'),
    CONSTRAINT target_sets_archived_matches CHECK ((archived_at IS NULL) = (archived_by IS NULL))
);
-- Names are unique among the Target Sets in use; an archived set frees its name.
CREATE UNIQUE INDEX IF NOT EXISTS target_sets_name_unique ON endpoints.target_sets (lower(name)) WHERE archived_at IS NULL;

-- A Deployment: one Software Version, one intent (install | update | uninstall), optionally superseding earlier
-- versions. high_impact needs deployments.high_impact plus an approved plan Approval (subject deployment) before
-- the plan can be scheduled. While a plan is a draft it is a snapshot (uninstall, supersede, a ring on a high-impact
-- Target Set); validation, submission and scheduling recompute it including the evaluated target counts, and it is
-- frozen once the plan left draft except on the transitions that recompute it.
-- plan_sha256 binds the submitted plan (version, intent, rings, gates, Target Set versions) to its Approval; the
-- approval binding (approval_id, plan_sha256, submitted_*) is cleared when a pending plan returns to draft.
CREATE TABLE IF NOT EXISTS endpoints.deployments (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    reference text NOT NULL DEFAULT endpoints.next_deployment_reference() UNIQUE,
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 150),
    software_version_id uuid NOT NULL REFERENCES endpoints.software_versions(id) ON DELETE RESTRICT,
    intent text NOT NULL CHECK (intent IN ('install', 'update', 'uninstall')),
    supersede boolean NOT NULL DEFAULT false,
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'pending_approval', 'approved', 'scheduled', 'cancelled')),
    status_reason text CHECK (status_reason IS NULL OR status_reason ~ '^[a-z][a-z_]{0,39}$'),
    owner_user_id uuid NOT NULL,
    created_by uuid NOT NULL,
    editors uuid[] NOT NULL DEFAULT '{}' CHECK (cardinality(editors) <= 50),
    high_impact boolean NOT NULL DEFAULT false,
    approval_id uuid,
    submitted_by uuid,
    submitted_at timestamptz,
    plan_sha256 text CHECK (plan_sha256 IS NULL OR plan_sha256 ~ '^[0-9a-f]{64}$'),
    approved_at timestamptz,
    scheduled_by uuid,
    scheduled_at timestamptz,
    cancelled_by uuid,
    cancelled_at timestamptz,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT deployments_submitted_matches CHECK ((submitted_by IS NULL) = (submitted_at IS NULL)),
    CONSTRAINT deployments_scheduled_matches CHECK ((scheduled_by IS NULL) = (scheduled_at IS NULL)),
    CONSTRAINT deployments_cancelled_matches CHECK ((cancelled_by IS NULL) = (cancelled_at IS NULL)),
    CONSTRAINT deployments_status_fields CHECK (CASE status
        WHEN 'draft' THEN approval_id IS NULL AND plan_sha256 IS NULL AND submitted_at IS NULL
            AND approved_at IS NULL AND scheduled_at IS NULL AND cancelled_at IS NULL
        WHEN 'pending_approval' THEN approval_id IS NOT NULL AND submitted_at IS NOT NULL AND plan_sha256 IS NOT NULL
            AND approved_at IS NULL AND scheduled_at IS NULL AND cancelled_at IS NULL AND status_reason IS NULL
        WHEN 'approved' THEN approval_id IS NOT NULL AND plan_sha256 IS NOT NULL AND approved_at IS NOT NULL
            AND scheduled_at IS NULL AND cancelled_at IS NULL AND status_reason IS NULL
        WHEN 'scheduled' THEN scheduled_at IS NOT NULL AND plan_sha256 IS NOT NULL AND cancelled_at IS NULL AND status_reason IS NULL
            AND (NOT high_impact OR approved_at IS NOT NULL)
        ELSE cancelled_at IS NOT NULL AND status_reason IS NOT NULL END)
);
CREATE INDEX IF NOT EXISTS deployments_status_idx ON endpoints.deployments (status, id);
CREATE INDEX IF NOT EXISTS deployments_version_idx ON endpoints.deployments (software_version_id, id);
CREATE INDEX IF NOT EXISTS deployments_owner_idx ON endpoints.deployments (owner_user_id, id);
CREATE INDEX IF NOT EXISTS deployments_creator_idx ON endpoints.deployments (created_by, id);

-- Ordered rings of a Deployment. Position 1 is the pilot ring; only it may run without a Change window.
-- The position constraint is deferred so a reorder can swap positions inside one transaction; the application sets
-- it IMMEDIATE at the end of every ring change so a violation surfaces before commit.
CREATE TABLE IF NOT EXISTS endpoints.deployment_rings (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    deployment_id uuid NOT NULL REFERENCES endpoints.deployments(id) ON DELETE RESTRICT,
    position integer NOT NULL CHECK (position BETWEEN 1 AND 10),
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 100),
    target_set_id uuid NOT NULL REFERENCES endpoints.target_sets(id) ON DELETE RESTRICT,
    approval_required boolean NOT NULL DEFAULT false,
    success_threshold_percent integer NOT NULL CHECK (success_threshold_percent BETWEEN 1 AND 100),
    min_fresh_evidence_percent integer CHECK (min_fresh_evidence_percent IS NULL OR min_fresh_evidence_percent BETWEEN 1 AND 100),
    soak_minutes integer NOT NULL DEFAULT 0 CHECK (soak_minutes BETWEEN 0 AND 43200),
    -- The ring is GATED_BY this Change (by id; the Changes module owns it): its maintenance window gates the ring.
    -- That the Change is about this software is not enforced.
    change_id uuid,
    no_window_required boolean NOT NULL DEFAULT false,
    max_targets integer NOT NULL DEFAULT 5000 CHECK (max_targets BETWEEN 1 AND 5000),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT deployment_rings_position_unique UNIQUE (deployment_id, position) DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT deployment_rings_no_window_pilot CHECK (NOT no_window_required OR position = 1),
    CONSTRAINT deployment_rings_window_choice CHECK (NOT (no_window_required AND change_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS deployment_rings_target_set_idx ON endpoints.deployment_rings (target_set_id, deployment_id);

-- Append-only lifecycle of a Deployment.
CREATE TABLE IF NOT EXISTS endpoints.deployment_transitions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    deployment_id uuid NOT NULL REFERENCES endpoints.deployments(id) ON DELETE RESTRICT,
    from_status text CHECK (from_status IS NULL OR from_status IN ('draft', 'pending_approval', 'approved', 'scheduled', 'cancelled')),
    to_status text NOT NULL CHECK (to_status IN ('draft', 'pending_approval', 'approved', 'scheduled', 'cancelled')),
    operation text NOT NULL CHECK (operation ~ '^[a-z][a-z_]{0,39}$'),
    reason text CHECK (reason IS NULL OR reason ~ '^[a-z][a-z_]{0,39}$'),
    plan_sha256 text CHECK (plan_sha256 IS NULL OR plan_sha256 ~ '^[0-9a-f]{64}$'),
    actor_user_id uuid,
    actor_system text,
    correlation_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((actor_user_id IS NULL) <> (actor_system IS NULL))
);
CREATE INDEX IF NOT EXISTS deployment_transitions_deployment_idx ON endpoints.deployment_transitions (deployment_id, id);

CREATE OR REPLACE FUNCTION endpoints.forbid_deployment_history_change() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'deployment history rows are append-only (%.%)', TG_TABLE_SCHEMA, TG_TABLE_NAME USING ERRCODE = 'restrict_violation';
END
$$;
DROP TRIGGER IF EXISTS deployment_transitions_append_only ON endpoints.deployment_transitions;
CREATE TRIGGER deployment_transitions_append_only BEFORE UPDATE OR DELETE ON endpoints.deployment_transitions
    FOR EACH ROW EXECUTE FUNCTION endpoints.forbid_deployment_history_change();
DROP TRIGGER IF EXISTS deployment_transitions_no_truncate ON endpoints.deployment_transitions;
CREATE TRIGGER deployment_transitions_no_truncate BEFORE TRUNCATE ON endpoints.deployment_transitions
    FOR EACH STATEMENT EXECUTE FUNCTION endpoints.forbid_deployment_history_change();
-- Deployments and Target Sets are never deleted (cancel or archive instead).
DROP TRIGGER IF EXISTS deployments_no_delete ON endpoints.deployments;
CREATE TRIGGER deployments_no_delete BEFORE DELETE ON endpoints.deployments
    FOR EACH ROW EXECUTE FUNCTION endpoints.forbid_deployment_history_change();
DROP TRIGGER IF EXISTS deployments_no_truncate ON endpoints.deployments;
CREATE TRIGGER deployments_no_truncate BEFORE TRUNCATE ON endpoints.deployments
    FOR EACH STATEMENT EXECUTE FUNCTION endpoints.forbid_deployment_history_change();
DROP TRIGGER IF EXISTS target_sets_no_delete ON endpoints.target_sets;
CREATE TRIGGER target_sets_no_delete BEFORE DELETE ON endpoints.target_sets
    FOR EACH ROW EXECUTE FUNCTION endpoints.forbid_deployment_history_change();
DROP TRIGGER IF EXISTS target_sets_no_truncate ON endpoints.target_sets;
CREATE TRIGGER target_sets_no_truncate BEFORE TRUNCATE ON endpoints.target_sets
    FOR EACH STATEMENT EXECUTE FUNCTION endpoints.forbid_deployment_history_change();

-- The planning lifecycle holds in the database too. Once a plan left draft its planned content (version, intent,
-- supersede) is frozen, and so are its approval binding, approval, scheduling and high-impact fields except on the
-- transition that sets them: pending_approval -> draft clears the binding, pending_approval -> approved sets
-- approved_at, approved -> scheduled sets scheduled_*, and high_impact is recomputed only on a status change that is
-- not a cancellation.
CREATE OR REPLACE FUNCTION endpoints.check_deployment_transition() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.status IS DISTINCT FROM OLD.status AND NOT (
        (OLD.status = 'draft' AND NEW.status IN ('pending_approval', 'scheduled', 'cancelled'))
        OR (OLD.status = 'pending_approval' AND NEW.status IN ('draft', 'approved', 'cancelled'))
        OR (OLD.status = 'approved' AND NEW.status IN ('scheduled', 'cancelled'))
        OR (OLD.status = 'scheduled' AND NEW.status = 'cancelled')) THEN
        RAISE EXCEPTION 'deployment % -> % is not allowed', OLD.status, NEW.status USING ERRCODE = 'check_violation';
    END IF;
    IF OLD.status <> 'draft' THEN
        IF NEW.software_version_id IS DISTINCT FROM OLD.software_version_id
            OR NEW.intent IS DISTINCT FROM OLD.intent OR NEW.supersede IS DISTINCT FROM OLD.supersede THEN
            RAISE EXCEPTION 'a deployment plan is changed only in draft' USING ERRCODE = 'check_violation';
        END IF;
        IF (NEW.approval_id IS DISTINCT FROM OLD.approval_id OR NEW.plan_sha256 IS DISTINCT FROM OLD.plan_sha256
            OR NEW.submitted_by IS DISTINCT FROM OLD.submitted_by OR NEW.submitted_at IS DISTINCT FROM OLD.submitted_at)
            AND NOT (OLD.status = 'pending_approval' AND NEW.status = 'draft') THEN
            RAISE EXCEPTION 'the approval binding of a deployment is frozen' USING ERRCODE = 'check_violation';
        END IF;
        IF NEW.approved_at IS DISTINCT FROM OLD.approved_at AND NOT (OLD.status = 'pending_approval' AND NEW.status = 'approved') THEN
            RAISE EXCEPTION 'approved_at is set only by the approval' USING ERRCODE = 'check_violation';
        END IF;
        IF (NEW.scheduled_by IS DISTINCT FROM OLD.scheduled_by OR NEW.scheduled_at IS DISTINCT FROM OLD.scheduled_at)
            AND NOT (OLD.status = 'approved' AND NEW.status = 'scheduled') THEN
            RAISE EXCEPTION 'scheduled_* are set only by scheduling' USING ERRCODE = 'check_violation';
        END IF;
        IF NEW.high_impact IS DISTINCT FROM OLD.high_impact AND (NEW.status = OLD.status OR NEW.status = 'cancelled') THEN
            RAISE EXCEPTION 'high_impact is recomputed only by a lifecycle step' USING ERRCODE = 'check_violation';
        END IF;
    END IF;
    RETURN NEW;
END
$$;
DROP TRIGGER IF EXISTS deployments_transition ON endpoints.deployments;
CREATE TRIGGER deployments_transition BEFORE UPDATE ON endpoints.deployments
    FOR EACH ROW EXECUTE FUNCTION endpoints.check_deployment_transition();

-- Rings change only while their Deployment is a draft. The status is read FOR SHARE so a concurrent lifecycle step
-- (which locks the deployment row) cannot move the plan out of draft between this check and the ring change.
CREATE OR REPLACE FUNCTION endpoints.check_deployment_ring_change() RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    st text;
BEGIN
    SELECT status INTO st FROM endpoints.deployments WHERE id = CASE WHEN TG_OP = 'DELETE' THEN OLD.deployment_id ELSE NEW.deployment_id END
        FOR SHARE;
    IF st IS DISTINCT FROM 'draft' THEN
        RAISE EXCEPTION 'deployment rings change only while the deployment is a draft' USING ERRCODE = 'check_violation';
    END IF;
    IF TG_OP = 'UPDATE' AND NEW.deployment_id IS DISTINCT FROM OLD.deployment_id THEN
        RAISE EXCEPTION 'a ring cannot move to another deployment' USING ERRCODE = 'check_violation';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$$;
DROP TRIGGER IF EXISTS deployment_rings_draft_only ON endpoints.deployment_rings;
CREATE TRIGGER deployment_rings_draft_only BEFORE INSERT OR UPDATE OR DELETE ON endpoints.deployment_rings
    FOR EACH ROW EXECUTE FUNCTION endpoints.check_deployment_ring_change();
DROP TRIGGER IF EXISTS deployment_rings_no_truncate ON endpoints.deployment_rings;
CREATE TRIGGER deployment_rings_no_truncate BEFORE TRUNCATE ON endpoints.deployment_rings
    FOR EACH STATEMENT EXECUTE FUNCTION endpoints.forbid_deployment_history_change();
