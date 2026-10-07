-- Endpoints (F9 slice G3): Deployment execution (docs/product/f9-software-lifecycle-design.md, ADR-0027).
-- Deployment gets the execution statuses scheduled -> resolving_targets -> ready -> running <-> paused ->
-- completed | completed_with_errors | failed, and cancelled from every non-final status. Ring execution state lives in
-- deployment_ring_runs (one row per ring, created when the targets are resolved) because the ring definition itself is
-- frozen outside draft by trigger. Deployment Targets are the snapshot of the Devices a ring rolls out to; Deployment
-- Attempts are the immutable record of every provider write (set/clear of a ring assignment). Every history table is
-- append-only. The CHECK swaps are added NOT VALID and validated afterwards.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

ALTER TABLE endpoints.deployments ADD COLUMN IF NOT EXISTS started_by uuid;
ALTER TABLE endpoints.deployments ADD COLUMN IF NOT EXISTS started_at timestamptz;
ALTER TABLE endpoints.deployments ADD COLUMN IF NOT EXISTS finished_at timestamptz;
-- last_ticked_at orders the engine work round-robin (oldest first); scheduled_targets is the evaluated target total
-- at scheduling (the resolution refuses a plan that grew by more than 25% or became high impact since).
ALTER TABLE endpoints.deployments ADD COLUMN IF NOT EXISTS last_ticked_at timestamptz;
ALTER TABLE endpoints.deployments ADD COLUMN IF NOT EXISTS scheduled_targets integer CHECK (scheduled_targets IS NULL OR scheduled_targets >= 0);
CREATE INDEX IF NOT EXISTS deployments_engine_idx ON endpoints.deployments (last_ticked_at NULLS FIRST, id)
    WHERE status IN ('resolving_targets', 'running', 'paused', 'cancelled');

-- Deployment rings are referenced together with their Deployment by the execution tables (composite foreign keys).
ALTER TABLE endpoints.deployment_rings DROP CONSTRAINT IF EXISTS deployment_rings_id_deployment_unique;
ALTER TABLE endpoints.deployment_rings ADD CONSTRAINT deployment_rings_id_deployment_unique UNIQUE (id, deployment_id);

-- A Management Observation records whether its observed_at came from the provider (true) or is the time of the
-- synchronization that stored it (false); only provider timestamps or a state change after the read-back count as evidence.
ALTER TABLE endpoints.management_observations ADD COLUMN IF NOT EXISTS observed_at_provider boolean NOT NULL DEFAULT false;

ALTER TABLE endpoints.deployments DROP CONSTRAINT IF EXISTS deployments_status_check;
ALTER TABLE endpoints.deployments ADD CONSTRAINT deployments_status_check CHECK (status IN ('draft', 'pending_approval', 'approved', 'scheduled',
    'resolving_targets', 'ready', 'running', 'paused', 'completed', 'completed_with_errors', 'failed', 'cancelled')) NOT VALID;
ALTER TABLE endpoints.deployments VALIDATE CONSTRAINT deployments_status_check;

ALTER TABLE endpoints.deployments DROP CONSTRAINT IF EXISTS deployments_started_matches;
ALTER TABLE endpoints.deployments ADD CONSTRAINT deployments_started_matches CHECK ((started_by IS NULL) = (started_at IS NULL)) NOT VALID;
ALTER TABLE endpoints.deployments VALIDATE CONSTRAINT deployments_started_matches;

ALTER TABLE endpoints.deployments DROP CONSTRAINT IF EXISTS deployments_status_fields;
ALTER TABLE endpoints.deployments ADD CONSTRAINT deployments_status_fields CHECK (CASE status
    WHEN 'draft' THEN approval_id IS NULL AND plan_sha256 IS NULL AND submitted_at IS NULL
        AND approved_at IS NULL AND scheduled_at IS NULL AND cancelled_at IS NULL AND started_at IS NULL
    WHEN 'pending_approval' THEN approval_id IS NOT NULL AND submitted_at IS NOT NULL AND plan_sha256 IS NOT NULL
        AND approved_at IS NULL AND scheduled_at IS NULL AND cancelled_at IS NULL AND status_reason IS NULL AND started_at IS NULL
    WHEN 'approved' THEN approval_id IS NOT NULL AND plan_sha256 IS NOT NULL AND approved_at IS NOT NULL
        AND scheduled_at IS NULL AND cancelled_at IS NULL AND status_reason IS NULL AND started_at IS NULL
    WHEN 'scheduled' THEN scheduled_at IS NOT NULL AND plan_sha256 IS NOT NULL AND cancelled_at IS NULL AND status_reason IS NULL
        AND started_at IS NULL AND (NOT high_impact OR approved_at IS NOT NULL)
    WHEN 'resolving_targets' THEN scheduled_at IS NOT NULL AND plan_sha256 IS NOT NULL AND started_at IS NOT NULL AND finished_at IS NULL
        AND cancelled_at IS NULL AND status_reason IS NULL AND (NOT high_impact OR approved_at IS NOT NULL)
    WHEN 'ready' THEN scheduled_at IS NOT NULL AND plan_sha256 IS NOT NULL AND started_at IS NOT NULL AND finished_at IS NULL
        AND cancelled_at IS NULL AND status_reason IS NULL AND (NOT high_impact OR approved_at IS NOT NULL)
    WHEN 'running' THEN scheduled_at IS NOT NULL AND plan_sha256 IS NOT NULL AND started_at IS NOT NULL AND finished_at IS NULL
        AND cancelled_at IS NULL AND status_reason IS NULL AND (NOT high_impact OR approved_at IS NOT NULL)
    WHEN 'paused' THEN scheduled_at IS NOT NULL AND plan_sha256 IS NOT NULL AND started_at IS NOT NULL AND finished_at IS NULL
        AND cancelled_at IS NULL AND status_reason IS NOT NULL AND (NOT high_impact OR approved_at IS NOT NULL)
    WHEN 'completed' THEN started_at IS NOT NULL AND finished_at IS NOT NULL AND cancelled_at IS NULL AND status_reason IS NULL
    WHEN 'completed_with_errors' THEN started_at IS NOT NULL AND finished_at IS NOT NULL AND cancelled_at IS NULL AND status_reason IS NULL
    WHEN 'failed' THEN started_at IS NOT NULL AND finished_at IS NOT NULL AND cancelled_at IS NULL AND status_reason IS NOT NULL
    WHEN 'cancelled' THEN cancelled_at IS NOT NULL AND status_reason IS NOT NULL AND finished_at IS NULL
    ELSE false END) NOT VALID;
ALTER TABLE endpoints.deployments VALIDATE CONSTRAINT deployments_status_fields;

ALTER TABLE endpoints.deployment_transitions DROP CONSTRAINT IF EXISTS deployment_transitions_from_status_check;
ALTER TABLE endpoints.deployment_transitions ADD CONSTRAINT deployment_transitions_from_status_check CHECK (from_status IS NULL OR from_status IN
    ('draft', 'pending_approval', 'approved', 'scheduled', 'resolving_targets', 'ready', 'running', 'paused', 'completed', 'completed_with_errors',
     'failed', 'cancelled')) NOT VALID;
ALTER TABLE endpoints.deployment_transitions VALIDATE CONSTRAINT deployment_transitions_from_status_check;
ALTER TABLE endpoints.deployment_transitions DROP CONSTRAINT IF EXISTS deployment_transitions_to_status_check;
ALTER TABLE endpoints.deployment_transitions ADD CONSTRAINT deployment_transitions_to_status_check CHECK (to_status IN
    ('draft', 'pending_approval', 'approved', 'scheduled', 'resolving_targets', 'ready', 'running', 'paused', 'completed', 'completed_with_errors',
     'failed', 'cancelled')) NOT VALID;
ALTER TABLE endpoints.deployment_transitions VALIDATE CONSTRAINT deployment_transitions_to_status_check;

-- Deployment lifecycle including execution. Planning content and the approval binding stay frozen as in G2;
-- started_* is set only by the start (scheduled -> resolving_targets), finished_at only by a final status.
CREATE OR REPLACE FUNCTION endpoints.check_deployment_transition() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.status IS DISTINCT FROM OLD.status AND NOT (
        (OLD.status = 'draft' AND NEW.status IN ('pending_approval', 'scheduled', 'cancelled'))
        OR (OLD.status = 'pending_approval' AND NEW.status IN ('draft', 'approved', 'cancelled'))
        OR (OLD.status = 'approved' AND NEW.status IN ('scheduled', 'cancelled'))
        OR (OLD.status = 'scheduled' AND NEW.status IN ('resolving_targets', 'cancelled'))
        OR (OLD.status = 'resolving_targets' AND NEW.status IN ('ready', 'failed', 'cancelled'))
        OR (OLD.status = 'ready' AND NEW.status IN ('running', 'failed', 'cancelled'))
        OR (OLD.status = 'running' AND NEW.status IN ('paused', 'completed', 'completed_with_errors', 'failed', 'cancelled'))
        OR (OLD.status = 'paused' AND NEW.status IN ('running', 'failed', 'cancelled'))) THEN
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
        IF (NEW.scheduled_by IS DISTINCT FROM OLD.scheduled_by OR NEW.scheduled_at IS DISTINCT FROM OLD.scheduled_at
            OR NEW.scheduled_targets IS DISTINCT FROM OLD.scheduled_targets)
            AND NOT (OLD.status = 'approved' AND NEW.status = 'scheduled') THEN
            RAISE EXCEPTION 'scheduled_* are set only by scheduling' USING ERRCODE = 'check_violation';
        END IF;
        IF (NEW.started_by IS DISTINCT FROM OLD.started_by OR NEW.started_at IS DISTINCT FROM OLD.started_at)
            AND NOT (OLD.status = 'scheduled' AND NEW.status = 'resolving_targets') THEN
            RAISE EXCEPTION 'started_* are set only by the start' USING ERRCODE = 'check_violation';
        END IF;
        IF NEW.finished_at IS DISTINCT FROM OLD.finished_at AND NEW.status NOT IN ('completed', 'completed_with_errors', 'failed') THEN
            RAISE EXCEPTION 'finished_at is set only by a final status' USING ERRCODE = 'check_violation';
        END IF;
        IF NEW.high_impact IS DISTINCT FROM OLD.high_impact AND (NEW.status = OLD.status OR NEW.status = 'cancelled'
            OR OLD.status IN ('scheduled', 'resolving_targets', 'ready', 'running', 'paused')) THEN
            RAISE EXCEPTION 'high_impact is recomputed only by a lifecycle step' USING ERRCODE = 'check_violation';
        END IF;
    END IF;
    RETURN NEW;
END
$$;

-- One row per ring of a started Deployment: execution state of the ring.
-- pending -> active -> awaiting_promotion -> promoted; active | awaiting_promotion -> halted; halted -> active (resume).
-- group_external_id is the Turaco-owned provider group of the ring; management_artifact_* pins the artifact written at the
-- first write (read-back, clearing and the gate use it; it never changes); clear_requested_at queues the clearing of the
-- ring assignment (cancel, kill switch); attempt_base/retry_count implement the explicit retry of a ring halted with
-- assignment_failed. promotion_approval_* is the optional Approval
-- (subject deployment_ring) that gates the promotion of this ring to the next one.
CREATE TABLE IF NOT EXISTS endpoints.deployment_ring_runs (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    deployment_id uuid NOT NULL REFERENCES endpoints.deployments(id) ON DELETE RESTRICT,
    ring_id uuid NOT NULL UNIQUE REFERENCES endpoints.deployment_rings(id) ON DELETE RESTRICT,
    position integer NOT NULL CHECK (position BETWEEN 1 AND 10),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'active', 'awaiting_promotion', 'promoted', 'halted')),
    status_reason text CHECK (status_reason IS NULL OR status_reason ~ '^[a-z][a-z_]{0,39}$'),
    group_external_id text NOT NULL CHECK (length(group_external_id) BETWEEN 1 AND 200),
    activated_at timestamptz,
    settled_at timestamptz,
    awaiting_since timestamptz,
    promoted_at timestamptz,
    promoted_by uuid,
    halted_at timestamptz,
    assignment_requested_at timestamptz,
    assignment_cleared_at timestamptz,
    clear_requested_at timestamptz,
    management_artifact_id uuid REFERENCES endpoints.management_artifacts(id) ON DELETE RESTRICT,
    management_artifact_external_id text CHECK (management_artifact_external_id IS NULL OR length(management_artifact_external_id) BETWEEN 1 AND 200),
    attempt_base integer NOT NULL DEFAULT 0 CHECK (attempt_base >= 0),
    retry_count integer NOT NULL DEFAULT 0 CHECK (retry_count BETWEEN 0 AND 3),
    promotion_approval_id uuid,
    promotion_approval_status text CHECK (promotion_approval_status IS NULL OR promotion_approval_status IN ('pending', 'approved', 'rejected')),
    promotion_approval_requested_by uuid,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT deployment_ring_runs_position_unique UNIQUE (deployment_id, position),
    CONSTRAINT deployment_ring_runs_id_deployment_unique UNIQUE (id, deployment_id),
    CONSTRAINT deployment_ring_runs_ring_fk FOREIGN KEY (ring_id, deployment_id) REFERENCES endpoints.deployment_rings (id, deployment_id) ON DELETE RESTRICT,
    CONSTRAINT deployment_ring_runs_artifact_pin CHECK ((management_artifact_id IS NULL) = (management_artifact_external_id IS NULL)),
    CONSTRAINT deployment_ring_runs_approval_matches CHECK ((promotion_approval_id IS NULL) = (promotion_approval_status IS NULL)),
    CONSTRAINT deployment_ring_runs_status_fields CHECK (CASE status
        WHEN 'pending' THEN activated_at IS NULL AND halted_at IS NULL AND promoted_at IS NULL AND status_reason IS NULL AND awaiting_since IS NULL
        WHEN 'active' THEN activated_at IS NOT NULL AND halted_at IS NULL AND promoted_at IS NULL AND awaiting_since IS NULL
        WHEN 'awaiting_promotion' THEN activated_at IS NOT NULL AND awaiting_since IS NOT NULL AND halted_at IS NULL AND promoted_at IS NULL
        WHEN 'promoted' THEN activated_at IS NOT NULL AND promoted_at IS NOT NULL AND halted_at IS NULL
        ELSE activated_at IS NOT NULL AND halted_at IS NOT NULL AND status_reason IS NOT NULL AND promoted_at IS NULL END)
);
CREATE INDEX IF NOT EXISTS deployment_ring_runs_deployment_idx ON endpoints.deployment_ring_runs (deployment_id, position);

CREATE OR REPLACE FUNCTION endpoints.check_ring_run_transition() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.status IS DISTINCT FROM OLD.status AND NOT (
        (OLD.status = 'pending' AND NEW.status = 'active')
        OR (OLD.status = 'active' AND NEW.status IN ('awaiting_promotion', 'halted'))
        OR (OLD.status = 'awaiting_promotion' AND NEW.status IN ('promoted', 'halted'))
        OR (OLD.status = 'halted' AND NEW.status = 'active')) THEN
        RAISE EXCEPTION 'deployment ring % -> % is not allowed', OLD.status, NEW.status USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.deployment_id IS DISTINCT FROM OLD.deployment_id OR NEW.ring_id IS DISTINCT FROM OLD.ring_id
        OR NEW.position IS DISTINCT FROM OLD.position OR NEW.group_external_id IS DISTINCT FROM OLD.group_external_id THEN
        RAISE EXCEPTION 'a ring run is bound to its ring' USING ERRCODE = 'check_violation';
    END IF;
    IF OLD.management_artifact_id IS NOT NULL AND (NEW.management_artifact_id IS DISTINCT FROM OLD.management_artifact_id
        OR NEW.management_artifact_external_id IS DISTINCT FROM OLD.management_artifact_external_id) THEN
        RAISE EXCEPTION 'the management artifact of a ring run is pinned' USING ERRCODE = 'check_violation';
    END IF;
    IF (OLD.clear_requested_at IS NOT NULL AND NEW.clear_requested_at IS DISTINCT FROM OLD.clear_requested_at)
        OR (OLD.assignment_cleared_at IS NOT NULL AND NEW.assignment_cleared_at IS DISTINCT FROM OLD.assignment_cleared_at) THEN
        RAISE EXCEPTION 'clearing of a ring assignment cannot be undone' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.attempt_base < OLD.attempt_base OR NEW.retry_count < OLD.retry_count OR NEW.retry_count > OLD.retry_count + 1 THEN
        RAISE EXCEPTION 'the attempt counter only moves forward' USING ERRCODE = 'check_violation';
    END IF;
    -- A cancelled Deployment is final: only the clearing of its ring assignments is recorded afterwards.
    IF EXISTS (SELECT 1 FROM endpoints.deployments d WHERE d.id = OLD.deployment_id AND d.status = 'cancelled')
        AND (to_jsonb(NEW) - 'assignment_cleared_at' - 'version' - 'updated_at') IS DISTINCT FROM (to_jsonb(OLD) - 'assignment_cleared_at' - 'version' - 'updated_at') THEN
        RAISE EXCEPTION 'a ring run of a cancelled deployment is final' USING ERRCODE = 'check_violation';
    END IF;
    -- Promotion approval: none -> pending -> approved | rejected, rejected -> pending (new request); only a halt withdraws it.
    IF NEW.promotion_approval_status IS DISTINCT FROM OLD.promotion_approval_status AND NOT (
        (OLD.promotion_approval_status IS NULL AND NEW.promotion_approval_status = 'pending')
        OR (OLD.promotion_approval_status = 'pending' AND NEW.promotion_approval_status IN ('approved', 'rejected'))
        OR (OLD.promotion_approval_status = 'rejected' AND NEW.promotion_approval_status = 'pending')
        OR (OLD.promotion_approval_status IS NOT NULL AND NEW.promotion_approval_status IS NULL AND NEW.status = 'halted')) THEN
        RAISE EXCEPTION 'promotion approval % -> % is not allowed', OLD.promotion_approval_status, NEW.promotion_approval_status USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;
DROP TRIGGER IF EXISTS deployment_ring_runs_transition ON endpoints.deployment_ring_runs;
CREATE TRIGGER deployment_ring_runs_transition BEFORE UPDATE ON endpoints.deployment_ring_runs
    FOR EACH ROW EXECUTE FUNCTION endpoints.check_ring_run_transition();
DROP TRIGGER IF EXISTS deployment_ring_runs_no_delete ON endpoints.deployment_ring_runs;
CREATE TRIGGER deployment_ring_runs_no_delete BEFORE DELETE ON endpoints.deployment_ring_runs
    FOR EACH ROW EXECUTE FUNCTION endpoints.forbid_deployment_history_change();
DROP TRIGGER IF EXISTS deployment_ring_runs_no_truncate ON endpoints.deployment_ring_runs;
CREATE TRIGGER deployment_ring_runs_no_truncate BEFORE TRUNCATE ON endpoints.deployment_ring_runs
    FOR EACH STATEMENT EXECUTE FUNCTION endpoints.forbid_deployment_history_change();

CREATE TABLE IF NOT EXISTS endpoints.deployment_ring_transitions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    deployment_id uuid NOT NULL REFERENCES endpoints.deployments(id) ON DELETE RESTRICT,
    ring_run_id uuid NOT NULL,
    from_status text CHECK (from_status IS NULL OR from_status IN ('pending', 'active', 'awaiting_promotion', 'promoted', 'halted')),
    to_status text NOT NULL CHECK (to_status IN ('pending', 'active', 'awaiting_promotion', 'promoted', 'halted')),
    operation text NOT NULL CHECK (operation ~ '^[a-z][a-z_]{0,39}$'),
    reason text CHECK (reason IS NULL OR reason ~ '^[a-z][a-z_]{0,39}$'),
    actor_user_id uuid,
    actor_system text,
    correlation_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((actor_user_id IS NULL) <> (actor_system IS NULL)),
    FOREIGN KEY (ring_run_id, deployment_id) REFERENCES endpoints.deployment_ring_runs (id, deployment_id) ON DELETE RESTRICT
);
CREATE INDEX IF NOT EXISTS deployment_ring_transitions_run_idx ON endpoints.deployment_ring_transitions (ring_run_id, id);

-- The Devices a ring rolls out to, snapshotted when the targets are resolved (a Device belongs to the first ring that
-- selects it). pending -> assignment_requested -> awaiting_observation -> successful | failed | expired | not_applicable
-- (assignment_requested -> expired, reason read_back_missing, when the assignment was never read back);
-- already_satisfied and not_applicable may be set at resolution; cancelled from any non-final state.
CREATE TABLE IF NOT EXISTS endpoints.deployment_targets (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    deployment_id uuid NOT NULL REFERENCES endpoints.deployments(id) ON DELETE RESTRICT,
    ring_run_id uuid NOT NULL,
    device_id uuid NOT NULL REFERENCES endpoints.devices(id) ON DELETE RESTRICT,
    state text NOT NULL CHECK (state IN ('pending', 'assignment_requested', 'awaiting_observation', 'successful', 'failed', 'expired',
        'already_satisfied', 'not_applicable', 'cancelled')),
    state_reason text CHECK (state_reason IS NULL OR state_reason ~ '^[a-z][a-z_]{0,39}$'),
    resolved_at timestamptz NOT NULL,
    assignment_requested_at timestamptz,
    read_back_at timestamptz,
    expires_at timestamptz,
    decided_at timestamptz,
    evidence_observed_at timestamptz,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT deployment_targets_device_unique UNIQUE (deployment_id, device_id),
    CONSTRAINT deployment_targets_id_deployment_unique UNIQUE (id, deployment_id),
    CONSTRAINT deployment_targets_run_fk FOREIGN KEY (ring_run_id, deployment_id) REFERENCES endpoints.deployment_ring_runs (id, deployment_id) ON DELETE RESTRICT,
    CONSTRAINT deployment_targets_state_fields CHECK (CASE state
        WHEN 'pending' THEN assignment_requested_at IS NULL AND read_back_at IS NULL AND decided_at IS NULL
        WHEN 'assignment_requested' THEN assignment_requested_at IS NOT NULL AND read_back_at IS NULL AND decided_at IS NULL
        WHEN 'awaiting_observation' THEN read_back_at IS NOT NULL AND expires_at IS NOT NULL AND decided_at IS NULL
        WHEN 'successful' THEN read_back_at IS NOT NULL AND decided_at IS NOT NULL AND evidence_observed_at IS NOT NULL
        WHEN 'failed' THEN read_back_at IS NOT NULL AND decided_at IS NOT NULL AND evidence_observed_at IS NOT NULL
        WHEN 'expired' THEN decided_at IS NOT NULL AND (read_back_at IS NOT NULL OR state_reason = 'read_back_missing')
        WHEN 'not_applicable' THEN decided_at IS NOT NULL AND state_reason IS NOT NULL
        ELSE decided_at IS NOT NULL END)
);
CREATE INDEX IF NOT EXISTS deployment_targets_ring_state_idx ON endpoints.deployment_targets (ring_run_id, state, id);
CREATE INDEX IF NOT EXISTS deployment_targets_device_idx ON endpoints.deployment_targets (device_id);

CREATE OR REPLACE FUNCTION endpoints.check_deployment_target_transition() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.state IS DISTINCT FROM OLD.state AND NOT (
        (OLD.state = 'pending' AND NEW.state IN ('assignment_requested', 'already_satisfied', 'not_applicable', 'cancelled'))
        OR (OLD.state = 'assignment_requested' AND NEW.state IN ('awaiting_observation', 'expired', 'cancelled'))
        OR (OLD.state = 'awaiting_observation' AND NEW.state IN ('successful', 'failed', 'expired', 'not_applicable', 'cancelled'))) THEN
        RAISE EXCEPTION 'deployment target % -> % is not allowed', OLD.state, NEW.state USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.deployment_id IS DISTINCT FROM OLD.deployment_id OR NEW.ring_run_id IS DISTINCT FROM OLD.ring_run_id
        OR NEW.device_id IS DISTINCT FROM OLD.device_id THEN
        RAISE EXCEPTION 'a deployment target is bound to its ring and device' USING ERRCODE = 'check_violation';
    END IF;
    IF EXISTS (SELECT 1 FROM endpoints.deployments d WHERE d.id = OLD.deployment_id AND d.status = 'cancelled') THEN
        RAISE EXCEPTION 'a deployment target of a cancelled deployment is final' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;
DROP TRIGGER IF EXISTS deployment_targets_transition ON endpoints.deployment_targets;
CREATE TRIGGER deployment_targets_transition BEFORE UPDATE ON endpoints.deployment_targets
    FOR EACH ROW EXECUTE FUNCTION endpoints.check_deployment_target_transition();
DROP TRIGGER IF EXISTS deployment_targets_no_delete ON endpoints.deployment_targets;
CREATE TRIGGER deployment_targets_no_delete BEFORE DELETE ON endpoints.deployment_targets
    FOR EACH ROW EXECUTE FUNCTION endpoints.forbid_deployment_history_change();
DROP TRIGGER IF EXISTS deployment_targets_no_truncate ON endpoints.deployment_targets;
CREATE TRIGGER deployment_targets_no_truncate BEFORE TRUNCATE ON endpoints.deployment_targets
    FOR EACH STATEMENT EXECUTE FUNCTION endpoints.forbid_deployment_history_change();

CREATE TABLE IF NOT EXISTS endpoints.deployment_target_transitions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    deployment_id uuid NOT NULL REFERENCES endpoints.deployments(id) ON DELETE RESTRICT,
    target_id uuid NOT NULL,
    from_state text CHECK (from_state IS NULL OR from_state IN ('pending', 'assignment_requested', 'awaiting_observation', 'successful', 'failed',
        'expired', 'already_satisfied', 'not_applicable', 'cancelled')),
    to_state text NOT NULL CHECK (to_state IN ('pending', 'assignment_requested', 'awaiting_observation', 'successful', 'failed',
        'expired', 'already_satisfied', 'not_applicable', 'cancelled')),
    reason text CHECK (reason IS NULL OR reason ~ '^[a-z][a-z_]{0,39}$'),
    evidence_observed_at timestamptz,
    correlation_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (target_id, deployment_id) REFERENCES endpoints.deployment_targets (id, deployment_id) ON DELETE RESTRICT
);
CREATE INDEX IF NOT EXISTS deployment_target_transitions_target_idx ON endpoints.deployment_target_transitions (target_id, id);

-- Provider write attempts, one row per call of the Management Assignment Writer. The row is inserted as in_flight in
-- the transaction that decides the write, BEFORE the call, and finished (accepted, transient_error, permanent_error) after
-- it; a crash in between leaves in_flight, which the next tick turns into interrupted. Any set attempt of a ring run
-- means the provider may hold its assignment (clearing is driven by that). Finishing is the only change allowed. operation_id is the idempotency key sent to the provider: deploymentId:ringId:attempt for set and
-- deploymentId:ringId:clear:attempt for clear.
CREATE TABLE IF NOT EXISTS endpoints.deployment_attempts (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    deployment_id uuid NOT NULL REFERENCES endpoints.deployments(id) ON DELETE RESTRICT,
    ring_run_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('set_assignment', 'clear_assignment')),
    attempt integer NOT NULL CHECK (attempt > 0),
    operation_id text NOT NULL UNIQUE CHECK (length(operation_id) BETWEEN 1 AND 200),
    requested_at timestamptz NOT NULL,
    outcome_code text NOT NULL CHECK (outcome_code IN ('in_flight', 'accepted', 'transient_error', 'permanent_error', 'interrupted')),
    finished_at timestamptz,
    correlation_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT deployment_attempts_unique UNIQUE (ring_run_id, kind, attempt),
    CONSTRAINT deployment_attempts_run_fk FOREIGN KEY (ring_run_id, deployment_id) REFERENCES endpoints.deployment_ring_runs (id, deployment_id) ON DELETE RESTRICT,
    CONSTRAINT deployment_attempts_finished CHECK ((outcome_code = 'in_flight') = (finished_at IS NULL))
);

DROP TRIGGER IF EXISTS deployment_ring_transitions_append_only ON endpoints.deployment_ring_transitions;
CREATE TRIGGER deployment_ring_transitions_append_only BEFORE UPDATE OR DELETE ON endpoints.deployment_ring_transitions
    FOR EACH ROW EXECUTE FUNCTION endpoints.forbid_deployment_history_change();
DROP TRIGGER IF EXISTS deployment_ring_transitions_no_truncate ON endpoints.deployment_ring_transitions;
CREATE TRIGGER deployment_ring_transitions_no_truncate BEFORE TRUNCATE ON endpoints.deployment_ring_transitions
    FOR EACH STATEMENT EXECUTE FUNCTION endpoints.forbid_deployment_history_change();
DROP TRIGGER IF EXISTS deployment_target_transitions_append_only ON endpoints.deployment_target_transitions;
CREATE TRIGGER deployment_target_transitions_append_only BEFORE UPDATE OR DELETE ON endpoints.deployment_target_transitions
    FOR EACH ROW EXECUTE FUNCTION endpoints.forbid_deployment_history_change();
DROP TRIGGER IF EXISTS deployment_target_transitions_no_truncate ON endpoints.deployment_target_transitions;
CREATE TRIGGER deployment_target_transitions_no_truncate BEFORE TRUNCATE ON endpoints.deployment_target_transitions
    FOR EACH STATEMENT EXECUTE FUNCTION endpoints.forbid_deployment_history_change();
CREATE OR REPLACE FUNCTION endpoints.check_deployment_attempt_change() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'deployment attempts are append-only' USING ERRCODE = 'check_violation';
    END IF;
    IF OLD.outcome_code <> 'in_flight' OR NEW.outcome_code = 'in_flight' OR NEW.finished_at IS NULL
        OR (to_jsonb(NEW) - 'outcome_code' - 'finished_at') IS DISTINCT FROM (to_jsonb(OLD) - 'outcome_code' - 'finished_at') THEN
        RAISE EXCEPTION 'a deployment attempt is only finished once' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;
DROP TRIGGER IF EXISTS deployment_attempts_append_only ON endpoints.deployment_attempts;
CREATE TRIGGER deployment_attempts_append_only BEFORE UPDATE OR DELETE ON endpoints.deployment_attempts
    FOR EACH ROW EXECUTE FUNCTION endpoints.check_deployment_attempt_change();
DROP TRIGGER IF EXISTS deployment_attempts_no_truncate ON endpoints.deployment_attempts;
CREATE TRIGGER deployment_attempts_no_truncate BEFORE TRUNCATE ON endpoints.deployment_attempts
    FOR EACH STATEMENT EXECUTE FUNCTION endpoints.forbid_deployment_history_change();

-- deployment_evidence_conflict: the Management Observation and the Device's software inventory contradict each other
-- for a Deployment Target. It is a Device finding (one open per Device).
-- deployment_clear_failed: clearing the assignment of a ring failed 10 times; the provider may still hold it. It is raised
-- on a Device of the ring (the first target) with the Deployment and ring ids in the detail.
ALTER TABLE endpoints.findings DROP CONSTRAINT IF EXISTS findings_kind_check;
ALTER TABLE endpoints.findings ADD CONSTRAINT findings_kind_check
    CHECK (kind IN ('no_asset_match', 'serial_conflict', 'duplicate_device', 'unmatched_software', 'provider_reported_error', 'assignment_ineffective',
        'package_hash_mismatch', 'package_published_after_revoke', 'deployment_evidence_conflict', 'deployment_clear_failed')) NOT VALID;
ALTER TABLE endpoints.findings VALIDATE CONSTRAINT findings_kind_check;
