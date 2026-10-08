-- Workforce Presence (F11 P-A, docs/product/f11-workforce-presence-design.md, ADR-0028).
-- Operational availability only: there is no absence reason, note, category or free text anywhere in this
-- schema (a test asserts the column list). Operational Availability and Team Coverage are derived at read
-- time; nothing per person is aggregated or materialized. Past data is removed by presence.purge_entries,
-- the only way to delete entries.
CREATE SCHEMA IF NOT EXISTS presence;

CREATE OR REPLACE FUNCTION presence.forbid_truncate() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'presence rows are only deleted by presence.purge_entries (%.%)', TG_TABLE_SCHEMA, TG_TABLE_NAME USING ERRCODE = 'restrict_violation';
END
$$;

-- Single settings row: opt-in, kill switch, retention and the recorded data protection dates (dates only).
CREATE TABLE IF NOT EXISTS presence.settings (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    enabled boolean NOT NULL DEFAULT false,
    dpia_recorded_on date,
    council_confirmed_on date,
    retention_days smallint NOT NULL DEFAULT 30 CHECK (retention_days BETWEEN 1 AND 30),
    external_sources_enabled boolean NOT NULL DEFAULT false,
    -- When the module was switched off; all entries are deleted retention_days after it (or at once on request).
    disabled_at timestamptz,
    updated_by uuid,
    updated_at timestamptz NOT NULL DEFAULT now(),
    version integer NOT NULL DEFAULT 1,
    CONSTRAINT settings_external_needs_dpia CHECK (NOT external_sources_enabled OR dpia_recorded_on IS NOT NULL)
);
INSERT INTO presence.settings (singleton) VALUES (true) ON CONFLICT (singleton) DO NOTHING;

CREATE TABLE IF NOT EXISTS presence.entries (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    -- The subject User of the Organization module by id (no foreign key across modules).
    user_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('work_location', 'unavailable')),
    location_type text CHECK (location_type IN ('location', 'remote', 'travelling')),
    -- Organization Location by id; set only when location_type = 'location'.
    location_id uuid,
    -- For recurring entries the first occurrence.
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    all_day boolean NOT NULL DEFAULT false,
    -- Validated recurrence (frequency, interval, weekday, dayOfMonth, timeOfDay, timezone, startsOn, endsOn).
    recurrence jsonb,
    source text NOT NULL DEFAULT 'manual' CHECK (source ~ '^[a-z][a-z0-9_-]{1,39}$'),
    source_ref text CHECK (source_ref IS NULL OR (source_ref = btrim(source_ref) AND length(source_ref) BETWEEN 1 AND 200)),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'cancelled')),
    -- Interval history of externally observed entries (observed_to NULL while the source still reports it).
    observed_from timestamptz,
    observed_to timestamptz,
    observed_at timestamptz,
    -- What the owner allows others to see: availability only, or the entry's detail for presence.view_entries.
    visibility text NOT NULL DEFAULT 'availability' CHECK (visibility IN ('availability', 'detail')),
    -- The instant after which the entry has no effect (last occurrence end, or cancellation); retention counts from here.
    ended_at timestamptz NOT NULL,
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    cancelled_at timestamptz,
    version integer NOT NULL DEFAULT 1,
    CONSTRAINT entries_kind_location CHECK (
        (kind = 'unavailable' AND location_type IS NULL AND location_id IS NULL)
        OR (kind = 'work_location' AND location_type IS NOT NULL AND ((location_type = 'location') = (location_id IS NOT NULL)))),
    CONSTRAINT entries_span CHECK (ends_at > starts_at AND ends_at - starts_at <= interval '31 days'),
    CONSTRAINT entries_recurrence_shape CHECK (recurrence IS NULL OR (jsonb_typeof(recurrence) = 'object' AND recurrence ? 'endsOn')),
    CONSTRAINT entries_cancelled CHECK ((status = 'cancelled') = (cancelled_at IS NOT NULL)),
    CONSTRAINT entries_manual CHECK (source <> 'manual' OR (source_ref IS NULL AND observed_from IS NULL AND observed_to IS NULL
        AND observed_at IS NULL AND created_by IS NOT NULL)),
    -- External rows are source-owned: they never transition (no status), and carry their observation interval.
    CONSTRAINT entries_external CHECK (source = 'manual' OR (source_ref IS NOT NULL AND observed_from IS NOT NULL
        AND observed_at IS NOT NULL AND status = 'active' AND recurrence IS NULL AND (observed_to IS NULL OR observed_to >= observed_from)))
);
CREATE UNIQUE INDEX IF NOT EXISTS entries_source_ref_uq ON presence.entries (source, source_ref, observed_from) WHERE source_ref IS NOT NULL;
CREATE INDEX IF NOT EXISTS entries_user_idx ON presence.entries (user_id, starts_at);
CREATE INDEX IF NOT EXISTS entries_window_idx ON presence.entries (starts_at, ended_at) WHERE status = 'active' AND observed_to IS NULL;
CREATE INDEX IF NOT EXISTS entries_ended_idx ON presence.entries (ended_at);

-- Identity columns never change.
CREATE OR REPLACE FUNCTION presence.entries_frozen_identity() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF (OLD.id, OLD.user_id, OLD.source, OLD.source_ref, OLD.created_by, OLD.created_at)
       IS DISTINCT FROM (NEW.id, NEW.user_id, NEW.source, NEW.source_ref, NEW.created_by, NEW.created_at) THEN
        RAISE EXCEPTION 'the identity of a presence entry cannot change' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END
$$;
DROP TRIGGER IF EXISTS entries_frozen_identity ON presence.entries;
CREATE TRIGGER entries_frozen_identity BEFORE UPDATE ON presence.entries
    FOR EACH ROW EXECUTE FUNCTION presence.entries_frozen_identity();

-- Rows are deleted only inside presence.purge_entries (which the application calls from the audited purge).
CREATE OR REPLACE FUNCTION presence.entries_guard_delete() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF coalesce(current_setting('presence.purging', true), 'off') <> 'on' THEN
        RAISE EXCEPTION 'presence entries are only deleted by presence.purge_entries' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN OLD;
END
$$;
DROP TRIGGER IF EXISTS entries_guard_delete ON presence.entries;
CREATE TRIGGER entries_guard_delete BEFORE DELETE ON presence.entries
    FOR EACH ROW EXECUTE FUNCTION presence.entries_guard_delete();
DROP TRIGGER IF EXISTS entries_no_truncate ON presence.entries;
CREATE TRIGGER entries_no_truncate BEFORE TRUNCATE ON presence.entries
    FOR EACH STATEMENT EXECUTE FUNCTION presence.forbid_truncate();

-- Deletes the entries that ended before cutoff, or every entry; returns the number of deleted rows.
CREATE OR REPLACE FUNCTION presence.purge_entries(cutoff timestamptz, everything boolean) RETURNS bigint
LANGUAGE plpgsql
AS $$
DECLARE
    n bigint;
BEGIN
    PERFORM set_config('presence.purging', 'on', true);
    DELETE FROM presence.entries WHERE everything OR ended_at < cutoff;
    GET DIAGNOSTICS n = ROW_COUNT;
    PERFORM set_config('presence.purging', 'off', true);
    RETURN n;
END
$$;

-- Per-Team minimum coverage (Team by id; no foreign key across modules).
CREATE TABLE IF NOT EXISTS presence.team_coverage_minimums (
    team_id uuid PRIMARY KEY,
    minimum smallint NOT NULL CHECK (minimum >= 0),
    onsite_minimum smallint CHECK (onsite_minimum IS NULL OR onsite_minimum >= 0),
    -- The Team's coverage Location (Organization Location by id); on-site members are counted there.
    location_id uuid,
    updated_by uuid,
    updated_at timestamptz NOT NULL DEFAULT now(),
    version integer NOT NULL DEFAULT 1
);
