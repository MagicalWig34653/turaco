-- Cancelling a request cancels its tasks by context; without an index that scans every open task.
CREATE INDEX IF NOT EXISTS tasks_context_idx ON platform.tasks (context_type, context_id) WHERE context_id IS NOT NULL;

-- Requests create approval steps one at a time; enforce it so a subject never has two pending steps.
CREATE UNIQUE INDEX IF NOT EXISTS approvals_one_pending_per_subject
    ON approvals.approvals (subject_type, subject_id) WHERE status = 'pending';
