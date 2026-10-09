-- Result note of a completed Task: the closing comment the person who finished the work leaves for the assigning
-- team (bounded, plain text, only while the task is completed; cleared when the task is reopened).
ALTER TABLE platform.tasks
    ADD COLUMN IF NOT EXISTS result_note text,
    ADD CONSTRAINT tasks_result_note_valid CHECK (result_note IS NULL OR (status = 'completed' AND length(result_note) BETWEEN 1 AND 1000));
