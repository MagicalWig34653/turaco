-- Remote Access (F10 R-A, docs/product/f10-remote-access-design.md, ADR-0026).
-- Remote Access Sessions are attended only; Turaco-authorized facts, consent and provider-observed facts are
-- separate columns. Launch links are never stored: a launch handle keeps only the SHA-256 of a one-time token.
CREATE SCHEMA IF NOT EXISTS remoteaccess;

CREATE SEQUENCE IF NOT EXISTS remoteaccess.session_number_seq;

CREATE OR REPLACE FUNCTION remoteaccess.next_reference() RETURNS text
LANGUAGE sql
AS $$
    SELECT 'RAS-' || CASE WHEN n < 1000000 THEN lpad(n::text, 6, '0') ELSE n::text END
    FROM (SELECT nextval('remoteaccess.session_number_seq') AS n) AS s
$$;

CREATE OR REPLACE FUNCTION remoteaccess.forbid_history_change() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'remote access history rows are append-only (%.%)', TG_TABLE_SCHEMA, TG_TABLE_NAME USING ERRCODE = 'restrict_violation';
END
$$;

-- Device <-> provider peer mappings. A mapping is closed, never deleted, when it is replaced or removed.
-- Matched by an explicit, audited manual mapping or a provider id, never by hostname.
CREATE TABLE IF NOT EXISTS remoteaccess.peer_mappings (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    -- The Device of the Endpoints module by id (no foreign key across modules).
    device_id uuid NOT NULL,
    provider text NOT NULL CHECK (provider ~ '^[a-z][a-z0-9_-]{1,39}$'),
    peer_id text NOT NULL CHECK (peer_id = btrim(peer_id) AND length(peer_id) BETWEEN 1 AND 100 AND peer_id ~ '^[A-Za-z0-9_.@-]+$'),
    source text NOT NULL CHECK (source IN ('manual', 'provider')),
    reason text CHECK (reason IS NULL OR reason ~ '^[a-z][a-z_]{0,39}$'),
    mapped_by uuid,
    mapped_at timestamptz NOT NULL DEFAULT now(),
    closed_at timestamptz,
    closed_by uuid,
    close_reason text CHECK (close_reason IS NULL OR close_reason ~ '^[a-z][a-z_]{0,39}$'),
    CONSTRAINT peer_mappings_closed CHECK ((closed_at IS NULL) = (close_reason IS NULL)),
    CONSTRAINT peer_mappings_closed_by CHECK (closed_by IS NULL OR closed_at IS NOT NULL),
    CONSTRAINT peer_mappings_manual_has_user CHECK (source <> 'manual' OR mapped_by IS NOT NULL)
);
-- One active mapping per Device and provider, and one active Device per peer.
CREATE UNIQUE INDEX IF NOT EXISTS peer_mappings_device_active ON remoteaccess.peer_mappings (device_id, provider) WHERE closed_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS peer_mappings_peer_active ON remoteaccess.peer_mappings (provider, peer_id) WHERE closed_at IS NULL;
CREATE INDEX IF NOT EXISTS peer_mappings_device_idx ON remoteaccess.peer_mappings (device_id, id DESC);
DROP TRIGGER IF EXISTS peer_mappings_no_delete ON remoteaccess.peer_mappings;
CREATE TRIGGER peer_mappings_no_delete BEFORE DELETE ON remoteaccess.peer_mappings
    FOR EACH ROW EXECUTE FUNCTION remoteaccess.forbid_history_change();

CREATE TABLE IF NOT EXISTS remoteaccess.sessions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    reference text NOT NULL DEFAULT remoteaccess.next_reference() UNIQUE,
    device_id uuid NOT NULL,
    ticket_id uuid NOT NULL,
    provider text NOT NULL CHECK (provider ~ '^[a-z][a-z0-9_-]{1,39}$'),
    -- The peer id at request time (the mapping can change later); never a secret.
    peer_id text NOT NULL CHECK (peer_id ~ '^[A-Za-z0-9_.@-]{1,100}$'),
    mode text NOT NULL CHECK (mode = 'attended'),
    status text NOT NULL CHECK (status IN ('requested', 'pending_approval', 'authorized', 'launched', 'closed',
        'rejected', 'cancelled', 'expired', 'failed')),
    -- Reason code of the last exceptional or closing transition.
    status_reason text CHECK (status_reason IS NULL OR status_reason ~ '^[a-z][a-z_]{0,39}$'),
    initiated_by uuid NOT NULL,
    approval_id uuid,
    -- Users that can never approve (initiator, Ticket requester).
    excluded_user_ids uuid[] NOT NULL DEFAULT '{}',
    consent text NOT NULL DEFAULT 'unknown' CHECK (consent IN ('granted', 'declined', 'not_required', 'unknown')),
    consent_recorded_by uuid,
    consent_recorded_at timestamptz,
    -- Why the Ticket's affected User is not the Device's holder.
    mismatch_reason text CHECK (mismatch_reason IS NULL OR mismatch_reason IN ('holder_changed', 'shared_device', 'on_behalf')),
    launched_at timestamptz,
    closed_at timestamptz,
    expires_at timestamptz NOT NULL,
    -- Plain text note (safetext validated, never audited).
    note text CHECK (note IS NULL OR length(note) BETWEEN 1 AND 500),
    -- Provider-observed facts, with source and observation time. Null means unknown, never inferred.
    observed_connected_at timestamptz,
    observed_ended_at timestamptz,
    observed_source text CHECK (observed_source IS NULL OR observed_source ~ '^[a-z][a-z0-9_.-]{0,59}$'),
    observed_at timestamptz,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT sessions_pending_has_approval CHECK (status <> 'pending_approval' OR approval_id IS NOT NULL),
    CONSTRAINT sessions_launched_at CHECK (status <> 'launched' OR launched_at IS NOT NULL),
    CONSTRAINT sessions_launched_only_late CHECK (launched_at IS NULL OR status IN ('launched', 'closed')),
    CONSTRAINT sessions_closed_at CHECK ((closed_at IS NOT NULL) = (status IN ('closed', 'rejected', 'cancelled', 'expired', 'failed'))),
    CONSTRAINT sessions_terminal_reason CHECK (status NOT IN ('closed', 'rejected', 'cancelled', 'expired', 'failed') OR status_reason IS NOT NULL),
    CONSTRAINT sessions_closed_was_launched CHECK (status <> 'closed' OR launched_at IS NOT NULL),
    CONSTRAINT sessions_consent_recorded CHECK ((consent = 'unknown') = (consent_recorded_at IS NULL)
        AND (consent_recorded_by IS NULL OR consent_recorded_at IS NOT NULL)),
    CONSTRAINT sessions_declined_closed CHECK (consent <> 'declined' OR status = 'closed'),
    CONSTRAINT sessions_consent_after_launch CHECK (consent = 'unknown' OR launched_at IS NOT NULL),
    CONSTRAINT sessions_observed_source CHECK ((observed_source IS NULL) = (observed_at IS NULL)
        AND (observed_connected_at IS NULL AND observed_ended_at IS NULL OR observed_source IS NOT NULL)),
    CONSTRAINT sessions_time_order CHECK (closed_at IS NULL OR launched_at IS NULL OR launched_at <= closed_at)
);
-- One open session per Device and provider.
CREATE UNIQUE INDEX IF NOT EXISTS sessions_open_per_device ON remoteaccess.sessions (device_id, provider)
    WHERE status IN ('requested', 'pending_approval', 'authorized', 'launched');
CREATE INDEX IF NOT EXISTS sessions_status_idx ON remoteaccess.sessions (status, id DESC);
CREATE INDEX IF NOT EXISTS sessions_initiator_idx ON remoteaccess.sessions (initiated_by, id DESC);
CREATE INDEX IF NOT EXISTS sessions_device_idx ON remoteaccess.sessions (device_id, id DESC);
CREATE INDEX IF NOT EXISTS sessions_ticket_idx ON remoteaccess.sessions (ticket_id, id DESC);
CREATE INDEX IF NOT EXISTS sessions_expiry_idx ON remoteaccess.sessions (expires_at) WHERE status IN ('pending_approval', 'authorized', 'launched');
DROP TRIGGER IF EXISTS sessions_no_delete ON remoteaccess.sessions;
CREATE TRIGGER sessions_no_delete BEFORE DELETE ON remoteaccess.sessions
    FOR EACH ROW EXECUTE FUNCTION remoteaccess.forbid_history_change();

-- Append-only history of state transitions; reason codes only.
CREATE TABLE IF NOT EXISTS remoteaccess.session_transitions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    session_id uuid NOT NULL REFERENCES remoteaccess.sessions(id) ON DELETE RESTRICT,
    from_status text CHECK (from_status IS NULL OR from_status IN ('requested', 'pending_approval', 'authorized', 'launched', 'closed',
        'rejected', 'cancelled', 'expired', 'failed')),
    to_status text NOT NULL CHECK (to_status IN ('requested', 'pending_approval', 'authorized', 'launched', 'closed',
        'rejected', 'cancelled', 'expired', 'failed')),
    operation text NOT NULL CHECK (operation ~ '^[a-z][a-z_]{0,39}$'),
    reason text CHECK (reason IS NULL OR reason ~ '^[a-z][a-z_]{0,39}$'),
    actor_user_id uuid,
    actor_system text,
    correlation_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((actor_user_id IS NULL) <> (actor_system IS NULL))
);
CREATE INDEX IF NOT EXISTS session_transitions_session_idx ON remoteaccess.session_transitions (session_id, id);
DROP TRIGGER IF EXISTS session_transitions_append_only ON remoteaccess.session_transitions;
CREATE TRIGGER session_transitions_append_only BEFORE UPDATE OR DELETE ON remoteaccess.session_transitions
    FOR EACH ROW EXECUTE FUNCTION remoteaccess.forbid_history_change();
DROP TRIGGER IF EXISTS session_transitions_no_truncate ON remoteaccess.session_transitions;
CREATE TRIGGER session_transitions_no_truncate BEFORE TRUNCATE ON remoteaccess.session_transitions
    FOR EACH STATEMENT EXECUTE FUNCTION remoteaccess.forbid_history_change();

-- One-time launch handles: only the hash of the token is stored, never the token or the launch link.
CREATE TABLE IF NOT EXISTS remoteaccess.launch_handles (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    session_id uuid NOT NULL REFERENCES remoteaccess.sessions(id) ON DELETE RESTRICT,
    user_id uuid NOT NULL,
    token_hash bytea NOT NULL CHECK (length(token_hash) = 32),
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT launch_handles_token_unique UNIQUE (token_hash),
    CONSTRAINT launch_handles_used CHECK (used_at IS NULL OR used_at <= expires_at)
);
CREATE INDEX IF NOT EXISTS launch_handles_session_idx ON remoteaccess.launch_handles (session_id, id DESC);
DROP TRIGGER IF EXISTS launch_handles_no_delete ON remoteaccess.launch_handles;
CREATE TRIGGER launch_handles_no_delete BEFORE DELETE ON remoteaccess.launch_handles
    FOR EACH ROW EXECUTE FUNCTION remoteaccess.forbid_history_change();
