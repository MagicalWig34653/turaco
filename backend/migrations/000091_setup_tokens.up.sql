-- Single-use setup tokens for the first-administrator web page. Only the SHA-256 digest of a token is stored;
-- the plaintext is shown once by "turaco-admin admin setup-token". A token is usable while it is unused,
-- unexpired and no platform administrator exists (checked on every call, not here).
CREATE TABLE platform.setup_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    CHECK (expires_at > created_at)
);

CREATE INDEX setup_tokens_open_idx ON platform.setup_tokens (expires_at) WHERE used_at IS NULL;
