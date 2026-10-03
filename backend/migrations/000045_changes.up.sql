-- Changes (F7c, docs/product/f7-infrastructure-change-design.md, slice 3).
-- A Change is its own lifecycle record; approval is an Approval subject, execution
-- steps are Tasks (context type 'change') and affected resources are platform
-- Relationships ('change AFFECTS ...'), so none of those are stored here.
CREATE SCHEMA IF NOT EXISTS changes;

CREATE SEQUENCE IF NOT EXISTS changes.change_number_seq;

-- Human reference CHG-000001; the width grows instead of truncating.
CREATE OR REPLACE FUNCTION changes.next_reference() RETURNS text
LANGUAGE sql
AS $$
    SELECT 'CHG-' || CASE WHEN n < 1000000 THEN lpad(n::text, 6, '0') ELSE n::text END
    FROM (SELECT nextval('changes.change_number_seq') AS n) AS s
$$;

CREATE TABLE IF NOT EXISTS changes.changes (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    reference text NOT NULL DEFAULT changes.next_reference() UNIQUE,
    title text NOT NULL CHECK (title = btrim(title) AND length(title) BETWEEN 1 AND 150),
    description text CHECK (description IS NULL OR length(description) BETWEEN 1 AND 4000),
    kind text NOT NULL CHECK (kind IN ('standard', 'normal', 'emergency')),
    risk text NOT NULL CHECK (risk IN ('low', 'medium', 'high')),
    status text NOT NULL DEFAULT 'draft' CHECK (status IN (
        'draft', 'assessment', 'pending_approval', 'approved', 'scheduled', 'in_progress',
        'completed', 'review', 'closed', 'rejected', 'failed', 'cancelled')),
    -- Reason code of the last exceptional transition (cancel, fail, reject, task waiver).
    status_reason text CHECK (status_reason IS NULL OR status_reason ~ '^[a-z][a-z_]{0,39}$'),
    -- Organization Users by id (the Organization module owns them); owner is the assignee who executes.
    requester_user_id uuid NOT NULL,
    owner_user_id uuid,
    rollback_plan text CHECK (rollback_plan IS NULL OR length(rollback_plan) BETWEEN 1 AND 4000),
    emergency_justification text CHECK (emergency_justification IS NULL OR length(emergency_justification) BETWEEN 1 AND 1000),
    -- The maintenance window is a pair of fields, not a scheduling subsystem.
    window_start timestamptz,
    window_end timestamptz,
    outcome_note text CHECK (outcome_note IS NULL OR length(outcome_note) BETWEEN 1 AND 1000),
    -- Whether a failed change was rolled back; set by fail.
    rollback_done boolean,
    -- The Approval of the current approval step (reference only; Approvals own the record).
    approval_id uuid,
    -- Users who edited the change; with the requester they can never approve it.
    editors uuid[] NOT NULL DEFAULT '{}',
    -- The window start for which the "starts soon" reminder was already sent.
    reminded_for timestamptz,
    started_at timestamptz,
    completed_at timestamptz,
    closed_at timestamptz,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT changes_window_pair CHECK ((window_start IS NULL) = (window_end IS NULL)),
    CONSTRAINT changes_window_order CHECK (window_end IS NULL OR window_end > window_start),
    CONSTRAINT changes_window_max CHECK (window_end IS NULL OR window_end - window_start <= interval '30 days'),
    CONSTRAINT changes_scheduled_has_window CHECK (status NOT IN ('scheduled', 'in_progress') OR window_start IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS changes_status_idx ON changes.changes (status, id DESC);
CREATE INDEX IF NOT EXISTS changes_requester_idx ON changes.changes (requester_user_id, id DESC);
CREATE INDEX IF NOT EXISTS changes_owner_idx ON changes.changes (owner_user_id, id DESC) WHERE owner_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS changes_window_idx ON changes.changes (window_start) WHERE window_start IS NOT NULL;
-- The reminder job only looks at scheduled changes.
CREATE INDEX IF NOT EXISTS changes_scheduled_window_idx ON changes.changes (window_start) WHERE status = 'scheduled';

-- Append-only history of state transitions (what happened, by whom, why); reason codes only.
CREATE TABLE IF NOT EXISTS changes.change_transitions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    change_id uuid NOT NULL REFERENCES changes.changes(id) ON DELETE CASCADE,
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
CREATE INDEX IF NOT EXISTS change_transitions_change_idx ON changes.change_transitions (change_id, id);

CREATE OR REPLACE FUNCTION changes.forbid_transition_update() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'change transitions are append-only';
END
$$;

DROP TRIGGER IF EXISTS change_transitions_append_only ON changes.change_transitions;
CREATE TRIGGER change_transitions_append_only BEFORE UPDATE ON changes.change_transitions
    FOR EACH ROW EXECUTE FUNCTION changes.forbid_transition_update();

-- Execution Tasks of a change (Tasks own them; context type 'change').
CREATE TABLE IF NOT EXISTS changes.change_tasks (
    change_id uuid NOT NULL REFERENCES changes.changes(id) ON DELETE CASCADE,
    task_id uuid NOT NULL,
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (change_id, task_id)
);
