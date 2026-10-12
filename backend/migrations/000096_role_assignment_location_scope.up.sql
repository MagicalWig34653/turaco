-- F16 slice L-A: Location-scoped Role Assignments (ADR-0040, docs/product/f16-location-scoped-access-design.md L1).
-- Forward-only. Existing assignments stay scope 'global' without a Location; no data is rewritten.

-- A scoped assignment names one Location (organization.locations.id). No foreign key into the organization schema
-- (platform never depends on a business schema; same rule as subject_id); the role service validates the Location
-- through organization/public when it creates a scoped assignment (slice L-B).
ALTER TABLE platform.role_assignments
    ADD COLUMN scope_location_id uuid;

ALTER TABLE platform.role_assignments
    DROP CONSTRAINT role_assignments_scope_check;

ALTER TABLE platform.role_assignments
    ADD CONSTRAINT role_assignments_scope_check CHECK (scope IN ('global', 'location')),
    ADD CONSTRAINT role_assignments_scope_location CHECK ((scope = 'location') = (scope_location_id IS NOT NULL));

-- The same role may be held by one subject globally and at several Locations; effective rights are the union.
-- NULLS NOT DISTINCT keeps at most one active global assignment per role and subject.
CREATE UNIQUE INDEX role_assignments_active_scope_unique
    ON platform.role_assignments (role_id, subject_type, subject_id, scope_location_id) NULLS NOT DISTINCT
    WHERE revoked_at IS NULL;
DROP INDEX platform.role_assignments_active_unique;

-- Holders at a Location and the Location move check (slices L-B, L-C).
CREATE INDEX role_assignments_scope_location_idx ON platform.role_assignments (scope_location_id)
    WHERE revoked_at IS NULL AND scope_location_id IS NOT NULL;

-- The built-in administrator role is always global. The operations check it as well (slice L-B); this keeps ad-hoc
-- SQL honest, like role_assignments_admin_no_expiry.
CREATE OR REPLACE FUNCTION platform.role_assignments_admin_global() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.scope <> 'global' AND EXISTS (SELECT 1 FROM platform.roles r WHERE r.id = NEW.role_id AND r.built_in) THEN
        RAISE EXCEPTION 'the built-in administrator role cannot be assigned with a Location scope' USING ERRCODE = 'ACC02';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER role_assignments_admin_global
    BEFORE INSERT OR UPDATE OF scope, scope_location_id, role_id ON platform.role_assignments
    FOR EACH ROW EXECUTE FUNCTION platform.role_assignments_admin_global();
