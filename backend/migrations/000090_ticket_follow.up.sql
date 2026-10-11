-- Requester notification preferences. Absence means following, preserving the existing
-- notification behavior for tickets created before this migration.
CREATE TABLE servicedesk.ticket_follows (
    ticket_id uuid NOT NULL REFERENCES servicedesk.tickets(id) ON DELETE CASCADE,
    user_id uuid NOT NULL,
    following boolean NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (ticket_id, user_id)
);
