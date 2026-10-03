-- Services and the shared Relationship mechanism (F7b,
-- docs/product/f7-infrastructure-change-design.md). Relationships are platform
-- data: modules register allowed (source_type, type, target_type) triples in
-- Go and link records by id without foreign keys because other modules own
-- the records. Services own their lifecycle in schema services.
CREATE SCHEMA IF NOT EXISTS services;

CREATE TABLE IF NOT EXISTS platform.relationships (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    source_type text NOT NULL CHECK (source_type ~ '^[a-z][a-z_]{0,39}$'),
    source_id uuid NOT NULL,
    type text NOT NULL CHECK (type ~ '^[A-Z][A-Z_]{0,39}$'),
    target_type text NOT NULL CHECK (target_type ~ '^[a-z][a-z_]{0,39}$'),
    target_id uuid NOT NULL,
    -- declared by a person, derived by Turaco from its own data, observed from an integration.
    confidence text NOT NULL CHECK (confidence IN ('declared', 'derived', 'observed')),
    valid_from timestamptz NOT NULL DEFAULT now(),
    valid_until timestamptz,
    -- Reason code for ending a relationship; set exactly when it is ended.
    end_reason text CHECK (end_reason IS NULL OR end_reason ~ '^[a-z][a-z_]{0,39}$'),
    created_by uuid,
    ended_by uuid,
    -- Who or what recorded it, e.g. a module name or an integration key.
    source text CHECK (source IS NULL OR (source = btrim(source) AND length(source) BETWEEN 1 AND 50)),
    CHECK ((valid_until IS NULL) = (end_reason IS NULL)),
    CHECK (valid_until IS NULL OR valid_until >= valid_from),
    CHECK (NOT (source_type = target_type AND source_id = target_id))
);
-- One current relationship per (source, type, target).
CREATE UNIQUE INDEX IF NOT EXISTS relationships_current_unique
    ON platform.relationships (source_type, source_id, type, target_type, target_id) WHERE valid_until IS NULL;
-- Outgoing and incoming reads and traversals only look at current rows.
CREATE INDEX IF NOT EXISTS relationships_current_source ON platform.relationships (source_type, source_id, id) WHERE valid_until IS NULL;
CREATE INDEX IF NOT EXISTS relationships_current_target ON platform.relationships (target_type, target_id, id) WHERE valid_until IS NULL;

CREATE SEQUENCE IF NOT EXISTS services.service_number_seq;

-- Human reference SVC-000001; the width grows instead of truncating.
CREATE OR REPLACE FUNCTION services.next_reference() RETURNS text
LANGUAGE sql
AS $$
    SELECT 'SVC-' || CASE WHEN n < 1000000 THEN lpad(n::text, 6, '0') ELSE n::text END
    FROM (SELECT nextval('services.service_number_seq') AS n) AS s
$$;

CREATE TABLE IF NOT EXISTS services.services (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    reference text NOT NULL DEFAULT services.next_reference() UNIQUE,
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 100),
    description text CHECK (description IS NULL OR length(description) BETWEEN 1 AND 2000),
    -- Organization records by id (the Organization module owns them).
    owner_user_id uuid,
    owner_team_id uuid,
    support_team_id uuid,
    criticality text NOT NULL CHECK (criticality IN ('low', 'medium', 'high', 'critical')),
    status text NOT NULL CHECK (status IN ('operational', 'degraded', 'outage', 'planned', 'retired')),
    -- Reason code of the last status change or of the retirement.
    status_reason text CHECK (status_reason IS NULL OR status_reason ~ '^[a-z][a-z_]{0,39}$'),
    retired_at timestamptz,
    CHECK ((status = 'retired') = (retired_at IS NOT NULL)),
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
-- Names are unique among live Services; a retired Service keeps its tombstone.
CREATE UNIQUE INDEX IF NOT EXISTS services_name_live ON services.services (lower(name)) WHERE status <> 'retired';
CREATE INDEX IF NOT EXISTS services_owner_user ON services.services (owner_user_id, id) WHERE owner_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS services_owner_team ON services.services (owner_team_id, id) WHERE owner_team_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS services_support_team ON services.services (support_team_id, id) WHERE support_team_id IS NOT NULL;
