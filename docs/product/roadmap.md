# Product Roadmap

**Status:** Directional, not a commitment.

## Foundation
- repository governance and CI
- auth abstraction and AD/LDAP foundation
- users/teams/locations
- permissions/scopes
- audit/events/outbox
- object storage/secrets/localization
- tasks/My Work/notifications

## Phase 1 — Service Catalog & internal ordering
- catalog items/forms
- hardware/workplace/access/software requests
- approvals
- IT-to-logistics procurement request
- request status communication

## Phase 2 — Inventory & Assets
- products/variants
- warehouse/storage locations
- goods receipt/reservations/stock transactions
- serialized assets, QR/barcode, assignment/return
- provisioning state
- initial Intune synchronization and normalized device context

## Phase 3 — Service Desk & Knowledge
- employee incident creation with user/device detection
- ticket queues/SLA concepts
- known incidents/major incidents
- knowledge articles/known errors/runbooks
- Autotask integration

## Phase 4 — Infrastructure & Operations Intelligence
- Intune Assignment Intelligence: normalized artifacts/assignments/filters, Directory Group/User/Device views, Assigned / Expected Applicable / Observed separation
- "Why is this assigned?" paths, reverse lookup, assignment history and Device/Directory-Group comparison
- sites/buildings/rooms/racks
- servers/switches/VMs/dependencies
- network/IP address documentation or NetBox integration
- changes/initiatives/maintenance calendar
- software normalization, vulnerability correlation, IT Briefing

## Phase 5 — Endpoint Operations
- Endpoint Agent inventory
- desired software state
- WinGet/other provider deployments
- remediation/diagnostics
- remote support only after a separate security design and production experience with the agent
