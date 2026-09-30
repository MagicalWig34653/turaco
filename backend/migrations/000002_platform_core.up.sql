CREATE TABLE IF NOT EXISTS platform.audit_events (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    occurred_at timestamptz NOT NULL DEFAULT now(),
    actor_id uuid,
    action text NOT NULL,
    target_type text NOT NULL,
    target_id text NOT NULL,
    correlation_id text NOT NULL,
    before_data jsonb,
    after_data jsonb,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS audit_events_target_idx ON platform.audit_events(target_type, target_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS audit_events_correlation_idx ON platform.audit_events(correlation_id);

CREATE TABLE IF NOT EXISTS platform.outbox_events (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    event_type text NOT NULL,
    event_version integer NOT NULL CHECK (event_version > 0),
    occurred_at timestamptz NOT NULL DEFAULT now(),
    actor_id uuid,
    correlation_id text NOT NULL,
    payload jsonb NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','processed','failed')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz,
    last_error text
);
CREATE INDEX IF NOT EXISTS outbox_pending_idx ON platform.outbox_events(status, available_at, occurred_at) WHERE status IN ('pending','failed');

CREATE TABLE IF NOT EXISTS platform.jobs (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    job_type text NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','completed','failed','cancelled')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts integer NOT NULL DEFAULT 5 CHECK (max_attempts > 0),
    available_at timestamptz NOT NULL DEFAULT now(),
    locked_at timestamptz,
    locked_by text,
    completed_at timestamptz,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS jobs_claim_idx ON platform.jobs(status, available_at, created_at) WHERE status IN ('pending','failed');
