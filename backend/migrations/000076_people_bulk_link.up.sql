-- F14 people operations: stored previews of CSV imports and bulk operations, and the one allowed change of a User's
-- origin (linking a directory identity to a local account, ADR-0034 review rule R5).
-- Forward-only.

-- A batch is a dry-run result owned by its creator. kind 'bulk_users' is a bulk operation on selected Users; the other
-- kinds are CSV imports. Rows carry personal data and are deleted when the preview expires (one hour) or, once the
-- batch is applied, together with the batch's rows (the batch keeps counts and the file hash only).
CREATE TABLE IF NOT EXISTS organization.import_batches (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    kind text NOT NULL CHECK (kind IN ('users', 'locations', 'departments', 'bulk_users')),
    match_key text,
    mode text,
    operation text,
    params jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by uuid NOT NULL,
    status text NOT NULL DEFAULT 'previewed' CHECK (status IN ('previewed', 'applied')),
    file_hash text,
    preview_hash text NOT NULL,
    row_count integer NOT NULL CHECK (row_count >= 0),
    counts jsonb NOT NULL DEFAULT '{}'::jsonb,
    unknown_columns jsonb NOT NULL DEFAULT '[]'::jsonb,
    correlation_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    applied_at timestamptz,
    applied_counts jsonb
);
CREATE INDEX IF NOT EXISTS import_batches_expiry_idx ON organization.import_batches (expires_at);
CREATE INDEX IF NOT EXISTS import_batches_creator_idx ON organization.import_batches (created_by, created_at DESC);

CREATE TABLE IF NOT EXISTS organization.import_rows (
    batch_id uuid NOT NULL REFERENCES organization.import_batches(id) ON DELETE CASCADE,
    row_no integer NOT NULL CHECK (row_no >= 1),
    action text NOT NULL CHECK (action IN ('create', 'update', 'unchanged', 'reject')),
    row_key text,
    data jsonb NOT NULL DEFAULT '{}'::jsonb,
    target_id uuid,
    target_version integer,
    diff jsonb NOT NULL DEFAULT '{}'::jsonb,
    errors jsonb NOT NULL DEFAULT '[]'::jsonb,
    warnings jsonb NOT NULL DEFAULT '[]'::jsonb,
    PRIMARY KEY (batch_id, row_no)
);
CREATE INDEX IF NOT EXISTS import_rows_action_idx ON organization.import_rows (batch_id, action, row_no);

-- origin stays immutable except for one transition: a local account becomes a directory account when a directory
-- identity is attached and the local credential is gone. The database checks both so that no code path can link an
-- identity while the old password still works (the pre-provisioning attack of review rule R5).
CREATE OR REPLACE FUNCTION organization.users_identity_columns_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.account_kind IS DISTINCT FROM OLD.account_kind THEN
        RAISE EXCEPTION 'organization.users.account_kind is immutable' USING ERRCODE = 'ORG03';
    END IF;
    IF NEW.origin IS DISTINCT FROM OLD.origin THEN
        IF OLD.origin = 'local' AND NEW.origin = 'directory' AND OLD.account_kind = 'employee'
           AND EXISTS (SELECT 1 FROM organization.external_identities e WHERE e.user_id = NEW.id)
           AND NOT EXISTS (SELECT 1 FROM platform.local_credentials c WHERE c.user_id = NEW.id) THEN
            RETURN NEW;
        END IF;
        RAISE EXCEPTION 'organization.users.origin is immutable' USING ERRCODE = 'ORG03';
    END IF;
    RETURN NEW;
END $$;
