-- How an Entra identity was linked (ADR-0035, slice E-B): administrator (API), source_anchor (hybrid match at
-- sign-in), provisioning (auto_employee) or cli (turaco-admin). Directory synchronization leaves it NULL.
ALTER TABLE organization.external_identities ADD COLUMN IF NOT EXISTS linked_via text;
ALTER TABLE organization.external_identities DROP CONSTRAINT IF EXISTS external_identities_linked_via_valid;
ALTER TABLE organization.external_identities ADD CONSTRAINT external_identities_linked_via_valid
    CHECK (linked_via IS NULL OR linked_via IN ('administrator', 'source_anchor', 'provisioning', 'cli'));
