CREATE SCHEMA IF NOT EXISTS approvals;

-- Shared Approval model (F3 slice 3). An approval is one decision a User or
-- any member of a Team has to make about a subject owned by another module
-- (for example a service request). Steps are created one at a time by the
-- subject's module; decisions are immutable.
CREATE TABLE IF NOT EXISTS approvals.approvals (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    subject_type text NOT NULL CHECK (subject_type ~ '^[a-z][a-z_]{1,39}$'),
    subject_id uuid NOT NULL,
    -- Display text for the inbox, set by the subject's module (never answers).
    subject_label text NOT NULL CHECK (btrim(subject_label) <> ''),
    step_index integer NOT NULL CHECK (step_index >= 0),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled')),
    approver_user_id uuid,
    approver_team_id uuid,
    -- Users who must not decide (the requester and the requested-for User).
    excluded_user_ids uuid[] NOT NULL DEFAULT '{}',
    requested_by_user_id uuid,
    decided_by_user_id uuid,
    decided_at timestamptz,
    decision_comment text,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT approvals_one_approver CHECK ((approver_user_id IS NULL) <> (approver_team_id IS NULL)),
    CONSTRAINT approvals_decision_matches_status CHECK ((status IN ('approved', 'rejected')) = (decided_at IS NOT NULL AND decided_by_user_id IS NOT NULL)),
    CONSTRAINT approvals_step_unique UNIQUE (subject_type, subject_id, step_index)
);
CREATE INDEX IF NOT EXISTS approvals_pending_user_idx ON approvals.approvals (approver_user_id, id) WHERE status = 'pending' AND approver_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS approvals_pending_team_idx ON approvals.approvals (approver_team_id, id) WHERE status = 'pending' AND approver_team_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS approvals_decided_by_idx ON approvals.approvals (decided_by_user_id, id) WHERE decided_by_user_id IS NOT NULL;
