-- Service Desk (F5 slice 1): tickets (incidents) with public and internal comments
-- (docs/product/f5-service-desk-design.md). Users, teams and assets are referenced by id.
CREATE SCHEMA IF NOT EXISTS servicedesk;

CREATE SEQUENCE IF NOT EXISTS servicedesk.ticket_number_seq;

CREATE OR REPLACE FUNCTION servicedesk.next_reference() RETURNS text
LANGUAGE sql
AS $$
    SELECT 'TKT-' || CASE WHEN n < 1000000 THEN lpad(n::text, 6, '0') ELSE n::text END
    FROM (SELECT nextval('servicedesk.ticket_number_seq') AS n) AS s
$$;

CREATE TABLE IF NOT EXISTS servicedesk.tickets (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    reference text NOT NULL DEFAULT servicedesk.next_reference(),
    kind text NOT NULL DEFAULT 'incident' CHECK (kind IN ('incident')),
    title text NOT NULL CHECK (title = btrim(title) AND length(title) BETWEEN 1 AND 200),
    description text CHECK (description IS NULL OR length(description) <= 5000),
    status text NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'open', 'in_progress', 'waiting', 'resolved', 'closed', 'cancelled')),
    waiting_reason text CHECK (waiting_reason IN ('customer', 'vendor', 'external_service', 'scheduled_change', 'hardware')),
    status_reason text CHECK (status_reason IS NULL OR length(status_reason) <= 500),
    resolution text CHECK (resolution IS NULL OR length(resolution) <= 5000),
    priority text NOT NULL DEFAULT 'normal' CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
    reporter_user_id uuid NOT NULL,
    affected_user_id uuid NOT NULL,
    queue_team_id uuid,
    assignee_user_id uuid,
    asset_id uuid,
    -- What the asset looked like when the ticket was created (reference, product, serial, tag, status).
    device_snapshot jsonb CHECK (device_snapshot IS NULL OR jsonb_typeof(device_snapshot) = 'object'),
    resolved_at timestamptz,
    closed_at timestamptz,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tickets_reference_unique UNIQUE (reference),
    CONSTRAINT tickets_waiting_matches CHECK ((status = 'waiting') = (waiting_reason IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS tickets_reporter_idx ON servicedesk.tickets (reporter_user_id, id DESC);
CREATE INDEX IF NOT EXISTS tickets_affected_idx ON servicedesk.tickets (affected_user_id, id DESC);
CREATE INDEX IF NOT EXISTS tickets_open_idx ON servicedesk.tickets (id DESC) WHERE status IN ('new', 'open', 'in_progress', 'waiting');
CREATE INDEX IF NOT EXISTS tickets_assignee_idx ON servicedesk.tickets (assignee_user_id, id DESC) WHERE assignee_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS tickets_queue_idx ON servicedesk.tickets (queue_team_id, id DESC) WHERE queue_team_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS tickets_asset_idx ON servicedesk.tickets (asset_id, id DESC) WHERE asset_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS servicedesk.ticket_comments (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    ticket_id uuid NOT NULL REFERENCES servicedesk.tickets(id) ON DELETE CASCADE,
    author_user_id uuid NOT NULL,
    body text NOT NULL CHECK (length(body) BETWEEN 1 AND 5000),
    -- Internal comments are never shown to the reporter or the affected user.
    internal boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ticket_comments_ticket_idx ON servicedesk.ticket_comments (ticket_id, id);
