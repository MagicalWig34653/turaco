-- Endpoints (F6 slice 1): provider-observed Devices, their observation history, installed
-- software with normalization, and data-quality findings (docs/product/f6-endpoint-intelligence-design.md).
-- Provider rows carry source and freshness; nothing is deleted, missing devices are tombstoned.
CREATE SCHEMA IF NOT EXISTS endpoints;

CREATE TABLE IF NOT EXISTS endpoints.devices (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    provider text NOT NULL CHECK (provider ~ '^[a-z][a-z0-9_-]{1,39}$'),
    external_id text NOT NULL CHECK (external_id = btrim(external_id) AND length(external_id) BETWEEN 1 AND 200),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 256),
    serial_number text CHECK (serial_number IS NULL OR (serial_number = btrim(serial_number) AND length(serial_number) BETWEEN 1 AND 100)),
    -- The canonical Asset by id (no foreign key across modules). Set by a serial-number match or by hand.
    asset_id uuid,
    asset_link_source text CHECK (asset_link_source IN ('serial', 'manual')),
    -- A manual unlink stops automatic serial matching until someone links again.
    auto_link_blocked boolean NOT NULL DEFAULT false,
    os_platform text NOT NULL DEFAULT 'other' CHECK (os_platform IN ('windows', 'macos', 'ios', 'android', 'linux', 'other')),
    os_version text CHECK (os_version IS NULL OR length(os_version) <= 100),
    manufacturer text CHECK (manufacturer IS NULL OR length(manufacturer) <= 100),
    model text CHECK (model IS NULL OR length(model) <= 100),
    ownership text NOT NULL DEFAULT 'unknown' CHECK (ownership IN ('corporate', 'personal', 'unknown')),
    compliance_state text NOT NULL DEFAULT 'unknown' CHECK (compliance_state IN ('compliant', 'noncompliant', 'in_grace_period', 'unknown')),
    last_checkin_at timestamptz,
    -- Where the values came from and how fresh they are.
    source text NOT NULL CHECK (source IN ('sync', 'import')),
    observed_at timestamptz NOT NULL,
    last_synced_at timestamptz NOT NULL,
    -- Set when a later complete snapshot no longer contained the device.
    deleted_observed_at timestamptz,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT devices_provider_external_unique UNIQUE (provider, external_id),
    CONSTRAINT devices_link_matches CHECK ((asset_id IS NULL) = (asset_link_source IS NULL))
);
CREATE INDEX IF NOT EXISTS devices_serial_idx ON endpoints.devices (lower(serial_number)) WHERE serial_number IS NOT NULL;
CREATE INDEX IF NOT EXISTS devices_asset_idx ON endpoints.devices (asset_id) WHERE asset_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS devices_name_idx ON endpoints.devices (lower(name) text_pattern_ops);

-- Append-only: one row per meaningful change of the normalized values (and one for the first sighting).
-- No foreign key: the history outlives anything that happens to the device row.
CREATE TABLE IF NOT EXISTS endpoints.device_observation_history (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    device_id uuid NOT NULL,
    name text NOT NULL,
    serial_number text,
    os_platform text NOT NULL,
    os_version text,
    manufacturer text,
    model text,
    ownership text NOT NULL,
    compliance_state text NOT NULL,
    source text NOT NULL,
    observed_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS device_observation_history_device_idx ON endpoints.device_observation_history (device_id, id DESC);

CREATE OR REPLACE FUNCTION endpoints.forbid_history_change() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'device observation history rows are immutable (%.%)', TG_TABLE_SCHEMA, TG_TABLE_NAME USING ERRCODE = 'restrict_violation';
END
$$;

DROP TRIGGER IF EXISTS device_observation_history_immutable ON endpoints.device_observation_history;
CREATE TRIGGER device_observation_history_immutable BEFORE UPDATE OR DELETE ON endpoints.device_observation_history
    FOR EACH ROW EXECUTE FUNCTION endpoints.forbid_history_change();

-- Normalized software. Aliases map observed names (lower case, single spaces) to a product; a
-- product's own name is registered as an alias too.
CREATE TABLE IF NOT EXISTS endpoints.software_products (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 200),
    publisher text CHECK (publisher IS NULL OR (publisher = btrim(publisher) AND length(publisher) BETWEEN 1 AND 200)),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS software_products_name_unique ON endpoints.software_products (lower(name), lower(coalesce(publisher, '')));

CREATE TABLE IF NOT EXISTS endpoints.software_aliases (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    alias text NOT NULL CHECK (alias = lower(alias) AND alias = btrim(alias) AND length(alias) BETWEEN 1 AND 300),
    software_product_id uuid NOT NULL REFERENCES endpoints.software_products(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT software_aliases_alias_unique UNIQUE (alias)
);
CREATE INDEX IF NOT EXISTS software_aliases_product_idx ON endpoints.software_aliases (software_product_id);

-- Observed installations. The raw name and version are kept as reported; the product is set when an
-- alias matches and stays null otherwise.
CREATE TABLE IF NOT EXISTS endpoints.software_installations (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    device_id uuid NOT NULL REFERENCES endpoints.devices(id) ON DELETE CASCADE,
    software_product_id uuid REFERENCES endpoints.software_products(id),
    raw_name text NOT NULL CHECK (raw_name = btrim(raw_name) AND length(raw_name) BETWEEN 1 AND 300),
    raw_version text NOT NULL DEFAULT '' CHECK (length(raw_version) <= 100),
    raw_publisher text CHECK (raw_publisher IS NULL OR length(raw_publisher) <= 200),
    observed_at timestamptz NOT NULL,
    last_synced_at timestamptz NOT NULL,
    deleted_observed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS software_installations_unique ON endpoints.software_installations (device_id, lower(raw_name), raw_version);
CREATE INDEX IF NOT EXISTS software_installations_product_idx ON endpoints.software_installations (software_product_id) WHERE software_product_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS software_installations_unmatched_idx ON endpoints.software_installations (lower(raw_name)) WHERE software_product_id IS NULL AND deleted_observed_at IS NULL;

-- Data-quality findings about a device. At most one open finding per kind and device.
CREATE TABLE IF NOT EXISTS endpoints.findings (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    kind text NOT NULL CHECK (kind IN ('no_asset_match', 'serial_conflict', 'duplicate_device', 'unmatched_software')),
    device_id uuid NOT NULL REFERENCES endpoints.devices(id) ON DELETE CASCADE,
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved')),
    detail jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(detail) = 'object' AND octet_length(detail::text) <= 2000),
    raised_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    CONSTRAINT findings_resolved_matches CHECK ((status = 'resolved') = (resolved_at IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS findings_one_open ON endpoints.findings (kind, device_id) WHERE status = 'open';
CREATE INDEX IF NOT EXISTS findings_device_idx ON endpoints.findings (device_id, id DESC);
CREATE INDEX IF NOT EXISTS findings_status_idx ON endpoints.findings (status, kind, id);
