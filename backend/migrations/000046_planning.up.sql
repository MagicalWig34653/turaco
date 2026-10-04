-- Planning (F7d, docs/product/f7-infrastructure-change-design.md, slice 4).
-- An Initiative is its own lifecycle record (docs/domain/state-machines.md#initiative);
-- its approval is an Approval subject and the Changes, Tasks, Procurement
-- Requests and Services it includes are platform Relationships
-- ('initiative INCLUDES ...'), so none of those are stored here.
CREATE SCHEMA IF NOT EXISTS planning;

CREATE SEQUENCE IF NOT EXISTS planning.initiative_number_seq;

-- Human reference INI-000001; the width grows instead of truncating.
CREATE OR REPLACE FUNCTION planning.next_reference() RETURNS text
LANGUAGE sql
AS $$
    SELECT 'INI-' || CASE WHEN n < 1000000 THEN lpad(n::text, 6, '0') ELSE n::text END
    FROM (SELECT nextval('planning.initiative_number_seq') AS n) AS s
$$;

CREATE TABLE IF NOT EXISTS planning.initiatives (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    reference text NOT NULL DEFAULT planning.next_reference() UNIQUE,
    title text NOT NULL CHECK (title = btrim(title) AND length(title) BETWEEN 1 AND 150),
    goal text CHECK (goal IS NULL OR length(goal) BETWEEN 1 AND 4000),
    -- Organization User by id (the Organization module owns it).
    owner_user_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'idea' CHECK (status IN (
        'idea', 'planning', 'proposed', 'approved', 'active', 'on_hold', 'completed', 'cancelled')),
    -- Reason code of the last exceptional transition (hold, cancel, approval rejected).
    status_reason text CHECK (status_reason IS NULL OR status_reason ~ '^[a-z][a-z_]{0,39}$'),
    target_date date,
    -- The Approval of the current or last proposal (reference only; Approvals own the record).
    approval_id uuid,
    -- The User who proposed the Initiative; with the editors and the owner they can never approve it.
    proposed_by uuid,
    editors uuid[] NOT NULL DEFAULT '{}',
    created_by uuid NOT NULL,
    approved_at timestamptz,
    activated_at timestamptz,
    closed_at timestamptz,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    -- Per-status invariants (docs/domain/state-machines.md#initiative).
    CONSTRAINT initiatives_proposed_has_approval CHECK (status <> 'proposed' OR (approval_id IS NOT NULL AND proposed_by IS NOT NULL)),
    CONSTRAINT initiatives_approved_at CHECK (
        (status NOT IN ('approved', 'active', 'on_hold', 'completed') OR approved_at IS NOT NULL)
        AND (status NOT IN ('idea', 'planning', 'proposed') OR approved_at IS NULL)),
    CONSTRAINT initiatives_activated_at CHECK (status = 'cancelled' OR (activated_at IS NOT NULL) = (status IN ('active', 'on_hold', 'completed'))),
    CONSTRAINT initiatives_closed_at CHECK ((closed_at IS NOT NULL) = (status IN ('completed', 'cancelled'))),
    CONSTRAINT initiatives_reason_required CHECK (status NOT IN ('on_hold', 'cancelled') OR status_reason IS NOT NULL),
    CONSTRAINT initiatives_time_order CHECK ((approved_at IS NULL OR activated_at IS NULL OR approved_at <= activated_at)
        AND (activated_at IS NULL OR closed_at IS NULL OR activated_at <= closed_at)
        AND (approved_at IS NULL OR closed_at IS NULL OR approved_at <= closed_at))
);
CREATE INDEX IF NOT EXISTS initiatives_status_idx ON planning.initiatives (status, id DESC);
CREATE INDEX IF NOT EXISTS initiatives_owner_idx ON planning.initiatives (owner_user_id, id DESC);
CREATE INDEX IF NOT EXISTS initiatives_target_idx ON planning.initiatives (target_date) WHERE target_date IS NOT NULL;

-- Milestones are dated entries of an Initiative; removal keeps the row with a reason.
CREATE TABLE IF NOT EXISTS planning.milestones (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    initiative_id uuid NOT NULL REFERENCES planning.initiatives(id) ON DELETE RESTRICT,
    title text NOT NULL CHECK (title = btrim(title) AND length(title) BETWEEN 1 AND 150),
    due_date date NOT NULL,
    position integer NOT NULL CHECK (position BETWEEN 0 AND 10000),
    done_at timestamptz,
    done_by uuid,
    removed_at timestamptz,
    remove_reason text CHECK (remove_reason IS NULL OR remove_reason ~ '^[a-z][a-z_]{0,39}$'),
    removed_by uuid,
    created_by uuid,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT milestones_removed CHECK ((removed_at IS NULL) = (remove_reason IS NULL)),
    CONSTRAINT milestones_done_by CHECK (done_by IS NULL OR done_at IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS milestones_initiative_idx ON planning.milestones (initiative_id, position, due_date, id) WHERE removed_at IS NULL;
-- Due milestones (briefing) look at open ones by date.
CREATE INDEX IF NOT EXISTS milestones_due_idx ON planning.milestones (due_date, id) WHERE removed_at IS NULL AND done_at IS NULL;

-- Append-only history of state transitions (what happened, by whom, why); reason codes only.
CREATE TABLE IF NOT EXISTS planning.initiative_transitions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    initiative_id uuid NOT NULL REFERENCES planning.initiatives(id) ON DELETE RESTRICT,
    from_status text,
    to_status text NOT NULL,
    operation text NOT NULL CHECK (operation ~ '^[a-z][a-z_]{0,39}$'),
    reason text CHECK (reason IS NULL OR reason ~ '^[a-z][a-z_]{0,39}$'),
    -- Exactly one of a User and a system actor name.
    actor_user_id uuid,
    actor_system text,
    correlation_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((actor_user_id IS NULL) <> (actor_system IS NULL))
);
CREATE INDEX IF NOT EXISTS initiative_transitions_initiative_idx ON planning.initiative_transitions (initiative_id, id);

CREATE OR REPLACE FUNCTION planning.forbid_history_change() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'initiative history rows are append-only (%.%)', TG_TABLE_SCHEMA, TG_TABLE_NAME USING ERRCODE = 'restrict_violation';
END
$$;

DROP TRIGGER IF EXISTS initiative_transitions_append_only ON planning.initiative_transitions;
CREATE TRIGGER initiative_transitions_append_only BEFORE UPDATE OR DELETE ON planning.initiative_transitions
    FOR EACH ROW EXECUTE FUNCTION planning.forbid_history_change();
DROP TRIGGER IF EXISTS initiative_transitions_no_truncate ON planning.initiative_transitions;
CREATE TRIGGER initiative_transitions_no_truncate BEFORE TRUNCATE ON planning.initiative_transitions
    FOR EACH STATEMENT EXECUTE FUNCTION planning.forbid_history_change();
