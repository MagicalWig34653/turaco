-- Turaco AI, F12 slice A-A (docs/product/f12-turaco-ai-design.md, ADR-0029).
-- Owned by platform/ai. No business data: providers, settings, usage counters, server-held conversation sessions and the
-- optional retained transcript. Prompt and response text is never written to audit. The audit extension (A15) is at the end.
CREATE SCHEMA IF NOT EXISTS ai;

-- AI Providers. Installation level. Credentials are never stored: secret_ref names a deployment secret file (ADR-0014).
CREATE TABLE IF NOT EXISTS ai.providers (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    kind text NOT NULL CHECK (kind IN ('fake', 'openai_compatible')),
    display_name text NOT NULL CHECK (display_name = btrim(display_name) AND length(display_name) BETWEEN 1 AND 100),
    endpoint_url text NOT NULL DEFAULT '' CHECK (length(endpoint_url) <= 500),
    model text NOT NULL DEFAULT '' CHECK (length(model) <= 200),
    local boolean NOT NULL DEFAULT false,
    allowed_data_classes text[] NOT NULL DEFAULT '{}'
        CHECK (allowed_data_classes <@ ARRAY['public_reference', 'business_record', 'personal_contact', 'device_context']::text[]),
    dpa_recorded_on date,
    no_training_confirmed boolean NOT NULL DEFAULT false,
    region text NOT NULL DEFAULT '' CHECK (length(region) <= 40),
    secret_ref text CHECK (secret_ref IS NULL OR secret_ref ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    enabled boolean NOT NULL DEFAULT false,
    price_in_per_mtok numeric(12, 4) NOT NULL DEFAULT 0 CHECK (price_in_per_mtok >= 0),
    price_out_per_mtok numeric(12, 4) NOT NULL DEFAULT 0 CHECK (price_out_per_mtok >= 0),
    created_by uuid,
    updated_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    version integer NOT NULL DEFAULT 1,
    CONSTRAINT providers_endpoint_required CHECK (kind = 'fake' OR endpoint_url <> ''),
    CONSTRAINT providers_fake_is_local CHECK (kind <> 'fake' OR local),
    -- An external provider needs the recorded data processing agreement, the no-training confirmation and a region.
    CONSTRAINT providers_external_needs_dpa CHECK (local OR (dpa_recorded_on IS NOT NULL AND no_training_confirmed AND region <> ''))
);
-- At most one enabled provider per installation in A-A.
CREATE UNIQUE INDEX IF NOT EXISTS providers_one_enabled_idx ON ai.providers ((true)) WHERE enabled;

-- Single settings row. enabled is the runtime switch; AI_ENABLED is the startup gate.
CREATE TABLE IF NOT EXISTS ai.settings (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    enabled boolean NOT NULL DEFAULT false,
    retain_conversations boolean NOT NULL DEFAULT false,
    retention_days smallint NOT NULL DEFAULT 7 CHECK (retention_days BETWEEN 1 AND 30),
    user_requests_per_hour integer NOT NULL DEFAULT 30 CHECK (user_requests_per_hour BETWEEN 1 AND 10000),
    user_requests_per_day integer NOT NULL DEFAULT 200 CHECK (user_requests_per_day BETWEEN 1 AND 100000),
    user_tokens_per_day integer NOT NULL DEFAULT 500000 CHECK (user_tokens_per_day BETWEEN 1000 AND 100000000),
    installation_tokens_per_day integer NOT NULL DEFAULT 2000000 CHECK (installation_tokens_per_day BETWEEN 1000 AND 1000000000),
    max_output_tokens integer NOT NULL DEFAULT 1024 CHECK (max_output_tokens BETWEEN 64 AND 8192),
    max_tool_iterations smallint NOT NULL DEFAULT 6 CHECK (max_tool_iterations BETWEEN 1 AND 10),
    updated_by uuid,
    updated_at timestamptz NOT NULL DEFAULT now(),
    version integer NOT NULL DEFAULT 1
);
INSERT INTO ai.settings (singleton) VALUES (true) ON CONFLICT (singleton) DO NOTHING;

-- Server-held conversation state (A12). The browser only ever holds the opaque id; token_hash is its SHA-256.
CREATE TABLE IF NOT EXISTS ai.sessions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    token_hash bytea NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    tenant_id text NOT NULL CHECK (length(tenant_id) BETWEEN 1 AND 100),
    user_id uuid NOT NULL,
    auth_session_id uuid NOT NULL,
    provider_id uuid NOT NULL,
    transcript jsonb NOT NULL DEFAULT '[]'::jsonb,
    scope jsonb NOT NULL DEFAULT '[]'::jsonb,
    turn_count integer NOT NULL DEFAULT 0,
    version integer NOT NULL DEFAULT 1,
    busy_until timestamptz,
    -- Ownership token of the running turn: only its holder may renew, release or save.
    busy_token uuid,
    -- ai.conversations row of the retained transcript while retention is on.
    retention_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_active_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_expires_idx ON ai.sessions (expires_at);
CREATE INDEX IF NOT EXISTS sessions_user_idx ON ai.sessions (user_id);

-- Counters only. tokens_reserved is the worst case of calls in flight; reservations are conditional upserts.
CREATE TABLE IF NOT EXISTS ai.usage (
    tenant_id text NOT NULL,
    user_id uuid NOT NULL,
    day date NOT NULL,
    request_count integer NOT NULL DEFAULT 0 CHECK (request_count >= 0),
    tokens_in bigint NOT NULL DEFAULT 0 CHECK (tokens_in >= 0),
    tokens_out bigint NOT NULL DEFAULT 0 CHECK (tokens_out >= 0),
    tokens_reserved bigint NOT NULL DEFAULT 0 CHECK (tokens_reserved >= 0),
    estimated_cost_micro bigint NOT NULL DEFAULT 0 CHECK (estimated_cost_micro >= 0),
    PRIMARY KEY (tenant_id, user_id, day)
);
CREATE TABLE IF NOT EXISTS ai.usage_hours (
    tenant_id text NOT NULL,
    user_id uuid NOT NULL,
    hour timestamptz NOT NULL,
    request_count integer NOT NULL DEFAULT 0 CHECK (request_count >= 0),
    PRIMARY KEY (tenant_id, user_id, hour)
);
CREATE TABLE IF NOT EXISTS ai.installation_usage (
    tenant_id text NOT NULL,
    day date NOT NULL,
    tokens_used bigint NOT NULL DEFAULT 0 CHECK (tokens_used >= 0),
    tokens_reserved bigint NOT NULL DEFAULT 0 CHECK (tokens_reserved >= 0),
    PRIMARY KEY (tenant_id, day)
);

-- Retained transcript, written only while ai.settings.retain_conversations is on; user and assistant text only.
CREATE TABLE IF NOT EXISTS ai.conversations (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id text NOT NULL,
    user_id uuid NOT NULL,
    provider_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS conversations_expires_idx ON ai.conversations (expires_at);
CREATE TABLE IF NOT EXISTS ai.messages (
    conversation_id uuid NOT NULL REFERENCES ai.conversations (id) ON DELETE CASCADE,
    seq integer NOT NULL,
    role text NOT NULL CHECK (role IN ('user', 'assistant')),
    content text NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (conversation_id, seq)
);
CREATE INDEX IF NOT EXISTS messages_expires_idx ON ai.messages (expires_at);

-- Audit extension (A15): who acted through which channel in which data plane. Nullable: existing writers are unchanged.
ALTER TABLE platform.audit_events
    ADD COLUMN IF NOT EXISTS via text CHECK (via IS NULL OR via IN ('ai', 'mcp')),
    ADD COLUMN IF NOT EXISTS tenant_id text CHECK (tenant_id IS NULL OR length(tenant_id) BETWEEN 1 AND 100),
    ADD COLUMN IF NOT EXISTS ai_proposal_id uuid;
CREATE INDEX IF NOT EXISTS audit_events_via_idx ON platform.audit_events (via, occurred_at DESC) WHERE via IS NOT NULL;
