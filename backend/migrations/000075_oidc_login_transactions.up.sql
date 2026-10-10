-- Server-side state of an OpenID Connect sign-in in progress (Microsoft Entra, ADR-0035, slice E-A).
-- Only SHA-256 hashes of state and browser binding are stored; the nonce and the PKCE verifier live for
-- ten minutes (the transaction is consumed on first use, also on failure) and are useless afterwards.
CREATE TABLE IF NOT EXISTS platform.oidc_login_transactions (
    state_hash   bytea PRIMARY KEY,
    binding_hash bytea NOT NULL,
    nonce        text NOT NULL,
    code_verifier text NOT NULL,
    return_to    text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS oidc_login_transactions_created_idx ON platform.oidc_login_transactions (created_at);
