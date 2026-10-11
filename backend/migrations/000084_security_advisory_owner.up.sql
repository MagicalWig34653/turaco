ALTER TABLE security.advisories ADD COLUMN owner_user_id uuid REFERENCES organization.users(id) ON DELETE SET NULL;
CREATE INDEX advisories_owner_user_id_idx ON security.advisories (owner_user_id) WHERE owner_user_id IS NOT NULL;
