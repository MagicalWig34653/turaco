-- Workbench Views (ADR-0033, F13 slice Q-B, docs/product/f13-workbench-views-design.md). Owned by platform/views.
--
-- A Saved View stores a resource key, a Filter AST (platform/query), sort and visible columns, never results and
-- never authorization: results are always evaluated as the viewer. Sharing is explicit (user, Team, role or
-- everyone) and resolved at read time; a Pin places a View in the viewer's sidebar; a Pin Rule shows a View to the
-- members of a Team or role (it grants no access).
--
-- Part 2 of this migration answers the Q-A query review: trigram indexes that serve the substring matches
-- (contains, ends_with, search) of the Ticket, Task and Device catalogs. pg_trgm is a contrib extension of the
-- PostgreSQL distribution (design decision 10), trusted since PostgreSQL 13, so a database owner can create it.
-- Only title-like fields are indexed; descriptions are deliberately not (multi-kilobyte text, rarely searched):
-- the catalogs do not offer substring operators on them.

CREATE SCHEMA IF NOT EXISTS views;

-- ---------------------------------------------------------------- Saved Views
CREATE TABLE IF NOT EXISTS views.saved_views (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    -- Catalog key of the queried resource (tickets, devices, tasks); registered catalogs are checked in code.
    resource text NOT NULL CHECK (resource ~ '^[a-z][a-z0-9_]{0,39}$'),
    name text NOT NULL CHECK (name = btrim(name) AND char_length(name) BETWEEN 1 AND 80),
    description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 500),
    -- The owning User of the Organization module by id (no foreign key across modules).
    owner_user_id uuid NOT NULL,
    -- {"filter": <Filter AST>, "columns": [<field key>...]}; canonical form, bounded size.
    definition jsonb NOT NULL CHECK (jsonb_typeof(definition) = 'object' AND octet_length(definition::text) <= 16384),
    definition_hash text NOT NULL CHECK (definition_hash ~ '^[0-9a-f]{64}$'),
    schema_version smallint NOT NULL DEFAULT 1 CHECK (schema_version >= 1),
    -- 'shared' exactly when at least one Share exists (maintained by the share operation in the same transaction).
    visibility text NOT NULL DEFAULT 'private' CHECK (visibility IN ('private', 'shared')),
    -- Optimistic lock: every state change (definition, name, shares, archive, ownership) increments it.
    version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
    last_edited_by uuid,
    archived_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS saved_views_owner_name_uq ON views.saved_views (owner_user_id, resource, lower(name)) WHERE archived_at IS NULL;
CREATE INDEX IF NOT EXISTS saved_views_owner_idx ON views.saved_views (owner_user_id, id DESC) WHERE archived_at IS NULL;
CREATE INDEX IF NOT EXISTS saved_views_archived_idx ON views.saved_views (archived_at) WHERE archived_at IS NOT NULL;

-- Identity never changes (the owner changes only through the explicit take-over operation).
CREATE OR REPLACE FUNCTION views.freeze_view_identity() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.resource <> OLD.resource OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'saved view identity is immutable' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END
$$;
DROP TRIGGER IF EXISTS saved_views_freeze_identity ON views.saved_views;
CREATE TRIGGER saved_views_freeze_identity BEFORE UPDATE ON views.saved_views
    FOR EACH ROW EXECUTE FUNCTION views.freeze_view_identity();

-- ---------------------------------------------------------------- Shares
CREATE TABLE IF NOT EXISTS views.view_shares (
    view_id uuid NOT NULL REFERENCES views.saved_views (id) ON DELETE CASCADE,
    subject_type text NOT NULL CHECK (subject_type IN ('user', 'team', 'role', 'everyone')),
    -- User, Organization Team or Role id by value; NULL exactly for 'everyone'.
    subject_id uuid,
    level text NOT NULL CHECK (level IN ('use', 'edit')),
    granted_by uuid NOT NULL,
    granted_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT view_shares_subject CHECK ((subject_type = 'everyone') = (subject_id IS NULL)),
    -- An everyone share only lets people use the View; editing is never published to everyone.
    CONSTRAINT view_shares_everyone_use CHECK (subject_type <> 'everyone' OR level = 'use')
);
CREATE UNIQUE INDEX IF NOT EXISTS view_shares_subject_uq
    ON views.view_shares (view_id, subject_type, COALESCE(subject_id, '00000000-0000-0000-0000-000000000000'::uuid));
CREATE INDEX IF NOT EXISTS view_shares_subject_idx ON views.view_shares (subject_type, subject_id);

-- ---------------------------------------------------------------- Pins (per user)
CREATE TABLE IF NOT EXISTS views.pins (
    user_id uuid NOT NULL,
    view_id uuid NOT NULL REFERENCES views.saved_views (id) ON DELETE CASCADE,
    group_key text NOT NULL CHECK (group_key ~ '^[a-z][a-z0-9_]{0,39}$'),
    position integer NOT NULL DEFAULT 0 CHECK (position BETWEEN 0 AND 10000),
    -- A hidden row is the user's override: it hides a View a Pin Rule would otherwise show.
    hidden boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, view_id)
);
CREATE INDEX IF NOT EXISTS pins_view_idx ON views.pins (view_id);

-- ---------------------------------------------------------------- Pin Rules (administrators, for Teams and roles)
CREATE TABLE IF NOT EXISTS views.pin_rules (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    view_id uuid NOT NULL REFERENCES views.saved_views (id) ON DELETE CASCADE,
    subject_type text NOT NULL CHECK (subject_type IN ('team', 'role')),
    subject_id uuid NOT NULL,
    group_key text NOT NULL CHECK (group_key ~ '^[a-z][a-z0-9_]{0,39}$'),
    position integer NOT NULL DEFAULT 0 CHECK (position BETWEEN 0 AND 10000),
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (view_id, subject_type, subject_id)
);
CREATE INDEX IF NOT EXISTS pin_rules_subject_idx ON views.pin_rules (subject_type, subject_id);

-- ---------------------------------------------------------------- Sidebar presentation (per user)
CREATE TABLE IF NOT EXISTS views.sidebar_state (
    user_id uuid PRIMARY KEY,
    collapsed_groups text[] NOT NULL DEFAULT '{}' CHECK (cardinality(collapsed_groups) <= 20),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------- Q-A review: substring and search indexes
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- Ticket reference and title (contains, ends_with, search). Equality on the reference compares lower(reference).
CREATE INDEX IF NOT EXISTS tickets_reference_trgm_idx ON servicedesk.tickets USING gin (reference gin_trgm_ops);
CREATE INDEX IF NOT EXISTS tickets_title_trgm_idx ON servicedesk.tickets USING gin (title gin_trgm_ops);
CREATE INDEX IF NOT EXISTS tickets_reference_lower_idx ON servicedesk.tickets (lower(reference));
-- Task title.
CREATE INDEX IF NOT EXISTS tasks_title_trgm_idx ON platform.tasks USING gin (title gin_trgm_ops);
-- Device name and serial number.
CREATE INDEX IF NOT EXISTS devices_name_trgm_idx ON endpoints.devices USING gin (name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS devices_serial_trgm_idx ON endpoints.devices USING gin (serial_number gin_trgm_ops);
