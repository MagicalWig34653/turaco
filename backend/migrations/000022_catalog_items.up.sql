-- Catalog Items (F3 slice 4, ADR-0025). The definition (form fields,
-- approval steps, fulfillment task templates) is validated by the catalog
-- module against a closed schema before it is stored.
CREATE TABLE IF NOT EXISTS catalog.items (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    -- Stable, human-readable identifier; never changes after creation.
    key text NOT NULL CHECK (key ~ '^[a-z][a-z0-9-]{1,62}$'),
    title text NOT NULL CHECK (btrim(title) <> ''),
    description text NOT NULL DEFAULT '',
    definition jsonb NOT NULL CHECK (jsonb_typeof(definition) = 'object'),
    active boolean NOT NULL DEFAULT true,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT catalog_items_key_unique UNIQUE (key)
);
CREATE INDEX IF NOT EXISTS catalog_items_active_idx ON catalog.items (id) WHERE active;

-- Manager lookup for approval steps ("approver: manager"); organization.users.manager_user_id exists (000003).
