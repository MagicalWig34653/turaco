-- Live advisory feeds (NVD, CISA KEV): Known Exploited enrichment of advisories and per-feed sync state.
-- docs/integrations/advisory-feeds.md. The three KEV columns are written only by the KEV enrichment of
-- the advisory sync job, never by manual edits or the generic import.
ALTER TABLE security.advisories
    ADD COLUMN IF NOT EXISTS known_exploited boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS known_exploited_added_at date,
    ADD COLUMN IF NOT EXISTS kev_due_date date;

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
    updated_at timestamptz NOT NULL DEFAULT now()
);
