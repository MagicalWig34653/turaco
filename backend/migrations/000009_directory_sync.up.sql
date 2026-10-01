-- F1 slice 3: LDAP/AD directory synchronization.
-- See docs/integrations/ldap-ad-sync-design.md.

-- External identities: directory freshness and change detection.
ALTER TABLE organization.external_identities
    ADD COLUMN IF NOT EXISTS deleted_observed_at timestamptz,
    ADD COLUMN IF NOT EXISTS attributes_hash text;
ALTER TABLE organization.external_identities
    ADD CONSTRAINT external_identities_keys_not_empty CHECK (provider_key <> '' AND external_subject <> '');
CREATE INDEX IF NOT EXISTS external_identities_user_idx ON organization.external_identities(user_id);

-- Who last set User.status. Directory sync reactivates only users it deactivated.
ALTER TABLE organization.users
    ADD COLUMN IF NOT EXISTS status_source text NOT NULL DEFAULT 'platform';
ALTER TABLE organization.users
    ADD CONSTRAINT users_status_source_valid CHECK (status_source IN ('platform', 'directory'));

-- Directory groups: constraints deferred from migration 000006.
ALTER TABLE organization.directory_groups
    ADD CONSTRAINT directory_groups_keys_not_empty CHECK (provider_key <> '' AND external_id <> ''),
    ADD CONSTRAINT directory_groups_deleted_after_first CHECK (deleted_observed_at IS NULL OR deleted_observed_at >= first_observed_at);

-- Membership becomes interval history (decision D3): one row per continuous
-- observed interval; observed_until IS NULL means currently observed.
ALTER TABLE organization.directory_group_memberships
    ADD COLUMN IF NOT EXISTS id uuid NOT NULL DEFAULT uuidv7(),
    ADD COLUMN IF NOT EXISTS observed_from timestamptz,
    ADD COLUMN IF NOT EXISTS observed_until timestamptz;
UPDATE organization.directory_group_memberships SET observed_from = last_observed_at WHERE observed_from IS NULL;
ALTER TABLE organization.directory_group_memberships
    ALTER COLUMN observed_from SET NOT NULL,
    DROP CONSTRAINT directory_group_memberships_pkey,
    ADD PRIMARY KEY (id),
    ADD CONSTRAINT directory_group_memberships_interval CHECK (
        last_observed_at >= observed_from
        AND (observed_until IS NULL OR observed_until >= last_observed_at));
CREATE UNIQUE INDEX IF NOT EXISTS directory_group_memberships_current_unique
    ON organization.directory_group_memberships(group_id, user_id) WHERE observed_until IS NULL;
-- Replaced by the partial unique index above plus a history lookup index.
CREATE INDEX IF NOT EXISTS directory_group_memberships_history_idx
    ON organization.directory_group_memberships(group_id, user_id, observed_from DESC);

-- Direct group-in-group edges as observed; transitive membership is not expanded.
CREATE TABLE IF NOT EXISTS organization.directory_group_nesting (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    parent_group_id uuid NOT NULL REFERENCES organization.directory_groups(id),
    child_group_id uuid NOT NULL REFERENCES organization.directory_groups(id),
    observed_from timestamptz NOT NULL,
    last_observed_at timestamptz NOT NULL,
    observed_until timestamptz,
    CHECK (parent_group_id <> child_group_id),
    CHECK (last_observed_at >= observed_from AND (observed_until IS NULL OR observed_until >= last_observed_at))
);
CREATE UNIQUE INDEX IF NOT EXISTS directory_group_nesting_current_unique
    ON organization.directory_group_nesting(parent_group_id, child_group_id) WHERE observed_until IS NULL;
CREATE INDEX IF NOT EXISTS directory_group_nesting_child_idx ON organization.directory_group_nesting(child_group_id);

-- One execution of directory synchronization for one provider.
CREATE TABLE IF NOT EXISTS organization.directory_sync_runs (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    provider_key text NOT NULL CHECK (provider_key <> ''),
    trigger text NOT NULL CHECK (trigger IN ('scheduled', 'manual')),
    started_at timestamptz NOT NULL,
    observed_at timestamptz,
    finished_at timestamptz,
    outcome text NOT NULL DEFAULT 'running'
        CHECK (outcome IN ('running', 'succeeded', 'failed', 'aborted_safeguard')),
    counts jsonb NOT NULL DEFAULT '{}'::jsonb,
    conflicts jsonb NOT NULL DEFAULT '[]'::jsonb,
    conflict_count integer NOT NULL DEFAULT 0 CHECK (conflict_count >= 0),
    error text,
    CHECK ((outcome = 'running') = (finished_at IS NULL)),
    CHECK (finished_at IS NULL OR finished_at >= started_at)
);
-- At most one running run per provider.
CREATE UNIQUE INDEX IF NOT EXISTS directory_sync_runs_running_unique
    ON organization.directory_sync_runs(provider_key) WHERE outcome = 'running';
CREATE INDEX IF NOT EXISTS directory_sync_runs_provider_idx
    ON organization.directory_sync_runs(provider_key, started_at DESC);
