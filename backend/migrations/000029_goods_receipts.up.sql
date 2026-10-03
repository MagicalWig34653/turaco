-- Goods receipts (F4 slice 4): immutable records of delivered goods. A posted
-- receipt is never changed; corrections are stock corrections or asset
-- operations (docs/product/f4-inventory-design.md).
CREATE SEQUENCE IF NOT EXISTS inventory.receipt_number_seq;

CREATE OR REPLACE FUNCTION inventory.next_receipt_reference() RETURNS text
LANGUAGE sql
AS $$
    SELECT 'GR-' || CASE WHEN n < 1000000 THEN lpad(n::text, 6, '0') ELSE n::text END
    FROM (SELECT nextval('inventory.receipt_number_seq') AS n) AS s
$$;

CREATE TABLE IF NOT EXISTS inventory.goods_receipts (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    reference text NOT NULL DEFAULT inventory.next_receipt_reference(),
    -- The purchase order (procurement) and its supplier, by id.
    order_id uuid NOT NULL,
    supplier_id uuid NOT NULL,
    delivery_note text CHECK (delivery_note IS NULL OR (delivery_note = btrim(delivery_note) AND length(delivery_note) BETWEEN 1 AND 100)),
    received_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT goods_receipts_reference_unique UNIQUE (reference)
);
CREATE INDEX IF NOT EXISTS goods_receipts_order_idx ON inventory.goods_receipts (order_id, id DESC);

CREATE TABLE IF NOT EXISTS inventory.goods_receipt_lines (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    receipt_id uuid NOT NULL REFERENCES inventory.goods_receipts(id),
    order_line_id uuid NOT NULL,
    product_id uuid NOT NULL,
    quantity integer NOT NULL CHECK (quantity BETWEEN 1 AND 1000000),
    -- Where stock went; null for serialized products (they become assets) and untracked products.
    storage_location_id uuid REFERENCES inventory.storage_locations(id)
);
CREATE INDEX IF NOT EXISTS goods_receipt_lines_receipt_idx ON inventory.goods_receipt_lines (receipt_id);

-- The assets a serialized line created.
CREATE TABLE IF NOT EXISTS inventory.goods_receipt_assets (
    receipt_line_id uuid NOT NULL REFERENCES inventory.goods_receipt_lines(id),
    asset_id uuid NOT NULL,
    PRIMARY KEY (receipt_line_id, asset_id),
    CONSTRAINT goods_receipt_assets_asset_unique UNIQUE (asset_id)
);

DROP TRIGGER IF EXISTS goods_receipts_immutable ON inventory.goods_receipts;
CREATE TRIGGER goods_receipts_immutable BEFORE UPDATE OR DELETE ON inventory.goods_receipts
    FOR EACH ROW EXECUTE FUNCTION inventory.forbid_ledger_change();
DROP TRIGGER IF EXISTS goods_receipt_lines_immutable ON inventory.goods_receipt_lines;
CREATE TRIGGER goods_receipt_lines_immutable BEFORE UPDATE OR DELETE ON inventory.goods_receipt_lines
    FOR EACH ROW EXECUTE FUNCTION inventory.forbid_ledger_change();
DROP TRIGGER IF EXISTS goods_receipt_assets_immutable ON inventory.goods_receipt_assets;
CREATE TRIGGER goods_receipt_assets_immutable BEFORE UPDATE OR DELETE ON inventory.goods_receipt_assets
    FOR EACH ROW EXECUTE FUNCTION inventory.forbid_ledger_change();
