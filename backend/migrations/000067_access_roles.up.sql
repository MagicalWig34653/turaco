-- F14 slice A-B: role templates, versions and assignment expiry (docs/product/f14-administration-design.md, section 2).
-- Forward-only.

ALTER TABLE platform.roles
    ADD COLUMN IF NOT EXISTS template_key text,
    ADD COLUMN IF NOT EXISTS template_version integer,
    ADD COLUMN IF NOT EXISTS version integer NOT NULL DEFAULT 1;
ALTER TABLE platform.roles
    ADD CONSTRAINT roles_template_pair CHECK ((template_key IS NULL) = (template_version IS NULL)),
    ADD CONSTRAINT roles_version_positive CHECK (version >= 1);

ALTER TABLE platform.role_assignments
    ADD COLUMN IF NOT EXISTS expires_at timestamptz;
ALTER TABLE platform.role_assignments
    ADD CONSTRAINT role_assignments_expiry_after_creation CHECK (expires_at IS NULL OR expires_at > created_at);

-- The expiry job reads the open assignments that are due.
CREATE INDEX IF NOT EXISTS role_assignments_expiry_idx ON platform.role_assignments (expires_at)
    WHERE revoked_at IS NULL AND expires_at IS NOT NULL;

-- The built-in administrator role is never time-limited (review rule R6). The operations check it as well; this
-- keeps ad-hoc SQL honest.
CREATE OR REPLACE FUNCTION platform.role_assignments_admin_no_expiry() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.expires_at IS NOT NULL AND EXISTS (SELECT 1 FROM platform.roles r WHERE r.id = NEW.role_id AND r.built_in) THEN
        RAISE EXCEPTION 'the built-in administrator role cannot be assigned with an expiry' USING ERRCODE = 'ACC01';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER role_assignments_admin_no_expiry
    BEFORE INSERT OR UPDATE OF expires_at, role_id ON platform.role_assignments
    FOR EACH ROW EXECUTE FUNCTION platform.role_assignments_admin_no_expiry();
