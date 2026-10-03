-- Inventory (F4 slice 2): warehouses, storage locations, an append-only ledger
-- of inventory transactions, balances derived from it and reservations
-- (docs/product/f4-inventory-design.md). Products, assets and organization
-- locations are referenced by id; other modules own those records.
CREATE SCHEMA IF NOT EXISTS inventory;

CREATE TABLE IF NOT EXISTS inventory.warehouses (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 100),
    -- Organization Location the warehouse belongs to (optional, by id).
    location_id uuid,
    active boolean NOT NULL DEFAULT true,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS warehouses_name_unique ON inventory.warehouses (lower(name));

CREATE TABLE IF NOT EXISTS inventory.storage_locations (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    warehouse_id uuid NOT NULL REFERENCES inventory.warehouses(id),
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 100),
    active boolean NOT NULL DEFAULT true,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS storage_locations_name_unique ON inventory.storage_locations (warehouse_id, lower(name));

-- Current quantity per product and storage location. Never written directly:
-- every change goes with an inventory transaction in the same database
-- transaction. The checks make over-reservation and negative stock impossible
-- even if application code is wrong.
CREATE TABLE IF NOT EXISTS inventory.stock_balances (
    product_id uuid NOT NULL,
    storage_location_id uuid NOT NULL REFERENCES inventory.storage_locations(id),
    on_hand integer NOT NULL DEFAULT 0,
    reserved integer NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (product_id, storage_location_id),
    CONSTRAINT stock_on_hand_not_negative CHECK (on_hand >= 0),
    CONSTRAINT stock_reserved_within_on_hand CHECK (reserved >= 0 AND reserved <= on_hand)
);
CREATE INDEX IF NOT EXISTS stock_balances_location_idx ON inventory.stock_balances (storage_location_id);

CREATE TABLE IF NOT EXISTS inventory.reservations (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    kind text NOT NULL CHECK (kind IN ('quantity', 'asset')),
    product_id uuid NOT NULL,
    storage_location_id uuid REFERENCES inventory.storage_locations(id),
    quantity integer CHECK (quantity > 0),
    asset_id uuid,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'fulfilled', 'released', 'expired', 'cancelled')),
    -- The record the reservation is for (service_request, onboarding, change, ...), by id.
    context_type text CHECK (context_type ~ '^[a-z][a-z_]{1,39}$'),
    context_id uuid,
    reason text CHECK (reason IS NULL OR length(reason) <= 500),
    closed_at timestamptz,
    created_by uuid,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT reservations_kind_shape CHECK (
        (kind = 'quantity' AND storage_location_id IS NOT NULL AND quantity IS NOT NULL AND asset_id IS NULL)
        OR (kind = 'asset' AND asset_id IS NOT NULL AND quantity IS NULL AND storage_location_id IS NULL)),
    CONSTRAINT reservations_context_matches CHECK ((context_type IS NULL) = (context_id IS NULL)),
    CONSTRAINT reservations_closed_matches CHECK ((status = 'active') = (closed_at IS NULL))
);
-- One serialized asset has at most one active reservation.
CREATE UNIQUE INDEX IF NOT EXISTS reservations_one_active_per_asset ON inventory.reservations (asset_id) WHERE status = 'active' AND asset_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS reservations_context_idx ON inventory.reservations (context_type, context_id) WHERE context_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS reservations_active_idx ON inventory.reservations (id DESC) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS reservations_product_idx ON inventory.reservations (product_id, id DESC);

-- The ledger: immutable stock movements. The sum of the deltas of a product at
-- a storage location equals its balance.
CREATE TABLE IF NOT EXISTS inventory.inventory_transactions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    type text NOT NULL CHECK (type IN ('goods_receipt', 'reservation', 'release', 'issue', 'return', 'transfer', 'correction', 'disposal')),
    product_id uuid NOT NULL,
    storage_location_id uuid NOT NULL REFERENCES inventory.storage_locations(id),
    on_hand_delta integer NOT NULL,
    reserved_delta integer NOT NULL DEFAULT 0,
    -- Rows that belong together (the two sides of a transfer, a fulfilled reservation) share a group.
    group_id uuid NOT NULL DEFAULT uuidv7(),
    reservation_id uuid REFERENCES inventory.reservations(id),
    context_type text CHECK (context_type ~ '^[a-z][a-z_]{1,39}$'),
    context_id uuid,
    reason text CHECK (reason IS NULL OR length(reason) <= 500),
    actor_user_id uuid,
    actor_system text,
    correlation_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT inventory_transactions_moves CHECK (on_hand_delta <> 0 OR reserved_delta <> 0),
    CONSTRAINT inventory_transactions_context_matches CHECK ((context_type IS NULL) = (context_id IS NULL))
);
CREATE INDEX IF NOT EXISTS inventory_transactions_product_idx ON inventory.inventory_transactions (product_id, id DESC);
CREATE INDEX IF NOT EXISTS inventory_transactions_location_idx ON inventory.inventory_transactions (storage_location_id, id DESC);
CREATE INDEX IF NOT EXISTS inventory_transactions_context_idx ON inventory.inventory_transactions (context_type, context_id) WHERE context_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS inventory_transactions_group_idx ON inventory.inventory_transactions (group_id);

CREATE OR REPLACE FUNCTION inventory.forbid_ledger_change() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'inventory ledger rows are immutable (%.%)', TG_TABLE_SCHEMA, TG_TABLE_NAME USING ERRCODE = 'restrict_violation';
END
$$;

DROP TRIGGER IF EXISTS inventory_transactions_immutable ON inventory.inventory_transactions;
CREATE TRIGGER inventory_transactions_immutable BEFORE UPDATE OR DELETE ON inventory.inventory_transactions
    FOR EACH ROW EXECUTE FUNCTION inventory.forbid_ledger_change();
