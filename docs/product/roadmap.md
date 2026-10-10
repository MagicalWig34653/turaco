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

## Phase 5 — Endpoint Operations (provider-based)
Revised 2026-10-03: Turaco integrates specialist providers instead of building a patch engine or a remote-desktop transport.
- software lifecycle and patch orchestration: approved software, deployment rings, approvals and rollout state over IntuneGet → Intune ([ADR-0027](../decisions/ADR-0027-software-management-providers.md))
- remote access through a Remote Access Provider, HopToDesk, RustDesk and AnyDesk as first providers, with Turaco-owned authorization, audit and ticket/device context ([ADR-0026](../decisions/ADR-0026-remote-access-providers.md))
- remediation/diagnostics as typed operations

## Phase 6 — People and assistance
- Workforce Presence: operational availability and team coverage, not HR ([ADR-0028](../decisions/ADR-0028-workforce-presence.md))
- Turaco AI: provider-independent, tool-based, acting with the requesting user's permissions ([ADR-0029](../decisions/ADR-0029-turaco-ai.md))

## Phase 7 — Workbench and administration
- platform query engine, Saved Views, Ticket Queues, Task Boards ([ADR-0033](../decisions/ADR-0033-workbench-views-query-engine.md)); further resource catalogs follow module by module
- People and access administration: local accounts, role templates, effective permissions ([ADR-0034](../decisions/ADR-0034-local-accounts-and-external-parties.md)); health, setup checklist, audit UX and External Parties follow
- runtime module switches ([ADR-0032](../decisions/ADR-0032-module-switches.md))

## Later / optional
- Endpoint Agent inventory and typed operations
- native WinGet or remote-access providers, each only with its own ADR
