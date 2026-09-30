# Hardware Request

## Goal
Employee requests one hardware item such as notebook/monitor/peripheral.

## Flow
1. Catalog Item identifies requester/requested-for and prefilled organization context.
2. ServiceRequest submitted.
3. Required approvals are created; all approved → Request approved.
4. Fulfillment checks Product/Variant availability.
5. If serialized Asset is available, create exclusive Reservation; if quantity stock, reserve quantity.
6. If unavailable, create ProcurementRequest linked to the ServiceRequest.
7. On Goods Receipt, create stock/serialized Assets and satisfy reservation.
8. Serialized endpoint provisioning runs as separate provisioning state/tasks.
9. Issue/assign Asset; fulfill Reservation.
10. Completion criteria close the ServiceRequest.

All stock/asset movements remain traceable; Request is not converted into a PurchaseOrder or Ticket.
