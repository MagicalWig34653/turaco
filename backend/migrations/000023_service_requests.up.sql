-- Service Requests (F3 slice 5). The catalog definition and the answers are
-- snapshotted with the request; typed references are promoted out of the
-- answers; fulfillment tasks are linked in request_tasks.
CREATE SCHEMA IF NOT EXISTS requests;

CREATE SEQUENCE IF NOT EXISTS requests.request_number_seq;

CREATE TABLE IF NOT EXISTS requests.service_requests (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    -- Human reference, separate from the primary key (REQ-2026-000001).
    reference text NOT NULL DEFAULT ('REQ-' || to_char(now(), 'YYYY') || '-' || lpad(nextval('requests.request_number_seq')::text, 6, '0')),
    catalog_item_id uuid NOT NULL,
    catalog_item_key text NOT NULL,
    catalog_item_title text NOT NULL,
    definition jsonb NOT NULL CHECK (jsonb_typeof(definition) = 'object'),
    answers jsonb NOT NULL CHECK (jsonb_typeof(answers) = 'object'),
    requester_user_id uuid NOT NULL,
    requested_for_user_id uuid NOT NULL,
    status text NOT NULL CHECK (status IN ('pending_approval', 'in_fulfillment', 'waiting', 'completed', 'rejected', 'cancelled')),
    waiting_reason text CHECK (waiting_reason IN ('stock', 'supplier', 'requester', 'external_system')),
    status_reason text,
    -- Index of the approval step that is pending; set exactly while pending_approval.
    current_approval_step integer CHECK (current_approval_step >= 0),
    submitted_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT service_requests_reference_unique UNIQUE (reference),
    CONSTRAINT service_requests_waiting_matches CHECK ((status = 'waiting') = (waiting_reason IS NOT NULL)),
    CONSTRAINT service_requests_step_matches CHECK ((status = 'pending_approval') = (current_approval_step IS NOT NULL)),
    CONSTRAINT service_requests_completed_matches CHECK ((status = 'completed') = (completed_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS service_requests_requester_idx ON requests.service_requests (requester_user_id, id DESC);
CREATE INDEX IF NOT EXISTS service_requests_requested_for_idx ON requests.service_requests (requested_for_user_id, id DESC);
CREATE INDEX IF NOT EXISTS service_requests_open_idx ON requests.service_requests (id DESC) WHERE status IN ('pending_approval', 'in_fulfillment', 'waiting');

-- Answers that point at other records, kept queryable (ADR-0025).
CREATE TABLE IF NOT EXISTS requests.request_references (
    request_id uuid NOT NULL REFERENCES requests.service_requests(id) ON DELETE CASCADE,
    field_key text NOT NULL,
    ref_type text NOT NULL CHECK (ref_type IN ('user', 'product')),
    ref_id uuid NOT NULL,
    PRIMARY KEY (request_id, field_key)
);
CREATE INDEX IF NOT EXISTS request_references_target_idx ON requests.request_references (ref_type, ref_id);

-- Fulfillment tasks of a request; a task belongs to at most one request.
CREATE TABLE IF NOT EXISTS requests.request_tasks (
    request_id uuid NOT NULL REFERENCES requests.service_requests(id) ON DELETE CASCADE,
    task_id uuid NOT NULL,
    template_index integer NOT NULL CHECK (template_index >= 0),
    mandatory boolean NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (request_id, task_id),
    CONSTRAINT request_tasks_task_unique UNIQUE (task_id)
);
