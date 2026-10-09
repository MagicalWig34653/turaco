-- F14 slice A-A2: local accounts with invitation and reset tokens (ADR-0034).
-- Forward-only. platform.local_credentials becomes the store of two credential kinds: the CLI-only emergency
-- (break-glass) account and local accounts of Users outside the directory. Platform tables carry no foreign keys
-- into business schemas, so user_id stays a plain uuid.

ALTER TABLE platform.local_credentials
    ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'emergency',
    ADD COLUMN IF NOT EXISTS failed_attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS locked_until timestamptz,
    ALTER COLUMN password_hash DROP NOT NULL;

ALTER TABLE platform.local_credentials
    DROP CONSTRAINT IF EXISTS local_credentials_password_hash_check,
    ADD CONSTRAINT local_credentials_kind_valid CHECK (kind IN ('emergency', 'local')),
    ADD CONSTRAINT local_credentials_hash_format CHECK (password_hash IS NULL OR password_hash LIKE '$argon2id$%'),
    -- A local account exists (disabled, without a password) from the moment it is created until the invitation is
    -- redeemed; the emergency account always has a hash.
    ADD CONSTRAINT local_credentials_hash_required CHECK (kind = 'local' OR password_hash IS NOT NULL),
    ADD CONSTRAINT local_credentials_enabled_needs_hash CHECK (NOT enabled OR password_hash IS NOT NULL),
    ADD CONSTRAINT local_credentials_failed_attempts CHECK (failed_attempts >= 0);

-- Single-use invitation and reset tokens. Only the SHA-256 hash of the 256-bit random token is stored.
CREATE TABLE IF NOT EXISTS platform.credential_tokens (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL,
    purpose text NOT NULL CHECK (purpose IN ('invitation', 'reset')),
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    -- {"userId": ...} or {"actor": ...} like role_assignments.created_by.
    created_by jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (expires_at > created_at)
);
CREATE INDEX IF NOT EXISTS credential_tokens_user_open_idx ON platform.credential_tokens (user_id, purpose) WHERE used_at IS NULL;
CREATE INDEX IF NOT EXISTS credential_tokens_expiry_idx ON platform.credential_tokens (expires_at);
