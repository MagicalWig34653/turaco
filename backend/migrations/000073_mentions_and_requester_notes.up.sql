-- Mentions in internal ticket comments: the people a staff note names. Only users who may view the ticket's Queue
-- are stored (checked by the service when the comment is added); the list drives the ticket.mention notification.
ALTER TABLE servicedesk.ticket_comments
    ADD COLUMN IF NOT EXISTS mentioned_user_ids uuid[] NOT NULL DEFAULT '{}',
    ADD CONSTRAINT ticket_comments_mentions_valid CHECK (cardinality(mentioned_user_ids) <= 10 AND (internal OR cardinality(mentioned_user_ids) = 0));

-- Result note visibility: the completer of a task may mark the closing note as meant for the requester of the
-- request the task belongs to. Notes stay internal by default.
ALTER TABLE platform.tasks
    ADD COLUMN IF NOT EXISTS result_note_for_requester boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT tasks_result_note_for_requester_valid CHECK (NOT result_note_for_requester OR result_note IS NOT NULL);
