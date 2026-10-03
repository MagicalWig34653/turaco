-- Problems and Known Errors (F5 part 2): underlying causes of repeating incidents. A Known Error
-- is a Problem in the status known_error with cause and workaround.
CREATE SEQUENCE IF NOT EXISTS servicedesk.problem_number_seq;

CREATE OR REPLACE FUNCTION servicedesk.next_problem_reference() RETURNS text
LANGUAGE sql
AS $$
    SELECT 'PRB-' || CASE WHEN n < 1000000 THEN lpad(n::text, 6, '0') ELSE n::text END
    FROM (SELECT nextval('servicedesk.problem_number_seq') AS n) AS s
$$;

CREATE TABLE IF NOT EXISTS servicedesk.problems (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    reference text NOT NULL DEFAULT servicedesk.next_problem_reference(),
    title text NOT NULL CHECK (title = btrim(title) AND length(title) BETWEEN 1 AND 200),
    description text CHECK (description IS NULL OR length(description) <= 5000),
    status text NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'under_investigation', 'cause_identified', 'known_error', 'resolution_planned', 'resolved', 'closed')),
    cause text CHECK (cause IS NULL OR length(cause) <= 5000),
    workaround text CHECK (workaround IS NULL OR length(workaround) <= 5000),
    resolution text CHECK (resolution IS NULL OR length(resolution) <= 5000),
    owner_user_id uuid,
    created_by uuid,
    resolved_at timestamptz,
    closed_at timestamptz,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT problems_reference_unique UNIQUE (reference),
    CONSTRAINT problems_known_error_has_cause CHECK (status NOT IN ('cause_identified', 'known_error', 'resolution_planned') OR cause IS NOT NULL),
    CONSTRAINT problems_known_error_has_workaround CHECK (status NOT IN ('known_error', 'resolution_planned') OR workaround IS NOT NULL),
    CONSTRAINT problems_resolved_matches CHECK ((status IN ('resolved', 'closed')) = (resolved_at IS NOT NULL)),
    CONSTRAINT problems_closed_matches CHECK ((status = 'closed') = (closed_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS problems_status_idx ON servicedesk.problems (status, id DESC);

CREATE TABLE IF NOT EXISTS servicedesk.problem_tickets (
    problem_id uuid NOT NULL REFERENCES servicedesk.problems(id) ON DELETE CASCADE,
    ticket_id uuid NOT NULL REFERENCES servicedesk.tickets(id) ON DELETE CASCADE,
    linked_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (problem_id, ticket_id)
);
CREATE INDEX IF NOT EXISTS problem_tickets_ticket_idx ON servicedesk.problem_tickets (ticket_id);
