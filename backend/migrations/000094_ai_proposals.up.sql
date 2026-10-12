-- Turaco AI, F12 slice A-C: AI Proposals (docs/product/f12-turaco-ai-design.md, ADR-0029 decision 4) and the
-- idempotency key the first write tool (tickets.draft_internal_note) needs from the Service Desk.

-- An AI Proposal is short-lived conversation state, not a business record. A write tool call creates one; the
-- creating User confirms it (hash-bound, single-use) and Turaco executes it as that User through the module's own
-- operation. Owned by platform/ai.
CREATE TABLE IF NOT EXISTS ai.proposals (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id text NOT NULL CHECK (length(tenant_id) BETWEEN 1 AND 100),
    user_id uuid NOT NULL,
    -- The conversation that proposed it; NULL once the conversation session was deleted.
    session_ref uuid REFERENCES ai.sessions (id) ON DELETE SET NULL,
    tool_name text NOT NULL CHECK (tool_name ~ '^[a-z][a-z0-9]*\.[a-z][a-z0-9_]*$' AND length(tool_name) <= 64),
    -- The exact parameters shown for confirmation; removed by the purge (parameters_hash and ids stay for audit).
    parameters jsonb,
    parameters_hash text NOT NULL CHECK (parameters_hash ~ '^[0-9a-f]{64}$'),
    target_type text NOT NULL CHECK (target_type ~ '^[a-z][a-z_]{1,30}$'),
    target_id uuid NOT NULL,
    target_version integer NOT NULL,
    -- Display label of the target (for example the Ticket reference and title) for the creating User only; purged
    -- together with the parameters.
    target_label text CHECK (length(target_label) <= 300),
    state text NOT NULL DEFAULT 'proposed'
        CHECK (state IN ('proposed', 'confirmed', 'executed', 'failed', 'dismissed', 'expired')),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    confirmed_at timestamptz,
    ended_at timestamptz,
    -- Durable claim of a confirmed proposal: whoever holds claim_token until lease_expires_at executes it.
    claim_token uuid,
    lease_expires_at timestamptz,
    attempt smallint NOT NULL DEFAULT 0 CHECK (attempt BETWEEN 0 AND 3),
    outcome_code text CHECK (outcome_code ~ '^[a-z][a-z_.]{1,60}$'),
    result_type text CHECK (result_type ~ '^[a-z][a-z_]{1,30}$'),
    result_id uuid,
    version integer NOT NULL DEFAULT 1,
    CONSTRAINT proposals_confirmed_has_claim CHECK (state <> 'confirmed' OR (claim_token IS NOT NULL AND lease_expires_at IS NOT NULL AND confirmed_at IS NOT NULL)),
    CONSTRAINT proposals_terminal_complete CHECK ((state IN ('proposed', 'confirmed')) = (ended_at IS NULL)),
    CONSTRAINT proposals_outcome_terminal CHECK ((outcome_code IS NULL) OR state IN ('executed', 'failed', 'dismissed', 'expired')),
    CONSTRAINT proposals_open_has_parameters CHECK (state NOT IN ('proposed', 'confirmed') OR parameters IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS proposals_user_state_idx ON ai.proposals (user_id, state, created_at DESC);
CREATE INDEX IF NOT EXISTS proposals_open_expiry_idx ON ai.proposals (expires_at) WHERE state = 'proposed';
CREATE INDEX IF NOT EXISTS proposals_lease_idx ON ai.proposals (lease_expires_at) WHERE state = 'confirmed';
CREATE INDEX IF NOT EXISTS proposals_purge_idx ON ai.proposals (ended_at) WHERE parameters IS NOT NULL AND ended_at IS NOT NULL;

-- Service Desk: an idempotency key on comments. A retried execution of the same AI Proposal returns the comment the
-- first execution created instead of adding a second one. Ordinary comments carry none.
ALTER TABLE servicedesk.ticket_comments ADD COLUMN IF NOT EXISTS idempotency_key uuid;
CREATE UNIQUE INDEX IF NOT EXISTS ticket_comments_idempotency_idx ON servicedesk.ticket_comments (idempotency_key)
    WHERE idempotency_key IS NOT NULL;
