CREATE TABLE IF NOT EXISTS platform.sessions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    token_hash bytea NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    -- No foreign key: the platform schema must not depend on business schemas.
    user_id uuid NOT NULL,
    auth_method text NOT NULL CHECK (auth_method <> ''),
    created_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    idle_expires_at timestamptz NOT NULL,
    absolute_expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CHECK (idle_expires_at <= absolute_expires_at)
);
CREATE INDEX IF NOT EXISTS sessions_user_active_idx ON platform.sessions(user_id) WHERE revoked_at IS NULL;
