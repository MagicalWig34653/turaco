-- Platform attachments (ADR-0037): metadata of files stored in the storage port (encrypted, addressed by generated
-- object ids). The owning module authorizes access through a registered owner type; this table never joins module
-- tables. The original file name is data only and never part of a path or object key.
CREATE TABLE IF NOT EXISTS platform.attachments (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    owner_type text NOT NULL CHECK (owner_type ~ '^[a-z][a-z0-9_]{0,39}$'),
    owner_id uuid NOT NULL,
    object_id text NOT NULL UNIQUE CHECK (object_id ~ '^[0-9a-f]{32}$'),
    file_name text NOT NULL CHECK (char_length(file_name) BETWEEN 1 AND 255),
    content_type text NOT NULL CHECK (char_length(content_type) BETWEEN 3 AND 150),
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    scan_status text NOT NULL DEFAULT 'pending' CHECK (scan_status IN ('pending', 'clean', 'infected', 'failed')),
    scan_signature text CHECK (scan_signature IS NULL OR char_length(scan_signature) <= 200),
    scan_attempts integer NOT NULL DEFAULT 0 CHECK (scan_attempts >= 0),
    scanned_at timestamptz,
    last_scan_error text CHECK (last_scan_error IS NULL OR char_length(last_scan_error) <= 200),
    audience text NOT NULL DEFAULT 'all' CHECK (audience IN ('all', 'privileged')),
    uploaded_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    deleted_by uuid,
    object_purged_at timestamptz,
    CONSTRAINT attachments_scan_consistent CHECK ((scan_status = 'pending') = (scanned_at IS NULL)),
    CONSTRAINT attachments_deleted_consistent CHECK ((deleted_at IS NULL) = (deleted_by IS NULL) AND (object_purged_at IS NULL OR deleted_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS attachments_owner_idx ON platform.attachments (owner_type, owner_id, created_at, id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS attachments_pending_idx ON platform.attachments (created_at, id) WHERE scan_status = 'pending' AND deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS attachments_purge_idx ON platform.attachments (deleted_at, id) WHERE deleted_at IS NOT NULL AND object_purged_at IS NULL;
