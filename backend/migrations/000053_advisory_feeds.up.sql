-- Live advisory feeds (NVD, CISA KEV): Known Exploited enrichment of advisories and per-feed sync state.
-- docs/integrations/advisory-feeds.md. The three KEV columns are written only by the KEV enrichment of
-- the advisory sync job, never by manual edits or the generic import.
ALTER TABLE security.advisories
    ADD COLUMN IF NOT EXISTS known_exploited boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS known_exploited_added_at date,
    ADD COLUMN IF NOT EXISTS kev_due_date date;

-- criteria_incomplete: the feed adapter had to leave affected software out (too many products or rules,
-- unusual versions); criteria_skipped counts what was left out. criteria_changed_upstream: the feed reported
-- different criteria for an advisory whose criteria are not changed by the feed any more (analyst decision
-- already taken); cleared when an analyst edits the criteria.
ALTER TABLE security.advisories
    ADD COLUMN IF NOT EXISTS criteria_incomplete boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS criteria_skipped integer NOT NULL DEFAULT 0 CHECK (criteria_skipped >= 0 AND criteria_skipped <= 100000),
    ADD COLUMN IF NOT EXISTS criteria_changed_upstream boolean NOT NULL DEFAULT false;

ALTER TABLE security.advisories
    ADD CONSTRAINT advisories_kev_consistent CHECK (known_exploited OR (known_exploited_added_at IS NULL AND kev_due_date IS NULL));

-- Known exploited advisories sort first in the feed and the overview.
CREATE INDEX IF NOT EXISTS advisories_known_exploited_idx ON security.advisories (id DESC) WHERE known_exploited;

-- One row per feed source: the incremental cursor (NVD: the end of the last completely read window as
-- RFC 3339), the conditional-fetch ETag (CISA KEV), the last success and a constant error code of the last
-- failed run (never an error text: those can contain URLs or keys). locked_until is a lease so runs of
-- the same source never overlap; it expires on its own when a worker dies.
CREATE TABLE IF NOT EXISTS security.feed_state (
    source text PRIMARY KEY CHECK (source ~ '^[a-z][a-z0-9_-]{1,39}$'),
    cursor text CHECK (cursor IS NULL OR length(cursor) <= 100),
    etag text CHECK (etag IS NULL OR length(etag) <= 200),
    last_success_at timestamptz,
    last_attempt_at timestamptz,
    last_error text CHECK (last_error IS NULL OR last_error ~ '^[a-z][a-z_]{0,39}$'),
    locked_until timestamptz,
    -- lease_owner is the token of the run that holds the lease; only that run may finish the source.
    lease_owner text CHECK (lease_owner IS NULL OR length(lease_owner) <= 64),
    -- kev_miss: CVE id -> RFC 3339 time of the last failed by-id fetch (KEV ids NVD could not provide);
    -- kev_count: size of the last accepted KEV catalog (plausibility check).
    kev_miss jsonb NOT NULL DEFAULT '{}'::jsonb,
    kev_count integer NOT NULL DEFAULT 0 CHECK (kev_count >= 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);
