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
    -- The maintenance window the approver approved; Schedule may only narrow it.
    approved_window_start timestamptz,
    approved_window_end timestamptz,
    -- The User who approved an emergency change on its justification; they never review or close it.
    emergency_approved_by uuid,
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
    -- List filters rely on this bound (window_start >= from - 30 days).
    CONSTRAINT changes_window_max CHECK (window_end IS NULL OR window_end - window_start <= interval '30 days'),
    CONSTRAINT changes_scheduled_has_window CHECK (status NOT IN ('scheduled', 'in_progress') OR window_start IS NOT NULL),
    CONSTRAINT changes_approved_window_pair CHECK ((approved_window_start IS NULL) = (approved_window_end IS NULL)),
    -- Per-status invariants (docs/domain/state-machines.md#change).
    CONSTRAINT changes_started_at CHECK ((started_at IS NOT NULL) = (status IN ('in_progress', 'completed', 'failed', 'review', 'closed'))),
    CONSTRAINT changes_completed_at CHECK ((completed_at IS NOT NULL) = (status IN ('completed', 'failed', 'review', 'closed'))),
    CONSTRAINT changes_closed_at CHECK ((closed_at IS NOT NULL) = (status IN ('closed', 'cancelled', 'rejected'))),
    CONSTRAINT changes_time_order CHECK ((started_at IS NULL OR completed_at IS NULL OR started_at <= completed_at)
        AND (completed_at IS NULL OR closed_at IS NULL OR completed_at <= closed_at)
        AND (started_at IS NULL OR closed_at IS NULL OR started_at <= closed_at)),
    CONSTRAINT changes_failed_outcome CHECK (status <> 'failed' OR (rollback_done IS NOT NULL AND status_reason IS NOT NULL)),
    CONSTRAINT changes_pending_has_approval CHECK (status <> 'pending_approval' OR approval_id IS NOT NULL),
    -- A review records an outcome note; an emergency change is only closed after its review.
    CONSTRAINT changes_review_note CHECK (outcome_note IS NOT NULL OR NOT (status = 'review' OR (status = 'closed' AND kind = 'emergency'))),
    CONSTRAINT changes_emergency_only CHECK (emergency_justification IS NULL OR kind = 'emergency'),
    CONSTRAINT changes_emergency_approver CHECK ((emergency_justification IS NULL) = (emergency_approved_by IS NULL))
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
    change_id uuid NOT NULL REFERENCES changes.changes(id) ON DELETE RESTRICT,
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

-- Transitions and Task links are history: no update, delete or truncate (the
-- pattern of endpoints.device_observation_history), and Changes holding them
-- cannot be deleted.
CREATE OR REPLACE FUNCTION changes.forbid_history_change() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'change history rows are append-only (%.%)', TG_TABLE_SCHEMA, TG_TABLE_NAME USING ERRCODE = 'restrict_violation';
END
$$;

DROP TRIGGER IF EXISTS change_transitions_append_only ON changes.change_transitions;
CREATE TRIGGER change_transitions_append_only BEFORE UPDATE OR DELETE ON changes.change_transitions
    FOR EACH ROW EXECUTE FUNCTION changes.forbid_history_change();
DROP TRIGGER IF EXISTS change_transitions_no_truncate ON changes.change_transitions;
CREATE TRIGGER change_transitions_no_truncate BEFORE TRUNCATE ON changes.change_transitions
    FOR EACH STATEMENT EXECUTE FUNCTION changes.forbid_history_change();

-- Execution Tasks of a change (Tasks own them; context type 'change').
CREATE TABLE IF NOT EXISTS changes.change_tasks (
    change_id uuid NOT NULL REFERENCES changes.changes(id) ON DELETE RESTRICT,
    task_id uuid NOT NULL,
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (change_id, task_id)
);

DROP TRIGGER IF EXISTS change_tasks_append_only ON changes.change_tasks;
CREATE TRIGGER change_tasks_append_only BEFORE UPDATE OR DELETE ON changes.change_tasks
    FOR EACH ROW EXECUTE FUNCTION changes.forbid_history_change();
DROP TRIGGER IF EXISTS change_tasks_no_truncate ON changes.change_tasks;
CREATE TRIGGER change_tasks_no_truncate BEFORE TRUNCATE ON changes.change_tasks
    FOR EACH STATEMENT EXECUTE FUNCTION changes.forbid_history_change();

-- A Change's AFFECTS links end when it reaches closed, cancelled or rejected;
-- its detail and the affected-resource list filter still read them by end
-- reason (relationships.OutgoingEnded, SourcesByTarget), so ended rows need
-- their own indexes (000044 indexes current rows only).
CREATE INDEX IF NOT EXISTS relationships_ended_source ON platform.relationships (source_type, source_id, end_reason, id) WHERE valid_until IS NOT NULL;
CREATE INDEX IF NOT EXISTS relationships_ended_target ON platform.relationships (target_type, target_id, type, source_type) WHERE valid_until IS NOT NULL;
