-- Assets (F4 slice 1): individually tracked instances of Products with a
-- lifecycle and a history of assignments (docs/product/f4-inventory-design.md).
CREATE SCHEMA IF NOT EXISTS assets;

CREATE SEQUENCE IF NOT EXISTS assets.asset_number_seq;

-- Human reference AST-000001; the width grows instead of truncating.
CREATE OR REPLACE FUNCTION assets.next_reference() RETURNS text
LANGUAGE sql
AS $$
    SELECT 'AST-' || CASE WHEN n < 1000000 THEN lpad(n::text, 6, '0') ELSE n::text END
    FROM (SELECT nextval('assets.asset_number_seq') AS n) AS s
$$;

CREATE TABLE IF NOT EXISTS assets.assets (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    reference text NOT NULL DEFAULT assets.next_reference(),
    product_id uuid NOT NULL,
    serial_number text CHECK (serial_number IS NULL OR (serial_number = btrim(serial_number) AND length(serial_number) BETWEEN 1 AND 100)),
    asset_tag text CHECK (asset_tag IS NULL OR (asset_tag = btrim(asset_tag) AND length(asset_tag) BETWEEN 1 AND 50)),
    status text NOT NULL CHECK (status IN ('received', 'available', 'reserved', 'assigned', 'returned', 'in_repair', 'retired', 'disposed', 'lost')),
    status_reason text,
    provisioning_status text NOT NULL DEFAULT 'not_required'
        CHECK (provisioning_status IN ('not_required', 'not_started', 'pending', 'in_progress', 'ready', 'failed')),
    ownership_type text NOT NULL DEFAULT 'owned' CHECK (ownership_type IN ('owned', 'leased', 'loaned')),
    -- Provenance and organization references by id (other modules own the records).
    supplier_id uuid,
    source_type text CHECK (source_type IN ('goods_receipt')),
    source_id uuid,
    purchased_at date,
    warranty_until date,
    location_id uuid,
    notes text CHECK (notes IS NULL OR length(notes) <= 2000),
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT assets_reference_unique UNIQUE (reference),
    CONSTRAINT assets_source_matches CHECK ((source_type IS NULL) = (source_id IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS assets_serial_unique ON assets.assets (product_id, lower(serial_number)) WHERE serial_number IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS assets_tag_unique ON assets.assets (lower(asset_tag)) WHERE asset_tag IS NOT NULL;
CREATE INDEX IF NOT EXISTS assets_product_idx ON assets.assets (product_id, id DESC);
CREATE INDEX IF NOT EXISTS assets_status_idx ON assets.assets (status, id DESC);

-- Historical assignments; at most one is active (returned_at IS NULL) per asset.
CREATE TABLE IF NOT EXISTS assets.asset_assignments (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    asset_id uuid NOT NULL REFERENCES assets.assets(id) ON DELETE CASCADE,
    assignee_type text NOT NULL CHECK (assignee_type IN ('user', 'team', 'location')),
    assignee_id uuid NOT NULL,
    assigned_at timestamptz NOT NULL DEFAULT now(),
    assigned_by uuid,
    returned_at timestamptz,
    note text CHECK (note IS NULL OR length(note) <= 500),
    CONSTRAINT asset_assignments_period CHECK (returned_at IS NULL OR returned_at >= assigned_at)
);
CREATE UNIQUE INDEX IF NOT EXISTS asset_assignments_one_active ON assets.asset_assignments (asset_id) WHERE returned_at IS NULL;
CREATE INDEX IF NOT EXISTS asset_assignments_assignee_idx ON assets.asset_assignments (assignee_type, assignee_id) WHERE returned_at IS NULL;
CREATE INDEX IF NOT EXISTS asset_assignments_asset_idx ON assets.asset_assignments (asset_id, assigned_at DESC);
