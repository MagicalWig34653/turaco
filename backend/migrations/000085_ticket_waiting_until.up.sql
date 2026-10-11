ALTER TABLE servicedesk.tickets ADD COLUMN waiting_until timestamptz;
ALTER TABLE servicedesk.tickets ADD CONSTRAINT tickets_waiting_until_only_while_waiting CHECK (waiting_until IS NULL OR status = 'waiting');
