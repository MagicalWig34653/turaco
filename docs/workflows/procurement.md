# Procurement

## Goal
Acquire unavailable products/services and reconcile delivery into inventory/assets.

## Flow
1. ProcurementRequest originates from ServiceRequest, replenishment, Initiative or manual IT need.
2. Approval/budget policy runs where needed.
3. One or more approved needs become PurchaseOrder Lines grouped by Supplier.
4. PurchaseOrder is approved/sent/acknowledged.
5. Goods Receipt records partial/full deliveries. Posted receipts are immutable; corrections use reversal/correction.
6. Non-serialized items create Inventory Transactions/balance. Serialized items create Asset records with serial/asset tag and placement.
7. Linked Reservations/Requests can continue fulfillment.
8. PurchaseOrder becomes received/closed only when quantities and exceptions are resolved.
