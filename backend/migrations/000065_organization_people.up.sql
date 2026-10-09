-- F14 slice A-A: People administration (docs/product/f14-administration-design.md, ADR-0034).
-- Forward-only. Locations become a tree (kind site/area), Departments and Teams get a code or description and a
-- version for expectedVersion, Users get a version and an immutable account kind.

-- ---------------------------------------------------------------- Locations
ALTER TABLE organization.locations
    ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'site',
    ADD COLUMN IF NOT EXISTS parent_location_id uuid REFERENCES organization.locations(id),
    ADD COLUMN IF NOT EXISTS code text,
    ADD COLUMN IF NOT EXISTS description text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS version integer NOT NULL DEFAULT 1;

ALTER TABLE organization.locations
    ADD CONSTRAINT locations_kind_valid CHECK (kind IN ('site', 'area')),
    -- A Site is a root; an area always hangs below another Location.
    ADD CONSTRAINT locations_kind_parent CHECK ((kind = 'site') = (parent_location_id IS NULL)),
    ADD CONSTRAINT locations_name_not_blank CHECK (btrim(name) <> ''),
    ADD CONSTRAINT locations_code_not_blank CHECK (code IS NULL OR btrim(code) <> ''),
    ADD CONSTRAINT locations_description_length CHECK (char_length(description) <= 500),
    ADD CONSTRAINT locations_version_positive CHECK (version >= 1);

CREATE UNIQUE INDEX IF NOT EXISTS locations_code_active_unique
    ON organization.locations (lower(code)) WHERE active AND code IS NOT NULL;
CREATE INDEX IF NOT EXISTS locations_parent_idx ON organization.locations (parent_location_id) WHERE parent_location_id IS NOT NULL;

-- Defence in depth for the tree rules the operations check under an advisory lock: no cycle and at most four
-- levels (site, area, sub-area, sub-sub-area).
CREATE OR REPLACE FUNCTION organization.locations_guard_tree() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    v_depth integer;
    v_cycle boolean;
BEGIN
    IF NEW.parent_location_id IS NULL THEN
        RETURN NEW;
    END IF;
    WITH RECURSIVE up AS (
        SELECT l.id, l.parent_location_id, 1 AS depth FROM organization.locations l WHERE l.id = NEW.parent_location_id
        UNION ALL
        SELECT l.id, l.parent_location_id, up.depth + 1
          FROM organization.locations l JOIN up ON l.id = up.parent_location_id
         WHERE up.depth < 16)
    SELECT coalesce(max(depth), 0) + 1, coalesce(bool_or(id = NEW.id), false) INTO v_depth, v_cycle FROM up;
    IF v_cycle THEN
        RAISE EXCEPTION 'location tree cycle' USING ERRCODE = 'ORG01';
    END IF;
    IF v_depth > 4 THEN
        RAISE EXCEPTION 'location tree is deeper than four levels' USING ERRCODE = 'ORG02';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER locations_guard_tree
    BEFORE INSERT OR UPDATE OF parent_location_id ON organization.locations
    FOR EACH ROW EXECUTE FUNCTION organization.locations_guard_tree();

-- ------------------------------------------------------------- Departments
ALTER TABLE organization.departments
    ADD COLUMN IF NOT EXISTS code text,
    ADD COLUMN IF NOT EXISTS version integer NOT NULL DEFAULT 1;

ALTER TABLE organization.departments
    ADD CONSTRAINT departments_name_not_blank CHECK (btrim(name) <> ''),
    ADD CONSTRAINT departments_code_not_blank CHECK (code IS NULL OR btrim(code) <> ''),
    ADD CONSTRAINT departments_version_positive CHECK (version >= 1);

CREATE UNIQUE INDEX IF NOT EXISTS departments_code_active_unique
    ON organization.departments (lower(code)) WHERE active AND code IS NOT NULL;
CREATE INDEX IF NOT EXISTS departments_parent_idx ON organization.departments (parent_department_id) WHERE parent_department_id IS NOT NULL;

CREATE OR REPLACE FUNCTION organization.departments_guard_tree() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    v_cycle boolean;
BEGIN
    IF NEW.parent_department_id IS NULL THEN
        RETURN NEW;
    END IF;
    WITH RECURSIVE up AS (
        SELECT d.id, d.parent_department_id, 1 AS depth FROM organization.departments d WHERE d.id = NEW.parent_department_id
        UNION ALL
        SELECT d.id, d.parent_department_id, up.depth + 1
          FROM organization.departments d JOIN up ON d.id = up.parent_department_id
         WHERE up.depth < 32)
    SELECT coalesce(bool_or(id = NEW.id), false) INTO v_cycle FROM up;
    IF v_cycle THEN
        RAISE EXCEPTION 'department tree cycle' USING ERRCODE = 'ORG01';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER departments_guard_tree
    BEFORE INSERT OR UPDATE OF parent_department_id ON organization.departments
    FOR EACH ROW EXECUTE FUNCTION organization.departments_guard_tree();

-- ------------------------------------------------------------------- Teams
ALTER TABLE organization.teams
    ADD COLUMN IF NOT EXISTS description text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS version integer NOT NULL DEFAULT 1;
ALTER TABLE organization.teams
    ADD CONSTRAINT teams_description_length CHECK (char_length(description) <= 500),
    ADD CONSTRAINT teams_version_positive CHECK (version >= 1);

-- A membership is a lead or a member. The role used to be free text; anything that is not "lead" is a member.
UPDATE organization.team_memberships SET role = CASE WHEN lower(btrim(role)) = 'lead' THEN 'lead' ELSE 'member' END
 WHERE role IS DISTINCT FROM 'lead' AND role IS DISTINCT FROM 'member';
UPDATE organization.team_memberships SET role = 'member' WHERE role IS NULL;
ALTER TABLE organization.team_memberships
    ALTER COLUMN role SET DEFAULT 'member',
    ALTER COLUMN role SET NOT NULL,
    ADD CONSTRAINT team_memberships_role_valid CHECK (role IN ('lead', 'member'));

-- ------------------------------------------------------------------- Users
ALTER TABLE organization.users
    ADD COLUMN IF NOT EXISTS version integer NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS account_kind text NOT NULL DEFAULT 'employee',
    -- The External Party table arrives with slice A-G (migration 000070), which adds the foreign key.
    ADD COLUMN IF NOT EXISTS external_party_id uuid,
    ADD COLUMN IF NOT EXISTS access_expires_at timestamptz;

ALTER TABLE organization.users
    ADD CONSTRAINT users_version_positive CHECK (version >= 1),
    ADD CONSTRAINT users_account_kind_valid CHECK (account_kind IN ('employee', 'external')),
    ADD CONSTRAINT users_external_needs_expiry CHECK (account_kind = 'employee' OR access_expires_at IS NOT NULL),
    ADD CONSTRAINT users_employee_no_external_fields CHECK (account_kind = 'external' OR (external_party_id IS NULL AND access_expires_at IS NULL));

-- Where the account comes from: "directory" (created by directory synchronization), "local" (created in Turaco)
-- or "emergency" (the CLI-only break-glass account). It decides who owns the profile attributes (directory-owned
-- attributes are read-only for directory Users) and which operations apply. Existing rows are classified by their
-- external identities and local credentials.
ALTER TABLE organization.users ADD COLUMN IF NOT EXISTS origin text NOT NULL DEFAULT 'directory';
UPDATE organization.users u SET origin = 'local'
 WHERE NOT EXISTS (SELECT 1 FROM organization.external_identities e WHERE e.user_id = u.id);
UPDATE organization.users u SET origin = 'emergency'
 WHERE EXISTS (SELECT 1 FROM platform.local_credentials c WHERE c.user_id = u.id);
ALTER TABLE organization.users ADD CONSTRAINT users_origin_valid CHECK (origin IN ('directory', 'local', 'emergency'));

-- account_kind and origin never change after creation (ADR-0034, review rule R4: kind confusion). There is no
-- operation for it; this makes ad-hoc SQL and application bugs fail as well.
CREATE OR REPLACE FUNCTION organization.users_identity_columns_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.account_kind IS DISTINCT FROM OLD.account_kind THEN
        RAISE EXCEPTION 'organization.users.account_kind is immutable' USING ERRCODE = 'ORG03';
    END IF;
    IF NEW.origin IS DISTINCT FROM OLD.origin THEN
        RAISE EXCEPTION 'organization.users.origin is immutable' USING ERRCODE = 'ORG03';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER users_identity_columns_immutable
    BEFORE UPDATE OF account_kind, origin ON organization.users
    FOR EACH ROW EXECUTE FUNCTION organization.users_identity_columns_immutable();

-- Substring search of the Users catalog (F13 rule: only trigram-indexed fields are searchable).
CREATE INDEX IF NOT EXISTS users_display_name_trgm_idx ON organization.users USING gin (display_name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS users_primary_email_trgm_idx ON organization.users USING gin (primary_email gin_trgm_ops);
-- Keyset sorts of the Users catalog.
CREATE INDEX IF NOT EXISTS users_display_name_sort_idx ON organization.users (lower(display_name), id);
CREATE INDEX IF NOT EXISTS users_status_sort_idx ON organization.users (status, id);
CREATE INDEX IF NOT EXISTS users_created_sort_idx ON organization.users (created_at, id);
CREATE INDEX IF NOT EXISTS users_updated_sort_idx ON organization.users (updated_at, id);

-- Keyset sorts of the Teams, Locations and Departments catalogs.
CREATE INDEX IF NOT EXISTS teams_name_sort_idx ON organization.teams (lower(name), id);
CREATE INDEX IF NOT EXISTS locations_name_sort_idx ON organization.locations (lower(name), id);
CREATE INDEX IF NOT EXISTS departments_name_sort_idx ON organization.departments (lower(name), id);
