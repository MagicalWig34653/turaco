-- Security advisories and vulnerability findings (F8a, docs/product/f8-security-briefing-design.md).
-- Lifecycles: docs/domain/state-machines.md#security-advisory--finding. Devices and Software Products
-- are referenced by id only (the Endpoints module owns them; no foreign key across modules). Findings
-- are Turaco-derived from observed installations and carry their confidence; nothing here writes
-- endpoint data.
CREATE SCHEMA IF NOT EXISTS security;

CREATE SEQUENCE IF NOT EXISTS security.advisory_number_seq;
CREATE SEQUENCE IF NOT EXISTS security.finding_number_seq;

-- Human references ADV-000001 and VUL-000001; the width grows instead of truncating.
CREATE OR REPLACE FUNCTION security.next_advisory_reference() RETURNS text
LANGUAGE sql
AS $$
    SELECT 'ADV-' || CASE WHEN n < 1000000 THEN lpad(n::text, 6, '0') ELSE n::text END
    FROM (SELECT nextval('security.advisory_number_seq') AS n) AS s
$$;

CREATE OR REPLACE FUNCTION security.next_finding_reference() RETURNS text
LANGUAGE sql
AS $$
    SELECT 'VUL-' || CASE WHEN n < 1000000 THEN lpad(n::text, 6, '0') ELSE n::text END
    FROM (SELECT nextval('security.finding_number_seq') AS n) AS s
$$;

CREATE TABLE IF NOT EXISTS security.advisories (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    reference text NOT NULL DEFAULT security.next_advisory_reference() UNIQUE,
    -- Feed key ('manual' for advisories entered by hand) and the source's stable id (CVE or bulletin id).
    source text NOT NULL CHECK (source ~ '^[a-z][a-z0-9_-]{1,39}$'),
    external_id text CHECK (external_id IS NULL OR (external_id = btrim(external_id) AND length(external_id) BETWEEN 1 AND 200)),
    title text NOT NULL CHECK (title = btrim(title) AND length(title) BETWEEN 1 AND 300),
    summary text CHECK (summary IS NULL OR length(summary) BETWEEN 1 AND 4000),
    severity text NOT NULL CHECK (severity IN ('none', 'low', 'medium', 'high', 'critical')),
    published_at timestamptz,
    modified_at timestamptz,
    -- Shown as text only; https is enforced here as well as in the application.
    source_url text CHECK (source_url IS NULL OR (source_url ~ '^https://[^[:space:]]+$' AND length(source_url) <= 2000)),
    status text NOT NULL DEFAULT 'new' CHECK (status IN (
        'new', 'analyzing', 'applicable', 'not_applicable', 'remediation_planned', 'remediating', 'resolved', 'archived')),
    -- Reason code of the last exceptional transition (not applicable, re-analysis).
    status_reason text CHECK (status_reason IS NULL OR status_reason ~ '^[a-z][a-z_]{0,39}$'),
    -- Bumped on every criteria change; the matching job records the revision it matched.
    criteria_revision integer NOT NULL DEFAULT 1 CHECK (criteria_revision > 0),
    matched_revision integer CHECK (matched_revision IS NULL OR matched_revision > 0),
    matched_at timestamptz,
    -- The endpoint ingestion time the last match saw; a newer ingestion makes the periodic job re-match.
    matched_ingestion_at timestamptz,
    match_truncated boolean NOT NULL DEFAULT false,
    created_by uuid,
    applicable_at timestamptz,
    resolved_at timestamptz,
    archived_at timestamptz,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT advisories_source_external_unique UNIQUE (source, external_id),
    CONSTRAINT advisories_feed_has_external_id CHECK (source = 'manual' OR external_id IS NOT NULL),
    -- Per-status invariants.
    CONSTRAINT advisories_not_applicable_reason CHECK (status <> 'not_applicable' OR status_reason IS NOT NULL),
    CONSTRAINT advisories_applicable_at CHECK (status NOT IN ('applicable', 'remediation_planned', 'remediating') OR applicable_at IS NOT NULL),
    CONSTRAINT advisories_resolved_at CHECK ((status <> 'resolved' OR resolved_at IS NOT NULL) AND (status NOT IN ('new', 'analyzing', 'applicable', 'not_applicable', 'remediation_planned', 'remediating') OR resolved_at IS NULL)),
    CONSTRAINT advisories_archived_at CHECK ((archived_at IS NOT NULL) = (status = 'archived')),
    CONSTRAINT advisories_matched CHECK ((matched_revision IS NULL) = (matched_at IS NULL) AND (matched_revision IS NULL OR matched_revision <= criteria_revision))
);
CREATE INDEX IF NOT EXISTS advisories_status_idx ON security.advisories (status, id DESC);
CREATE INDEX IF NOT EXISTS advisories_severity_idx ON security.advisories (severity, id DESC);

-- Affected criteria; replaced as a whole when edited (the transitions and audit keep the history).
CREATE TABLE IF NOT EXISTS security.advisory_criteria (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    advisory_id uuid NOT NULL REFERENCES security.advisories(id) ON DELETE RESTRICT,
    position integer NOT NULL CHECK (position BETWEEN 0 AND 49),
    -- Endpoints Software Product by id, set by normalization or given explicitly.
    software_product_id uuid,
    product_name text CHECK (product_name IS NULL OR (product_name = btrim(product_name) AND length(product_name) BETWEEN 1 AND 200)),
    publisher text CHECK (publisher IS NULL OR (publisher = btrim(publisher) AND length(publisher) BETWEEN 1 AND 200)),
    os_platform text CHECK (os_platform IS NULL OR os_platform IN ('windows', 'macos', 'ios', 'android', 'linux', 'other')),
    normalization text NOT NULL CHECK (normalization IN ('matched', 'unmatched')),
    -- How the product was found: given explicitly, by exact product name or only through an alias.
    match_method text CHECK (match_method IS NULL OR match_method IN ('explicit', 'product', 'alias')),
    CONSTRAINT advisory_criteria_product CHECK (software_product_id IS NOT NULL OR product_name IS NOT NULL),
    CONSTRAINT advisory_criteria_matched CHECK ((normalization = 'matched') = (software_product_id IS NOT NULL)
        AND (match_method IS NULL) = (software_product_id IS NULL)),
    CONSTRAINT advisory_criteria_position_unique UNIQUE (advisory_id, position)
);
CREATE INDEX IF NOT EXISTS advisory_criteria_product_idx ON security.advisory_criteria (software_product_id) WHERE software_product_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS security.advisory_criteria_rules (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    criteria_id uuid NOT NULL REFERENCES security.advisory_criteria(id) ON DELETE CASCADE,
    position integer NOT NULL CHECK (position BETWEEN 0 AND 19),
    kind text NOT NULL CHECK (kind IN ('introduced', 'fixed', 'lt', 'le', 'eq')),
    version text NOT NULL CHECK (version = btrim(version) AND length(version) BETWEEN 1 AND 100),
    CONSTRAINT advisory_criteria_rules_position_unique UNIQUE (criteria_id, position)
);

CREATE TABLE IF NOT EXISTS security.vulnerability_findings (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    reference text NOT NULL DEFAULT security.next_finding_reference() UNIQUE,
    advisory_id uuid NOT NULL REFERENCES security.advisories(id) ON DELETE RESTRICT,
    -- Endpoints Device and Software Product by id.
    device_id uuid NOT NULL,
    software_product_id uuid NOT NULL,
    installed_version text NOT NULL DEFAULT '' CHECK (length(installed_version) <= 100),
    -- Turaco-derived: probable (normalized product, version inside the range) or potential (alias match
    -- or version not comparable). confirmed is never assigned automatically.
    confidence text NOT NULL CHECK (confidence IN ('probable', 'potential')),
    status text NOT NULL DEFAULT 'open' CHECK (status IN (
        'open', 'investigating', 'accepted', 'remediation_planned', 'remediating', 'remediated', 'false_positive', 'risk_accepted')),
    status_reason text CHECK (status_reason IS NULL OR status_reason ~ '^[a-z][a-z_]{0,39}$'),
    risk_accepted_by uuid,
    risk_accepted_at timestamptz,
    risk_review_by date,
    -- Observation times of the matching installation.
    first_seen_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    remediated_at timestamptz,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT vulnerability_findings_identity UNIQUE (advisory_id, device_id, software_product_id),
    CONSTRAINT vulnerability_findings_seen CHECK (first_seen_at <= last_seen_at),
    CONSTRAINT vulnerability_findings_remediated CHECK ((remediated_at IS NOT NULL) = (status = 'remediated')),
    CONSTRAINT vulnerability_findings_reason CHECK (status NOT IN ('false_positive', 'risk_accepted') OR status_reason IS NOT NULL),
    -- A risk acceptance names its actor, time and a review date at most twelve months later.
    CONSTRAINT vulnerability_findings_risk CHECK (
        (status = 'risk_accepted') = (risk_accepted_by IS NOT NULL)
        AND (risk_accepted_by IS NULL) = (risk_accepted_at IS NULL)
        AND (risk_accepted_by IS NULL) = (risk_review_by IS NULL)),
    CONSTRAINT vulnerability_findings_review_window CHECK (risk_review_by IS NULL OR (
        risk_review_by > (risk_accepted_at AT TIME ZONE 'UTC')::date
        AND risk_review_by <= ((risk_accepted_at AT TIME ZONE 'UTC')::date + interval '12 months')::date))
);
CREATE INDEX IF NOT EXISTS vulnerability_findings_advisory_idx ON security.vulnerability_findings (advisory_id, status, id DESC);
CREATE INDEX IF NOT EXISTS vulnerability_findings_status_idx ON security.vulnerability_findings (status, id DESC);
CREATE INDEX IF NOT EXISTS vulnerability_findings_device_idx ON security.vulnerability_findings (device_id);
CREATE INDEX IF NOT EXISTS vulnerability_findings_review_idx ON security.vulnerability_findings (risk_review_by) WHERE status = 'risk_accepted';

-- Append-only histories of state transitions (what happened, by whom, why); reason codes only.
CREATE TABLE IF NOT EXISTS security.advisory_transitions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    advisory_id uuid NOT NULL REFERENCES security.advisories(id) ON DELETE RESTRICT,
    from_status text CHECK (from_status IS NULL OR from_status IN (
        'new', 'analyzing', 'applicable', 'not_applicable', 'remediation_planned', 'remediating', 'resolved', 'archived')),
    to_status text NOT NULL CHECK (to_status IN (
        'new', 'analyzing', 'applicable', 'not_applicable', 'remediation_planned', 'remediating', 'resolved', 'archived')),
    operation text NOT NULL CHECK (operation ~ '^[a-z][a-z_]{0,39}$'),
    reason text CHECK (reason IS NULL OR reason ~ '^[a-z][a-z_]{0,39}$'),
    actor_user_id uuid,
    actor_system text,
    correlation_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((actor_user_id IS NULL) <> (actor_system IS NULL))
);
CREATE INDEX IF NOT EXISTS advisory_transitions_advisory_idx ON security.advisory_transitions (advisory_id, id);

CREATE TABLE IF NOT EXISTS security.finding_transitions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    finding_id uuid NOT NULL REFERENCES security.vulnerability_findings(id) ON DELETE RESTRICT,
    from_status text CHECK (from_status IS NULL OR from_status IN (
        'open', 'investigating', 'accepted', 'remediation_planned', 'remediating', 'remediated', 'false_positive', 'risk_accepted')),
    to_status text NOT NULL CHECK (to_status IN (
        'open', 'investigating', 'accepted', 'remediation_planned', 'remediating', 'remediated', 'false_positive', 'risk_accepted')),
    operation text NOT NULL CHECK (operation ~ '^[a-z][a-z_]{0,39}$'),
    reason text CHECK (reason IS NULL OR reason ~ '^[a-z][a-z_]{0,39}$'),
    actor_user_id uuid,
    actor_system text,
    correlation_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((actor_user_id IS NULL) <> (actor_system IS NULL))
);
CREATE INDEX IF NOT EXISTS finding_transitions_finding_idx ON security.finding_transitions (finding_id, id);

CREATE OR REPLACE FUNCTION security.forbid_history_change() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'security history rows are append-only (%.%)', TG_TABLE_SCHEMA, TG_TABLE_NAME USING ERRCODE = 'restrict_violation';
END
$$;

DROP TRIGGER IF EXISTS advisory_transitions_append_only ON security.advisory_transitions;
CREATE TRIGGER advisory_transitions_append_only BEFORE UPDATE OR DELETE ON security.advisory_transitions
    FOR EACH ROW EXECUTE FUNCTION security.forbid_history_change();
DROP TRIGGER IF EXISTS advisory_transitions_no_truncate ON security.advisory_transitions;
CREATE TRIGGER advisory_transitions_no_truncate BEFORE TRUNCATE ON security.advisory_transitions
    FOR EACH STATEMENT EXECUTE FUNCTION security.forbid_history_change();
DROP TRIGGER IF EXISTS finding_transitions_append_only ON security.finding_transitions;
CREATE TRIGGER finding_transitions_append_only BEFORE UPDATE OR DELETE ON security.finding_transitions
    FOR EACH ROW EXECUTE FUNCTION security.forbid_history_change();
DROP TRIGGER IF EXISTS finding_transitions_no_truncate ON security.finding_transitions;
CREATE TRIGGER finding_transitions_no_truncate BEFORE TRUNCATE ON security.finding_transitions
    FOR EACH STATEMENT EXECUTE FUNCTION security.forbid_history_change();
-- Findings are never deleted; their status says what happened.
DROP TRIGGER IF EXISTS vulnerability_findings_no_delete ON security.vulnerability_findings;
CREATE TRIGGER vulnerability_findings_no_delete BEFORE DELETE ON security.vulnerability_findings
    FOR EACH ROW EXECUTE FUNCTION security.forbid_history_change();

-- Endpoints read path of the Security matching (endpoints/public InstallationsByProducts): current
-- installations of a Software Product in id order.
CREATE INDEX IF NOT EXISTS software_installations_product_live_idx
    ON endpoints.software_installations (software_product_id, id) WHERE deleted_observed_at IS NULL;
