-- Endpoints (F6 slice 2): the normalized management model - Management Artifacts, Assignments with
-- interval history, Filters, provider Observations with history, and Device group memberships
-- (docs/integrations/intune-assignment-intelligence.md, ADR-0020). Provider rows carry source and
-- freshness; nothing is deleted, vanished rows are tombstoned or closed.

-- A provider-reported configuration error or conflict is a finding of its own kind, distinct from the
-- data-quality kinds that Turaco derives itself.
ALTER TABLE endpoints.findings DROP CONSTRAINT IF EXISTS findings_kind_check;
-- Added NOT VALID and validated separately, so the table is not held under a long exclusive lock.
ALTER TABLE endpoints.findings ADD CONSTRAINT findings_kind_check
    CHECK (kind IN ('no_asset_match', 'serial_conflict', 'duplicate_device', 'unmatched_software', 'provider_reported_error')) NOT VALID;
ALTER TABLE endpoints.findings VALIDATE CONSTRAINT findings_kind_check;

CREATE TABLE IF NOT EXISTS endpoints.management_filters (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    provider text NOT NULL CHECK (provider ~ '^[a-z][a-z0-9_-]{1,39}$'),
    external_id text NOT NULL CHECK (external_id = btrim(external_id) AND length(external_id) BETWEEN 1 AND 200),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 256),
    platform text NOT NULL DEFAULT 'other' CHECK (platform IN ('windows', 'macos', 'ios', 'android', 'linux', 'other')),
    -- The provider's rule expression as reported; it is data to display, never executed.
    rule text NOT NULL DEFAULT '' CHECK (length(rule) <= 2000),
    revision text CHECK (revision IS NULL OR length(revision) <= 200),
    source text NOT NULL CHECK (source IN ('sync', 'import')),
    observed_at timestamptz NOT NULL,
    last_synced_at timestamptz NOT NULL,
    deleted_observed_at timestamptz,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT management_filters_provider_external_unique UNIQUE (provider, external_id)
);
CREATE INDEX IF NOT EXISTS management_filters_live_idx ON endpoints.management_filters (provider, last_synced_at) WHERE deleted_observed_at IS NULL;

CREATE TABLE IF NOT EXISTS endpoints.management_artifacts (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    provider text NOT NULL CHECK (provider ~ '^[a-z][a-z0-9_-]{1,39}$'),
    external_id text NOT NULL CHECK (external_id = btrim(external_id) AND length(external_id) BETWEEN 1 AND 200),
    kind text NOT NULL CHECK (kind IN ('application', 'configuration_profile', 'compliance_policy', 'endpoint_security_policy', 'script', 'remediation')),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 256),
    platform text NOT NULL DEFAULT 'other' CHECK (platform IN ('windows', 'macos', 'ios', 'android', 'linux', 'other')),
    -- Canonical software identity (by alias match); the provider object is not authoritative for it.
    software_product_id uuid REFERENCES endpoints.software_products(id),
    -- Provider revision, etag or hash where the provider reports one.
    revision text CHECK (revision IS NULL OR length(revision) <= 200),
    source text NOT NULL CHECK (source IN ('sync', 'import')),
    observed_at timestamptz NOT NULL,
    last_synced_at timestamptz NOT NULL,
    deleted_observed_at timestamptz,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT management_artifacts_provider_external_unique UNIQUE (provider, external_id)
);
CREATE INDEX IF NOT EXISTS management_artifacts_kind_idx ON endpoints.management_artifacts (kind, id);
CREATE INDEX IF NOT EXISTS management_artifacts_name_idx ON endpoints.management_artifacts (lower(name) text_pattern_ops);
CREATE INDEX IF NOT EXISTS management_artifacts_live_idx ON endpoints.management_artifacts (provider, last_synced_at) WHERE deleted_observed_at IS NULL;
CREATE INDEX IF NOT EXISTS management_artifacts_product_idx ON endpoints.management_artifacts (software_product_id) WHERE software_product_id IS NOT NULL;

-- Interval history: a changed assignment closes its row (valid_until) and opens a new one (valid_from
-- never before the previous row's valid_until, so intervals of one provider assignment do not overlap;
-- btree_gist is not installed, so no exclusion constraint backs this); an unchanged assignment only
-- refreshes freshness. The group is the Directory Group's provider external
-- id (resolved to the organization group lazily by readers; no foreign key across modules).
CREATE TABLE IF NOT EXISTS endpoints.management_assignments (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    artifact_id uuid NOT NULL REFERENCES endpoints.management_artifacts(id),
    -- Denormalized from the artifact (immutable there) so the reverse lookup below can filter by provider.
    provider text NOT NULL CHECK (provider ~ '^[a-z][a-z0-9_-]{1,39}$'),
    provider_assignment_id text NOT NULL CHECK (provider_assignment_id = btrim(provider_assignment_id) AND length(provider_assignment_id) BETWEEN 1 AND 200),
    target_kind text NOT NULL CHECK (target_kind IN ('group', 'all_devices', 'all_users')),
    target_group_external_id text CHECK (target_group_external_id IS NULL OR (target_group_external_id = btrim(target_group_external_id) AND length(target_group_external_id) BETWEEN 1 AND 200)),
    mode text NOT NULL CHECK (mode IN ('include', 'exclude')),
    intent text NOT NULL DEFAULT 'none' CHECK (intent IN ('required', 'available', 'uninstall', 'none')),
    filter_id uuid REFERENCES endpoints.management_filters(id),
    filter_mode text NOT NULL DEFAULT 'none' CHECK (filter_mode IN ('include', 'exclude', 'none')),
    source text NOT NULL CHECK (source IN ('sync', 'import')),
    observed_at timestamptz NOT NULL,
    last_synced_at timestamptz NOT NULL,
    valid_from timestamptz NOT NULL,
    valid_until timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT management_assignments_group_matches CHECK ((target_kind = 'group') = (target_group_external_id IS NOT NULL)),
    CONSTRAINT management_assignments_filter_matches CHECK ((filter_id IS NULL) = (filter_mode = 'none')),
    CONSTRAINT management_assignments_interval CHECK (valid_until IS NULL OR valid_until >= valid_from)
);
-- One current row per provider assignment of an artifact.
CREATE UNIQUE INDEX IF NOT EXISTS management_assignments_current_unique ON endpoints.management_assignments (artifact_id, provider_assignment_id) WHERE valid_until IS NULL;
CREATE INDEX IF NOT EXISTS management_assignments_artifact_idx ON endpoints.management_assignments (artifact_id, id);
-- Reverse lookup: which artifacts of a provider target a group.
CREATE INDEX IF NOT EXISTS management_assignments_group_idx ON endpoints.management_assignments (provider, target_group_external_id, artifact_id) WHERE valid_until IS NULL AND target_group_external_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS management_assignments_filter_idx ON endpoints.management_assignments (filter_id) WHERE filter_id IS NOT NULL;

-- Provider-reported result for an artifact on a device: one current row per pair; the normalized
-- state is a category, the raw status is kept for troubleshooting. Older provider data cannot
-- overwrite a newer state (observed_at guards state and raw status). A row the provider no longer reports in a complete
-- snapshot is retired (retired_at), not deleted; a later sighting revives it.
CREATE TABLE IF NOT EXISTS endpoints.management_observations (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    artifact_id uuid NOT NULL REFERENCES endpoints.management_artifacts(id) ON DELETE RESTRICT,
    device_id uuid NOT NULL REFERENCES endpoints.devices(id) ON DELETE RESTRICT,
    normalized_state text NOT NULL CHECK (normalized_state IN ('applied', 'pending', 'failed', 'conflict', 'not_applicable', 'unknown')),
    raw_status text NOT NULL DEFAULT '' CHECK (length(raw_status) <= 200),
    source text NOT NULL CHECK (source IN ('sync', 'import')),
    observed_at timestamptz NOT NULL,
    last_synced_at timestamptz NOT NULL,
    retired_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT management_observations_pair_unique UNIQUE (artifact_id, device_id)
);
CREATE INDEX IF NOT EXISTS management_observations_device_idx ON endpoints.management_observations (device_id, id);
CREATE INDEX IF NOT EXISTS management_observations_artifact_state_idx ON endpoints.management_observations (artifact_id, normalized_state) WHERE retired_at IS NULL;

-- Append-only: one row for the first sighting and one per change of the normalized state (the latest
-- raw status lives on the current row only). No foreign keys: the history outlives the rows it describes.
CREATE TABLE IF NOT EXISTS endpoints.management_observation_history (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    artifact_id uuid NOT NULL,
    device_id uuid NOT NULL,
    normalized_state text NOT NULL CHECK (normalized_state IN ('applied', 'pending', 'failed', 'conflict', 'not_applicable', 'unknown')),
    raw_status text NOT NULL CHECK (length(raw_status) <= 200),
    source text NOT NULL CHECK (source IN ('sync', 'import')),
    observed_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS management_observation_history_pair_idx ON endpoints.management_observation_history (artifact_id, device_id, id DESC);

DROP TRIGGER IF EXISTS management_observation_history_immutable ON endpoints.management_observation_history;
CREATE TRIGGER management_observation_history_immutable BEFORE UPDATE OR DELETE ON endpoints.management_observation_history
    FOR EACH ROW EXECUTE FUNCTION endpoints.forbid_history_change();
DROP TRIGGER IF EXISTS management_observation_history_no_truncate ON endpoints.management_observation_history;
CREATE TRIGGER management_observation_history_no_truncate BEFORE TRUNCATE ON endpoints.management_observation_history
    FOR EACH STATEMENT EXECUTE FUNCTION endpoints.forbid_history_change();

-- Membership of a Device in a provider group (the group's provider external id), as an interval like
-- Directory Group memberships. A membership that vanished from a complete snapshot is closed.
CREATE TABLE IF NOT EXISTS endpoints.device_group_memberships (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    device_id uuid NOT NULL REFERENCES endpoints.devices(id) ON DELETE RESTRICT,
    provider text NOT NULL CHECK (provider ~ '^[a-z][a-z0-9_-]{1,39}$'),
    group_external_id text NOT NULL CHECK (group_external_id = btrim(group_external_id) AND length(group_external_id) BETWEEN 1 AND 200),
    source text NOT NULL CHECK (source IN ('sync', 'import')),
    observed_from timestamptz NOT NULL,
    observed_until timestamptz,
    last_synced_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT device_group_memberships_interval CHECK (observed_until IS NULL OR observed_until >= observed_from)
);
CREATE UNIQUE INDEX IF NOT EXISTS device_group_memberships_current_unique ON endpoints.device_group_memberships (device_id, provider, group_external_id) WHERE observed_until IS NULL;
-- Reverse lookup: the current members of a group.
CREATE INDEX IF NOT EXISTS device_group_memberships_group_idx ON endpoints.device_group_memberships (provider, group_external_id, device_id) WHERE observed_until IS NULL;
CREATE INDEX IF NOT EXISTS device_group_memberships_stale_idx ON endpoints.device_group_memberships (provider, last_synced_at) WHERE observed_until IS NULL;

-- When the last synchronization run of a provider completed; the manual sync endpoint refuses a new run
-- within the cooldown (read under the provider run lock).
CREATE TABLE IF NOT EXISTS endpoints.provider_sync_state (
    provider text PRIMARY KEY CHECK (provider ~ '^[a-z][a-z0-9_-]{1,39}$'),
    last_completed_at timestamptz NOT NULL
);
