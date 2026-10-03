-- Major Incidents (F5 slice 4): significant incidents with a public status timeline,
-- subscribers and linked tickets.
CREATE SEQUENCE IF NOT EXISTS servicedesk.major_incident_number_seq;

CREATE OR REPLACE FUNCTION servicedesk.next_major_reference() RETURNS text
LANGUAGE sql
AS $$
    SELECT 'MI-' || CASE WHEN n < 1000000 THEN lpad(n::text, 6, '0') ELSE n::text END
    FROM (SELECT nextval('servicedesk.major_incident_number_seq') AS n) AS s
$$;

CREATE TABLE IF NOT EXISTS servicedesk.major_incidents (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    reference text NOT NULL DEFAULT servicedesk.next_major_reference(),
    title text NOT NULL CHECK (title = btrim(title) AND length(title) BETWEEN 1 AND 200),
    -- The current public message everybody sees.
    summary text NOT NULL CHECK (length(summary) BETWEEN 1 AND 2000),
    status text NOT NULL DEFAULT 'identified' CHECK (status IN ('identified', 'investigating', 'mitigating', 'monitoring', 'resolved', 'closed')),
    declared_by uuid,
    resolved_at timestamptz,
    closed_at timestamptz,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT major_incidents_reference_unique UNIQUE (reference)
);
CREATE INDEX IF NOT EXISTS major_incidents_active_idx ON servicedesk.major_incidents (id DESC) WHERE status NOT IN ('resolved', 'closed');

CREATE TABLE IF NOT EXISTS servicedesk.major_incident_updates (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    major_incident_id uuid NOT NULL REFERENCES servicedesk.major_incidents(id) ON DELETE CASCADE,
    author_user_id uuid,
    status text NOT NULL,
    body text NOT NULL CHECK (length(body) BETWEEN 1 AND 2000),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS major_incident_updates_idx ON servicedesk.major_incident_updates (major_incident_id, id);

CREATE TABLE IF NOT EXISTS servicedesk.major_incident_subscriptions (
    major_incident_id uuid NOT NULL REFERENCES servicedesk.major_incidents(id) ON DELETE CASCADE,
    user_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (major_incident_id, user_id)
);

ALTER TABLE servicedesk.tickets ADD COLUMN IF NOT EXISTS major_incident_id uuid REFERENCES servicedesk.major_incidents(id);
CREATE INDEX IF NOT EXISTS tickets_major_incident_idx ON servicedesk.tickets (major_incident_id) WHERE major_incident_id IS NOT NULL;
