-- Endpoints (F9 slice G1): Software Approval Status of Software Products, Software Versions with their version
-- approval bound to the installer SHA-256 and the install/detection definition, and Software Packages of a
-- Software Management Provider with their status observations (docs/product/f9-software-lifecycle-design.md,
-- ADR-0027). Decisions and observations are append-only; an approved binding is never mutated: a changed hash,
-- installer URL, publisher, install command or detection rule is a new Software Version.

-- Software Approval Status of a product: candidate -> approved -> deprecated -> retired; blocked (with a reason
-- code) from any state, left only to candidate. version is the optimistic concurrency counter of the decision.
ALTER TABLE endpoints.software_products
    ADD COLUMN IF NOT EXISTS approval_status text NOT NULL DEFAULT 'candidate'
        CHECK (approval_status IN ('candidate', 'approved', 'deprecated', 'retired', 'blocked')),
    ADD COLUMN IF NOT EXISTS approval_reason text CHECK (approval_reason IS NULL OR approval_reason ~ '^[a-z][a-z_]{0,39}$'),
    ADD COLUMN IF NOT EXISTS version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE endpoints.software_products DROP CONSTRAINT IF EXISTS software_products_blocked_reason;
ALTER TABLE endpoints.software_products ADD CONSTRAINT software_products_blocked_reason
    CHECK ((approval_status = 'blocked') = (approval_reason IS NOT NULL));
CREATE INDEX IF NOT EXISTS software_products_approval_idx ON endpoints.software_products (approval_status, id);

CREATE TABLE IF NOT EXISTS endpoints.software_product_transitions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    software_product_id uuid NOT NULL REFERENCES endpoints.software_products(id) ON DELETE RESTRICT,
    from_status text NOT NULL CHECK (from_status IN ('candidate', 'approved', 'deprecated', 'retired', 'blocked')),
    to_status text NOT NULL CHECK (to_status IN ('candidate', 'approved', 'deprecated', 'retired', 'blocked')),
    operation text NOT NULL CHECK (operation ~ '^[a-z][a-z_]{0,39}$'),
    reason text CHECK (reason IS NULL OR reason ~ '^[a-z][a-z_]{0,39}$'),
    actor_user_id uuid,
    actor_system text,
    correlation_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((actor_user_id IS NULL) <> (actor_system IS NULL))
);
CREATE INDEX IF NOT EXISTS software_product_transitions_product_idx ON endpoints.software_product_transitions (software_product_id, id);

-- A Software Version is one installable binding: product, version, installer (hash, https URL, publisher),
-- install command and detection rule. The command and rule are kept as bounded text for display and as a
-- SHA-256 that the database recomputes; binding_sha256 (computed by the application over every bound value)
-- identifies the binding, so registering the same binding again returns the existing row.
-- approval_status: registered -> pending -> approved | rejected; approved -> revoked.
CREATE TABLE IF NOT EXISTS endpoints.software_versions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    software_product_id uuid NOT NULL REFERENCES endpoints.software_products(id) ON DELETE RESTRICT,
    product_version text NOT NULL CHECK (product_version = btrim(product_version) AND length(product_version) BETWEEN 1 AND 100),
    installer_sha256 text NOT NULL CHECK (installer_sha256 ~ '^[0-9a-f]{64}$'),
    installer_url text NOT NULL CHECK (installer_url LIKE 'https://%' AND length(installer_url) BETWEEN 9 AND 2000),
    publisher text CHECK (publisher IS NULL OR (publisher = btrim(publisher) AND length(publisher) BETWEEN 1 AND 200)),
    install_command text NOT NULL CHECK (length(install_command) BETWEEN 1 AND 2000),
    install_command_sha256 text NOT NULL CHECK (install_command_sha256 = encode(sha256(convert_to(install_command, 'UTF8')), 'hex')),
    detection_rule text NOT NULL CHECK (length(detection_rule) BETWEEN 1 AND 4000),
    detection_rule_sha256 text NOT NULL CHECK (detection_rule_sha256 = encode(sha256(convert_to(detection_rule, 'UTF8')), 'hex')),
    binding_sha256 text NOT NULL CHECK (binding_sha256 ~ '^[0-9a-f]{64}$'),
    registered_by uuid NOT NULL,
    approval_status text NOT NULL DEFAULT 'registered' CHECK (approval_status IN ('registered', 'pending', 'approved', 'rejected', 'revoked')),
    approval_reason text CHECK (approval_reason IS NULL OR approval_reason ~ '^[a-z][a-z_]{0,39}$'),
    approval_requested_by uuid,
    approval_requested_at timestamptz,
    approval_decided_by uuid,
    approval_decided_at timestamptz,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT software_versions_binding_unique UNIQUE (software_product_id, binding_sha256),
    CONSTRAINT software_versions_requested_matches CHECK ((approval_requested_by IS NULL) = (approval_requested_at IS NULL)),
    CONSTRAINT software_versions_decided_matches CHECK ((approval_decided_by IS NULL) = (approval_decided_at IS NULL)),
    CONSTRAINT software_versions_status_fields CHECK (CASE approval_status
        WHEN 'registered' THEN approval_requested_by IS NULL AND approval_decided_by IS NULL AND approval_reason IS NULL
        WHEN 'pending' THEN approval_requested_by IS NOT NULL AND approval_decided_by IS NULL AND approval_reason IS NULL
        WHEN 'approved' THEN approval_requested_by IS NOT NULL AND approval_decided_by IS NOT NULL AND approval_reason IS NULL
        ELSE approval_requested_by IS NOT NULL AND approval_decided_by IS NOT NULL AND approval_reason IS NOT NULL END)
);
CREATE INDEX IF NOT EXISTS software_versions_product_idx ON endpoints.software_versions (software_product_id, id);
CREATE INDEX IF NOT EXISTS software_versions_status_idx ON endpoints.software_versions (approval_status, id);

-- Append-only approval decisions of a version, each recording the binding it was made for.
CREATE TABLE IF NOT EXISTS endpoints.software_version_approvals (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    software_version_id uuid NOT NULL REFERENCES endpoints.software_versions(id) ON DELETE RESTRICT,
    from_status text NOT NULL CHECK (from_status IN ('registered', 'pending', 'approved', 'rejected', 'revoked')),
    to_status text NOT NULL CHECK (to_status IN ('registered', 'pending', 'approved', 'rejected', 'revoked')),
    operation text NOT NULL CHECK (operation ~ '^[a-z][a-z_]{0,39}$'),
    reason text CHECK (reason IS NULL OR reason ~ '^[a-z][a-z_]{0,39}$'),
    installer_sha256 text NOT NULL CHECK (installer_sha256 ~ '^[0-9a-f]{64}$'),
    binding_sha256 text NOT NULL CHECK (binding_sha256 ~ '^[0-9a-f]{64}$'),
    actor_user_id uuid,
    actor_system text,
    correlation_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((actor_user_id IS NULL) <> (actor_system IS NULL))
);
CREATE INDEX IF NOT EXISTS software_version_approvals_version_idx ON endpoints.software_version_approvals (software_version_id, id);

-- A Software Package of a Software Management Provider for one Software Version. installer_sha256 is the hash
-- the provider reports (compared with the approved hash); the Management Artifact is linked once the
-- management sync has ingested the published object.
CREATE TABLE IF NOT EXISTS endpoints.software_packages (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    provider text NOT NULL CHECK (provider ~ '^[a-z][a-z0-9_-]{1,39}$'),
    provider_package_id text CHECK (provider_package_id IS NULL OR (provider_package_id = btrim(provider_package_id) AND length(provider_package_id) BETWEEN 1 AND 200)),
    software_version_id uuid NOT NULL REFERENCES endpoints.software_versions(id) ON DELETE RESTRICT,
    status text NOT NULL CHECK (status IN ('requested', 'building', 'packaged', 'published', 'failed')),
    installer_sha256 text CHECK (installer_sha256 IS NULL OR installer_sha256 ~ '^[0-9a-f]{64}$'),
    management_provider text CHECK (management_provider IS NULL OR management_provider ~ '^[a-z][a-z0-9_-]{1,39}$'),
    management_artifact_external_id text CHECK (management_artifact_external_id IS NULL OR (management_artifact_external_id = btrim(management_artifact_external_id) AND length(management_artifact_external_id) BETWEEN 1 AND 200)),
    management_artifact_id uuid REFERENCES endpoints.management_artifacts(id),
    source text NOT NULL CHECK (source IN ('operation', 'sync')),
    observed_at timestamptz,
    last_synced_at timestamptz,
    requested_by uuid NOT NULL,
    publish_requested_by uuid,
    publish_requested_at timestamptz,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT software_packages_version_unique UNIQUE (provider, software_version_id),
    CONSTRAINT software_packages_external_unique UNIQUE (provider, provider_package_id),
    CONSTRAINT software_packages_reported CHECK (status = 'requested' OR (provider_package_id IS NOT NULL AND observed_at IS NOT NULL)),
    CONSTRAINT software_packages_artifact_matches CHECK (management_artifact_id IS NULL OR management_artifact_external_id IS NOT NULL),
    CONSTRAINT software_packages_publish_matches CHECK ((publish_requested_by IS NULL) = (publish_requested_at IS NULL)),
    CONSTRAINT software_packages_publish_target CHECK (publish_requested_by IS NULL OR management_provider IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS software_packages_status_idx ON endpoints.software_packages (status, id);
CREATE INDEX IF NOT EXISTS software_packages_unlinked_idx ON endpoints.software_packages (management_provider, management_artifact_external_id)
    WHERE management_artifact_external_id IS NOT NULL AND management_artifact_id IS NULL;

-- Append-only: one row per meaningful change of a package's reported state.
CREATE TABLE IF NOT EXISTS endpoints.software_package_observations (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    software_package_id uuid NOT NULL REFERENCES endpoints.software_packages(id) ON DELETE RESTRICT,
    status text NOT NULL CHECK (status IN ('requested', 'building', 'packaged', 'published', 'failed')),
    provider_package_id text,
    installer_sha256 text,
    management_artifact_external_id text,
    source text NOT NULL CHECK (source IN ('operation', 'sync')),
    observed_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS software_package_observations_package_idx ON endpoints.software_package_observations (software_package_id, id);

CREATE OR REPLACE FUNCTION endpoints.forbid_software_history_change() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'software approval and package history rows are append-only (%.%)', TG_TABLE_SCHEMA, TG_TABLE_NAME USING ERRCODE = 'restrict_violation';
END
$$;

DROP TRIGGER IF EXISTS software_product_transitions_append_only ON endpoints.software_product_transitions;
CREATE TRIGGER software_product_transitions_append_only BEFORE UPDATE OR DELETE ON endpoints.software_product_transitions
    FOR EACH ROW EXECUTE FUNCTION endpoints.forbid_software_history_change();
DROP TRIGGER IF EXISTS software_product_transitions_no_truncate ON endpoints.software_product_transitions;
CREATE TRIGGER software_product_transitions_no_truncate BEFORE TRUNCATE ON endpoints.software_product_transitions
    FOR EACH STATEMENT EXECUTE FUNCTION endpoints.forbid_software_history_change();
DROP TRIGGER IF EXISTS software_version_approvals_append_only ON endpoints.software_version_approvals;
CREATE TRIGGER software_version_approvals_append_only BEFORE UPDATE OR DELETE ON endpoints.software_version_approvals
    FOR EACH ROW EXECUTE FUNCTION endpoints.forbid_software_history_change();
DROP TRIGGER IF EXISTS software_version_approvals_no_truncate ON endpoints.software_version_approvals;
CREATE TRIGGER software_version_approvals_no_truncate BEFORE TRUNCATE ON endpoints.software_version_approvals
    FOR EACH STATEMENT EXECUTE FUNCTION endpoints.forbid_software_history_change();
DROP TRIGGER IF EXISTS software_package_observations_append_only ON endpoints.software_package_observations;
CREATE TRIGGER software_package_observations_append_only BEFORE UPDATE OR DELETE ON endpoints.software_package_observations
    FOR EACH ROW EXECUTE FUNCTION endpoints.forbid_software_history_change();
DROP TRIGGER IF EXISTS software_package_observations_no_truncate ON endpoints.software_package_observations;
CREATE TRIGGER software_package_observations_no_truncate BEFORE TRUNCATE ON endpoints.software_package_observations
    FOR EACH STATEMENT EXECUTE FUNCTION endpoints.forbid_software_history_change();
-- Versions are never deleted; their approval status says what happened.
DROP TRIGGER IF EXISTS software_versions_no_delete ON endpoints.software_versions;
CREATE TRIGGER software_versions_no_delete BEFORE DELETE ON endpoints.software_versions
    FOR EACH ROW EXECUTE FUNCTION endpoints.forbid_software_history_change();
DROP TRIGGER IF EXISTS software_versions_no_truncate ON endpoints.software_versions;
CREATE TRIGGER software_versions_no_truncate BEFORE TRUNCATE ON endpoints.software_versions
    FOR EACH STATEMENT EXECUTE FUNCTION endpoints.forbid_software_history_change();

-- The binding of a version is immutable: only its approval state changes.
CREATE OR REPLACE FUNCTION endpoints.forbid_software_binding_change() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.software_product_id IS DISTINCT FROM OLD.software_product_id
        OR NEW.product_version IS DISTINCT FROM OLD.product_version
        OR NEW.installer_sha256 IS DISTINCT FROM OLD.installer_sha256
        OR NEW.installer_url IS DISTINCT FROM OLD.installer_url
        OR NEW.publisher IS DISTINCT FROM OLD.publisher
        OR NEW.install_command IS DISTINCT FROM OLD.install_command
        OR NEW.install_command_sha256 IS DISTINCT FROM OLD.install_command_sha256
        OR NEW.detection_rule IS DISTINCT FROM OLD.detection_rule
        OR NEW.detection_rule_sha256 IS DISTINCT FROM OLD.detection_rule_sha256
        OR NEW.binding_sha256 IS DISTINCT FROM OLD.binding_sha256
        OR NEW.registered_by IS DISTINCT FROM OLD.registered_by THEN
        RAISE EXCEPTION 'a software version binding is immutable; register a new version' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END
$$;
DROP TRIGGER IF EXISTS software_versions_binding_immutable ON endpoints.software_versions;
CREATE TRIGGER software_versions_binding_immutable BEFORE UPDATE ON endpoints.software_versions
    FOR EACH ROW EXECUTE FUNCTION endpoints.forbid_software_binding_change();

-- The constraint swaps below take ACCESS EXCLUSIVE locks on findings until the migration commits; they run last
-- and fail after 5s of waiting instead of queueing behind a long transaction.
SET LOCAL statement_timeout = '30s';
SET LOCAL lock_timeout = '5s';

-- package_hash_mismatch is an Endpoint Finding about a Software Package (not a Device): the installer hash the
-- provider reports differs from the approved hash. A finding has exactly one subject.
ALTER TABLE endpoints.findings ALTER COLUMN device_id DROP NOT NULL;
ALTER TABLE endpoints.findings ADD COLUMN IF NOT EXISTS software_package_id uuid REFERENCES endpoints.software_packages(id) ON DELETE CASCADE;
ALTER TABLE endpoints.findings DROP CONSTRAINT IF EXISTS findings_kind_check;
ALTER TABLE endpoints.findings ADD CONSTRAINT findings_kind_check
    CHECK (kind IN ('no_asset_match', 'serial_conflict', 'duplicate_device', 'unmatched_software', 'provider_reported_error', 'assignment_ineffective', 'package_hash_mismatch')) NOT VALID;
ALTER TABLE endpoints.findings VALIDATE CONSTRAINT findings_kind_check;
ALTER TABLE endpoints.findings DROP CONSTRAINT IF EXISTS findings_subject_check;
ALTER TABLE endpoints.findings ADD CONSTRAINT findings_subject_check
    CHECK (num_nonnulls(device_id, software_package_id) = 1 AND (kind = 'package_hash_mismatch') = (software_package_id IS NOT NULL)) NOT VALID;
ALTER TABLE endpoints.findings VALIDATE CONSTRAINT findings_subject_check;
CREATE UNIQUE INDEX IF NOT EXISTS findings_package_one_open ON endpoints.findings (kind, software_package_id)
    WHERE status = 'open' AND software_package_id IS NOT NULL;
