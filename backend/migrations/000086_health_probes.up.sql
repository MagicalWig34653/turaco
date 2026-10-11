CREATE TABLE platform.health_probes (
    check_key text PRIMARY KEY,
    last_attempt_at timestamptz NOT NULL,
    status text NOT NULL CHECK (status IN ('ok', 'failing', 'unknown')),
    error_code text NOT NULL DEFAULT ''
);
