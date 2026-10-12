-- Turaco AI, F12 slice A-D: the read-only Turaco MCP server and its OAuth authorization server role
-- (docs/product/f12-turaco-ai-design.md#a-d-mcp-server-design, ADR-0039). Owned by platform/ai.
-- Tokens and codes are stored only as SHA-256 hashes; no client secret exists (public clients with PKCE).

-- Runtime switch of the MCP server (default off). AI_MCP_ENABLED is the startup gate.
ALTER TABLE ai.settings ADD COLUMN IF NOT EXISTS mcp_enabled boolean NOT NULL DEFAULT false;

-- Administrator-managed allowlist of OAuth clients. No dynamic client registration.
CREATE TABLE IF NOT EXISTS ai.mcp_clients (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    client_id text NOT NULL UNIQUE CHECK (client_id ~ '^tmcp_[a-z2-7]{26}$'),
    display_name text NOT NULL CHECK (display_name = btrim(display_name) AND length(display_name) BETWEEN 1 AND 100),
    redirect_uris text[] NOT NULL CHECK (cardinality(redirect_uris) BETWEEN 1 AND 5),
    allowed_data_classes text[] NOT NULL DEFAULT '{}'
        CHECK (allowed_data_classes <@ ARRAY['public_reference', 'business_record', 'personal_contact', 'device_context']::text[]),
    enabled boolean NOT NULL DEFAULT true,
    created_by uuid,
    updated_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    version integer NOT NULL DEFAULT 1
);

-- A grant is one User's consent for one client and the MCP resource. It ends at expires_at (at most 24 hours) or on
-- revocation; every token belongs to exactly one grant.
CREATE TABLE IF NOT EXISTS ai.mcp_grants (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    client_ref uuid NOT NULL REFERENCES ai.mcp_clients (id),
    tenant_id text NOT NULL CHECK (length(tenant_id) BETWEEN 1 AND 100),
    user_id uuid NOT NULL,
    resource text NOT NULL CHECK (length(resource) BETWEEN 1 AND 500),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    last_used_at timestamptz,
    revoked_at timestamptz,
    revoked_reason text CHECK (revoked_reason IS NULL OR revoked_reason IN
        ('user', 'client_revocation', 'refresh_replay', 'code_replay', 'client_disabled')),
    CONSTRAINT mcp_grants_revocation_complete CHECK ((revoked_at IS NULL) = (revoked_reason IS NULL))
);
CREATE INDEX IF NOT EXISTS mcp_grants_user_idx ON ai.mcp_grants (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS mcp_grants_client_idx ON ai.mcp_grants (client_ref) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS mcp_grants_expires_idx ON ai.mcp_grants (expires_at);

-- Authorization codes: 60 seconds, single use. grant_id is set when the code is redeemed, so a replay can revoke it.
CREATE TABLE IF NOT EXISTS ai.mcp_codes (
    code_hash bytea PRIMARY KEY CHECK (length(code_hash) = 32),
    client_ref uuid NOT NULL REFERENCES ai.mcp_clients (id) ON DELETE CASCADE,
    tenant_id text NOT NULL CHECK (length(tenant_id) BETWEEN 1 AND 100),
    user_id uuid NOT NULL,
    redirect_uri text NOT NULL CHECK (length(redirect_uri) BETWEEN 1 AND 500),
    code_challenge text NOT NULL CHECK (code_challenge ~ '^[A-Za-z0-9_-]{43}$'),
    resource text NOT NULL CHECK (length(resource) BETWEEN 1 AND 500),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    grant_id uuid REFERENCES ai.mcp_grants (id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS mcp_codes_expires_idx ON ai.mcp_codes (expires_at);

-- Access and refresh tokens. audience is the resource the token is bound to; refresh tokens are single use (used_at).
CREATE TABLE IF NOT EXISTS ai.mcp_tokens (
    token_hash bytea PRIMARY KEY CHECK (length(token_hash) = 32),
    grant_id uuid NOT NULL REFERENCES ai.mcp_grants (id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('access', 'refresh')),
    audience text NOT NULL CHECK (length(audience) BETWEEN 1 AND 500),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    CONSTRAINT mcp_tokens_access_not_used CHECK (kind = 'refresh' OR used_at IS NULL)
);
CREATE INDEX IF NOT EXISTS mcp_tokens_grant_idx ON ai.mcp_tokens (grant_id);
CREATE INDEX IF NOT EXISTS mcp_tokens_expires_idx ON ai.mcp_tokens (expires_at);
