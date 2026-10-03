# F4 Inventory, Procurement and Assets — Feature Design

**Status:** Accepted 2026-10-03. Target design; [current status](current-status.md) is authoritative for what exists. Related: [procurement workflow](../workflows/procurement.md), [hardware request](../workflows/hardware-request.md), [F3 design](f3-requests-design.md), [state machines](../domain/state-machines.md), [core data model](../domain/core-data-model.md).

## Scope decisions

- **D1 Three modules, three schemas.** `assets` (identity, lifecycle, assignment), `inventory` (warehouses, storage locations, stock, immutable transactions, reservations, goods receipt) and `procurement` (suppliers, procurement requests, purchase orders). This follows the [module boundaries](../architecture/module-boundaries.md); cross-module work happens through `public` contracts inside the caller's transaction (the pattern of `tasks/public` and `approvals/public`).
- **D2 Serialized things are Assets, quantities are stock.** A Product with `serialized` creates Assets on goods receipt and never appears in stock balances; other stock-managed Products are tracked by quantity per Storage Location. A Product that is neither is not tracked.
- **D3 Stock is a ledger.** `inventory.inventory_transactions` is append-only (enforced by a database trigger). `inventory.stock_balances` (`on_hand`, `reserved` per Product and Storage Location) is updated in the same transaction under a row lock and protected by check constraints (`on_hand >= 0`, `0 <= reserved <= on_hand`), so available stock cannot go negative even under concurrency. Tests reconcile the balance against the ledger.
- **D4 Reservations lock a place.** A quantity Reservation targets one Product at one Storage Location (the caller picks it, the UI proposes the location with enough available stock); an Asset Reservation targets one `available` Asset and moves it to `reserved`. A partial unique index allows one active Reservation per Asset. Terminal Reservations are never rewound.
- **D5 Purchase Order approval reuses Approvals.** Submitting a PO for approval names an approver User or Team and creates an Approval (`subject_type = purchase_order`); the `ApprovalDecided` consumer approves the PO or returns it to `draft` with the reason `approval_rejected`. The PO creator can never decide it (existing exclusion rule). Budget policies are not implemented.
- **D6 Goods Receipt belongs to Inventory** (module-boundaries table) and drives the cross-module effects in one transaction: it records received quantities on the PO lines through `procurement/public`, creates stock transactions for quantity products and creates Assets through `assets/public` for serialized products. Posted receipts are immutable; a mistake is corrected by a `correction` transaction or an Asset lifecycle operation, never by editing the receipt.
- **D7 Linking to Service Requests is by reference, not automation.** Reservations and Procurement Requests carry an optional typed origin (`service_request`, `manual`, …). IT staff create them from the request context; fulfillment steps remain Tasks. Automatic reservation or procurement steps in catalog definitions (ADR-0025 extension) are not part of F4.
- **D8 Scanning needs no library.** A lookup endpoint resolves a scanned code (asset tag, serial number or Asset reference) to an Asset; the UI field works with keyboard-wedge scanners and QR labels that encode the Asset URL. Camera scanning is not implemented.
- **D9 Employees see their own equipment.** `GET /api/v1/my-assets` returns Assets currently assigned to the signed-in User (ownership rule, no role). Everything else needs permissions.

## 1. Outcome

IT knows what it has, where it is and who holds it: stock levels with a trustworthy history, reservations that cannot oversell, purchase orders that end in received goods, and Assets with serial numbers, tags, placement and a traceable assignment history.

## 2. Reused concepts

Product/Manufacturer/Category (F3), User/Team/Location (Organization), Approval (F3), Task, Notification, Audit, outbox events, permissions, i18n. Locations stay Organization data; Warehouses refer to them by id without a foreign key.

## 3. New concepts

Asset (+ Asset Assignment), Warehouse, Storage Location, Stock Balance, Inventory Transaction, Reservation, Goods Receipt, Supplier, Procurement Request, Purchase Order (+ Line). All are in the glossary; this milestone adds the terms **Asset Reference** and **Available stock** (on hand − reserved).

## 4. Lifecycles

- **Asset:** `received → available → reserved → assigned → returned → available`; `assigned → in_repair → available`; `available/returned → retired → disposed`; exceptional `lost`. `ordered` is not used (an Asset exists once goods are received). Explicit operations: `MakeAvailable`, `Reserve`/`ReleaseReservation` (called by Inventory), `Assign(user | team | location)`, `Return`, `SendToRepair`, `FinishRepair`, `Retire`, `Dispose`, `MarkLost`, `Recover` (lost → available, reasoned). `disposed` is terminal. Provisioning (`not_required | not_started | pending | in_progress | ready | failed`) is a separate field changed by its own operation; endpoint management state belongs to F6/F9.
- **Asset Assignment:** historical rows with validity; at most one active assignment per Asset (partial unique index); reassigning closes the old row.
- **Reservation:** `active → fulfilled | released | expired | cancelled`; `expired` needs an optional `expires_at` and a job and is implemented as `released` with reason `expired` by a scheduled sweep only if an expiry was set.
- **Purchase Order:** `draft → pending_approval → approved → sent → acknowledged? → partially_received → received → closed`, `cancelled` from `draft`, `pending_approval`, `approved`, `sent` (before any receipt). Editing lines is possible only in `draft`.
- **Procurement Request:** `open → ordered → fulfilled`, `cancelled`; `ordered` when linked to a PO line, `fulfilled` when its PO line is fully received.

## 5. Data and migrations

New schemas `assets`, `inventory`, `procurement`. Forward migrations only. Key constraints: unique Asset reference (sequence, never truncating), unique asset tag, unique serial per Product, one active assignment per Asset, one active Reservation per Asset, stock balance checks, append-only trigger on inventory transactions and goods receipt rows, `received_quantity <= ordered_quantity * 1.0` (over-delivery is refused; the exception path is a manual PO line increase while `sent`). Money is `numeric(14,2)` with a currency code per PO (default `EUR`).

## 6. API (bounded lists, optimistic versions where rows are edited)

- `/api/v1/assets` (filters: status, product, assigned user/team, location, q), `GET/PATCH /assets/{id}`, lifecycle and assignment action endpoints, `GET /assets/lookup?code=`, `GET /my-assets`.
- `/api/v1/warehouses`, `/warehouses/{id}/locations`, `GET /stock` (balances, filter by product/warehouse), `GET /inventory-transactions`, stock operations (`receive-manual` is not offered: stock enters only through goods receipt or `correction`; `issue`, `return`, `transfer`, `correction`, `disposal`), `/reservations` (create, `release`, `fulfill`, `cancel`).
- `/api/v1/suppliers`, `/procurement-requests`, `/purchase-orders` with action endpoints (`submit`, `send`, `acknowledge`, `cancel`, `close`), `/goods-receipts` (create posts immediately; read).

## 7. Permissions

`assets.view`, `assets.manage`, `inventory.view`, `inventory.manage`, `procurement.view`, `procurement.manage`. Goods receipts need `inventory.manage`; reading POs for the receipt screen needs `procurement.view`. All checks are backend-enforced; unknown ids return 404. `my-assets` is ownership based.

## 8. Audit

Every mutation is audited in the same transaction with ids, quantities, statuses and reasons; free text beyond reasons (notes, supplier contact data) is not copied into audit. Inventory transactions are themselves a ledger and additionally audited at operation level (one audit event per operation, not per ledger row).

## 9. Events

`AssetCreated`, `AssetAssigned`, `AssetReturned`, `AssetStatusChanged`, `StockReserved` (existing), `ReservationReleased`, `ReservationFulfilled`, `GoodsReceived` (existing), `PurchaseOrderApproved`, `PurchaseOrderSent`, `PurchaseOrderReceived`. Procurement consumes `ApprovalDecided`. Consumers are idempotent and derive effects from current state.

## 10. Cross-cutting impact

My Work: unchanged. Notifications: `asset.assigned` (the User who receives equipment; email follows the F2 channel) and `purchase_order.approval` is covered by the existing `approval.requested`. Search: list filters only. Relationships: the typed origin on Reservation/Procurement Request replaces a generic Relationship because it carries invariants.

## 11. Security and privacy

Asset assignment history is personal data (who held which device): readable with `assets.view`, the current holder sees their own Assets. Serial numbers and prices are business data, not secrets. Concurrency is the main risk: balance and reservation changes lock rows in a fixed order (PO → balance rows ordered by id → Asset rows ordered by id). Separation of duties: the PO creator cannot approve their PO. No external system is called.

## 12. Tests and documentation

Ledger/balance reconciliation, concurrent reservations (many goroutines against limited stock must never oversell), concurrent asset reservations (one winner), lifecycle table tests, receipt atomicity (a failing asset insert rolls back the PO update), IDOR and permission tests, PO state machine, approval round trip in the worker end-to-end test. Docs: current status, state machines, core data model, glossary, generated references, OpenAPI, local development (seed).

## 13. Not implemented in F4

Product Variants, supplier invoices and three-way match, budgets and cost centers, multi-currency conversion, lot/batch tracking, stock counts (cycle counts beyond `correction`), automatic reordering and `StockLow`, label printing, camera scanning, asset depreciation, automatic fulfillment steps in catalog definitions, Endpoint/Device identity (F6), rack placement (F7).

## 14. ADR

None: no new framework, database or major dependency. The append-only trigger and the balance check constraints are documented here and in the data model.

## 15. Slices

1. Assets backend. 2. Inventory backend (warehouses, ledger, balances, reservations). 3. Procurement backend (suppliers, needs, purchase orders, approval). 4. Goods receipt. 5. UI. 6. Demo seed, reviews, PR.
