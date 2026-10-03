-- Procurement (F4 slice 3): suppliers, internal acquisition needs and purchase
-- orders with lines (docs/product/f4-inventory-design.md). Products, users and
-- approvals are referenced by id; other modules own those records.
CREATE SCHEMA IF NOT EXISTS procurement;

CREATE TABLE IF NOT EXISTS procurement.suppliers (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 150),
    -- Our customer number at the supplier.
    account_reference text CHECK (account_reference IS NULL OR (account_reference = btrim(account_reference) AND length(account_reference) BETWEEN 1 AND 100)),
    active boolean NOT NULL DEFAULT true,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS suppliers_name_unique ON procurement.suppliers (lower(name));

CREATE SEQUENCE IF NOT EXISTS procurement.need_number_seq;
CREATE SEQUENCE IF NOT EXISTS procurement.order_number_seq;

CREATE OR REPLACE FUNCTION procurement.next_reference(prefix text, n bigint) RETURNS text
LANGUAGE sql IMMUTABLE
AS $$ SELECT prefix || '-' || CASE WHEN n < 1000000 THEN lpad(n::text, 6, '0') ELSE n::text END $$;

-- A Procurement Request: an internal need to acquire something.
CREATE TABLE IF NOT EXISTS procurement.procurement_requests (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    reference text NOT NULL DEFAULT procurement.next_reference('PRQ', nextval('procurement.need_number_seq')),
    product_id uuid NOT NULL,
    quantity integer NOT NULL CHECK (quantity BETWEEN 1 AND 1000000),
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'ordered', 'fulfilled', 'cancelled')),
    status_reason text CHECK (status_reason IS NULL OR length(status_reason) <= 500),
    -- The record the need comes from (service_request, initiative, ...), by id; null for a manual need.
    context_type text CHECK (context_type ~ '^[a-z][a-z_]{1,39}$'),
    context_id uuid,
    notes text CHECK (notes IS NULL OR length(notes) <= 2000),
    requested_by uuid,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT procurement_requests_reference_unique UNIQUE (reference),
    CONSTRAINT procurement_requests_context_matches CHECK ((context_type IS NULL) = (context_id IS NULL))
);
CREATE INDEX IF NOT EXISTS procurement_requests_status_idx ON procurement.procurement_requests (status, id DESC);
CREATE INDEX IF NOT EXISTS procurement_requests_product_idx ON procurement.procurement_requests (product_id, id DESC);
CREATE INDEX IF NOT EXISTS procurement_requests_context_idx ON procurement.procurement_requests (context_type, context_id) WHERE context_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS procurement.purchase_orders (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    reference text NOT NULL DEFAULT procurement.next_reference('PO', nextval('procurement.order_number_seq')),
    supplier_id uuid NOT NULL REFERENCES procurement.suppliers(id),
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'pending_approval', 'approved', 'sent', 'acknowledged', 'partially_received', 'received', 'closed', 'cancelled')),
    status_reason text CHECK (status_reason IS NULL OR length(status_reason) <= 500),
    currency text NOT NULL DEFAULT 'EUR' CHECK (currency ~ '^[A-Z]{3}$'),
    notes text CHECK (notes IS NULL OR length(notes) <= 2000),
    created_by uuid,
    sent_at timestamptz,
    closed_at timestamptz,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT purchase_orders_reference_unique UNIQUE (reference)
);
CREATE INDEX IF NOT EXISTS purchase_orders_status_idx ON procurement.purchase_orders (status, id DESC);
CREATE INDEX IF NOT EXISTS purchase_orders_supplier_idx ON procurement.purchase_orders (supplier_id, id DESC);

CREATE TABLE IF NOT EXISTS procurement.purchase_order_lines (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id uuid NOT NULL REFERENCES procurement.purchase_orders(id) ON DELETE CASCADE,
    line_no integer NOT NULL CHECK (line_no > 0),
    product_id uuid NOT NULL,
    quantity integer NOT NULL CHECK (quantity BETWEEN 1 AND 1000000),
    -- Minor currency units (cents); no floating point money.
    unit_price_cents bigint NOT NULL CHECK (unit_price_cents BETWEEN 0 AND 100000000000),
    received_quantity integer NOT NULL DEFAULT 0,
    -- The procurement request this line satisfies (cleared when the order is cancelled or closed short).
    request_id uuid REFERENCES procurement.procurement_requests(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT purchase_order_lines_received_bounds CHECK (received_quantity >= 0 AND received_quantity <= quantity),
    CONSTRAINT purchase_order_lines_line_no_unique UNIQUE (order_id, line_no)
);
CREATE UNIQUE INDEX IF NOT EXISTS purchase_order_lines_request_unique ON procurement.purchase_order_lines (request_id) WHERE request_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS purchase_order_lines_product_idx ON procurement.purchase_order_lines (product_id);
