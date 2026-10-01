-- Password login resolves an identifier with
--   provider_key = $1 AND lower(username) = lower($2) AND deleted_observed_at IS NULL.
-- This partial expression index serves exactly that lookup (it runs before any
-- password verification, so it must not scan the identity table).
CREATE INDEX IF NOT EXISTS external_identities_login_lookup_idx
    ON organization.external_identities (provider_key, lower(username))
    WHERE deleted_observed_at IS NULL;
