-- F1 slice 3b: password login throttling and the local emergency account.
-- See docs/security/identity-access-design.md §6 and §7.

-- Login attempt counters. key is "acct:<user id>", "id:<sha256 hex of the
-- normalized identifier>" or "ip:<client address, IPv6 as /64 prefix>"; neither
-- the identifier nor a password is stored. attempts counts reservations made
-- inside the window (see platform/authentication Throttle.Reserve); all times
-- are database times.
CREATE TABLE IF NOT EXISTS platform.auth_throttle (
    key text PRIMARY KEY CHECK (key ~ '^(acct|id|ip):'),
    attempts integer NOT NULL CHECK (attempts >= 0),
    window_started_at timestamptz NOT NULL,
    locked_until timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS auth_throttle_updated_idx ON platform.auth_throttle(updated_at);

-- Local (break-glass) credentials for emergency-account Users. Only an
-- argon2id PHC hash is stored.
CREATE TABLE IF NOT EXISTS platform.local_credentials (
    -- organization.users.id (no FK: platform must not depend on business schemas).
    user_id uuid PRIMARY KEY,
    login_name text NOT NULL UNIQUE CHECK (login_name ~ '^[a-z0-9][a-z0-9._-]{1,62}$'),
    password_hash text NOT NULL CHECK (password_hash LIKE '$argon2id$%'),
    enabled boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    password_changed_at timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz
);
