-- Task lifecycle (F2 slice 2): reason for blocked/cancelled, who created and
-- completed a task, an optimistic-concurrency version, and state invariants.
-- platform.tasks has no writers before this slice, so the constraints hold.
ALTER TABLE platform.tasks
    ADD COLUMN IF NOT EXISTS status_reason text,
    ADD COLUMN IF NOT EXISTS created_by_user_id uuid,
    ADD COLUMN IF NOT EXISTS completed_by_user_id uuid,
    ADD COLUMN IF NOT EXISTS version integer NOT NULL DEFAULT 1;

ALTER TABLE platform.tasks
    ADD CONSTRAINT tasks_version_positive CHECK (version > 0),
    ADD CONSTRAINT tasks_title_not_empty CHECK (title <> ''),
    ADD CONSTRAINT tasks_reason_matches_status CHECK ((status IN ('blocked', 'cancelled')) = (status_reason IS NOT NULL)),
    ADD CONSTRAINT tasks_completed_matches_status CHECK ((status = 'completed') = (completed_at IS NOT NULL)),
    ADD CONSTRAINT tasks_context_pair CHECK ((context_type IS NULL) = (context_id IS NULL));
