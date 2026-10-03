-- F4 review fixes.

-- A rejected or cancelled approval step must not block a new request for the same step (a rejected
-- purchase order is corrected and submitted again); a pending or approved step stays unique.
ALTER TABLE approvals.approvals DROP CONSTRAINT IF EXISTS approvals_step_unique;
CREATE UNIQUE INDEX IF NOT EXISTS approvals_step_unique ON approvals.approvals (subject_type, subject_id, step_index)
    WHERE status IN ('pending', 'approved');

-- Scanner lookup by serial number and ledger filters by type.
CREATE INDEX IF NOT EXISTS assets_serial_lookup_idx ON assets.assets (lower(serial_number)) WHERE serial_number IS NOT NULL;
CREATE INDEX IF NOT EXISTS inventory_transactions_type_idx ON inventory.inventory_transactions (type, id DESC);

-- A retried goods receipt with the same client key returns the posted receipt instead of booking twice.
ALTER TABLE inventory.goods_receipts ADD COLUMN IF NOT EXISTS idempotency_key text
    CHECK (idempotency_key IS NULL OR (length(idempotency_key) BETWEEN 8 AND 100));
CREATE UNIQUE INDEX IF NOT EXISTS goods_receipts_idempotency_unique ON inventory.goods_receipts (idempotency_key) WHERE idempotency_key IS NOT NULL;

-- Everyone who edited a draft purchase order can never approve it.
ALTER TABLE procurement.purchase_orders ADD COLUMN IF NOT EXISTS editors uuid[] NOT NULL DEFAULT '{}';
