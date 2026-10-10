-- F14 slice A-C: worker heartbeats (worker health), setup checklist state and the audit retention function.

CREATE TABLE IF NOT EXISTS platform.worker_heartbeats (
    instance_id text PRIMARY KEY CHECK (length(instance_id) BETWEEN 1 AND 200),
    version text NOT NULL DEFAULT '' CHECK (length(version) <= 100),
    started_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS worker_heartbeats_seen_idx ON platform.worker_heartbeats (last_seen_at DESC);

-- One row per setup checklist item that an administrator skipped or confirmed; everything else is derived.
CREATE TABLE IF NOT EXISTS platform.setup_items (
    key text PRIMARY KEY CHECK (key ~ '^[a-z][a-z0-9_]{0,63}$'),
    state text NOT NULL CHECK (state IN ('skipped', 'confirmed')),
    reason text NOT NULL DEFAULT '' CHECK (length(reason) <= 500),
    updated_by uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    version integer NOT NULL DEFAULT 1 CHECK (version >= 1)
);

-- Audit retention. API and worker share one database role, so the 365-day floor is enforced here, not by a grant:
-- whatever cutoff is passed is clamped to now() - 365 days. Deletes in one batch of at most batch_size rows (oldest
-- first) and returns what was done so the caller can audit it; the caller loops until deleted = 0.
CREATE OR REPLACE FUNCTION platform.purge_audit_before(cutoff timestamptz, batch_size integer DEFAULT 5000)
RETURNS TABLE (deleted bigint, effective_cutoff timestamptz, id_hash text)
LANGUAGE plpgsql
AS $$
DECLARE
    floor_cutoff timestamptz := now() - interval '365 days';
    eff timestamptz := LEAST(cutoff, now() - interval '365 days');
    ids uuid[];
BEGIN
    IF batch_size IS NULL OR batch_size < 1 OR batch_size > 50000 THEN
        RAISE EXCEPTION 'audit purge: batch_size must be between 1 and 50000';
    END IF;
    SELECT array_agg(id ORDER BY occurred_at, id) INTO ids FROM (
        SELECT id, occurred_at FROM platform.audit_events
        WHERE occurred_at < eff
        ORDER BY occurred_at, id
        LIMIT batch_size
    ) old;
    IF ids IS NULL THEN
        RETURN QUERY SELECT 0::bigint, eff, ''::text;
        RETURN;
    END IF;
    DELETE FROM platform.audit_events WHERE id = ANY (ids) AND occurred_at < floor_cutoff;
    RETURN QUERY SELECT array_length(ids, 1)::bigint, eff,
        encode(sha256(convert_to(array_to_string(ids, ','), 'UTF8')), 'hex');
END;
$$;
