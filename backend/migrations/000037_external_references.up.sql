-- External references (F5 part 2): the mapping of internal records to records in external systems
-- (Autotask first) with observable sync state, and the inbound events already processed.
CREATE TABLE IF NOT EXISTS platform.external_references (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    system text NOT NULL CHECK (system ~ '^[a-z][a-z0-9_-]{1,39}$'),
    entity_type text NOT NULL CHECK (entity_type ~ '^[a-z][a-z_]{1,39}$'),
    entity_id uuid NOT NULL,
    -- Null until the external system has created its record.
    external_id text CHECK (external_id IS NULL OR (external_id = btrim(external_id) AND length(external_id) BETWEEN 1 AND 200)),
    sync_state text NOT NULL DEFAULT 'pending' CHECK (sync_state IN ('pending', 'synced', 'failed')),
    last_error text CHECK (last_error IS NULL OR length(last_error) <= 500),
    last_synced_at timestamptz,
    -- When the external system last changed its record (from the last inbound event).
    external_updated_at timestamptz,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT external_references_entity_unique UNIQUE (system, entity_type, entity_id),
    CONSTRAINT external_references_synced_has_id CHECK (sync_state <> 'synced' OR external_id IS NOT NULL)
);
CREATE UNIQUE INDEX IF NOT EXISTS external_references_external_unique ON platform.external_references (system, entity_type, external_id) WHERE external_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS external_references_state_idx ON platform.external_references (system, sync_state) WHERE sync_state <> 'synced';

CREATE TABLE IF NOT EXISTS platform.external_events (
    system text NOT NULL,
    event_id text NOT NULL CHECK (length(event_id) BETWEEN 1 AND 200),
    received_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (system, event_id)
);
