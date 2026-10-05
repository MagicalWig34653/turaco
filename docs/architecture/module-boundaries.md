# Platform Services & Module Boundaries

**Status:** Accepted baseline

## Architecture style

The core is a modular monolith. Each domain owns its model, application operations, persistence and business rules. Other modules consume public contracts or events, never private repositories/tables.

## Platform services

`backend/internal/platform/` owns cross-cutting capabilities:
- authentication context and identity-provider abstraction;
- authorization (permissions + scopes);
- immutable audit;
- events/outbox/background job foundations;
- object/file storage abstraction;
- notifications/channels/templates/preferences;
- relationships (`platform/relationships`: registry of allowed triples registered by modules, bounded traversal; imports no module);
- global search contracts;
- secrets/encryption abstractions;
- tasks/My Work primitives;
- scheduling and maintenance windows;
- targeting/dynamic groups;
- configuration and feature flags;
- localization and shared API/error conventions;
- *(planned, [ADR-0029](../decisions/ADR-0029-turaco-ai.md))* Turaco AI runtime: AI Provider connectors, the AI Tool registry and AI Proposals. Modules contribute AI Tools that call their own public application contracts; the AI runtime never reads module tables.

A domain may use these capabilities but must not reimplement its own alternative.

## Business modules

| Module | Owns |
|---|---|
| Organization | users, departments, teams, locations, cost centers, external Directory Groups and observed memberships |
| Products | product, optional variant, manufacturer, categories |
| Catalog | requestable services/forms/eligibility |
| Requests | service request lifecycle and fulfillment coordination |
| Service Desk | incidents, major incidents, problems/known error state |
| Knowledge | articles, procedures, runbook definitions/executions |
| Assets | asset/device identity, lifecycle, assignment |
| Inventory | warehouse, storage location, stock, transactions, reservations, goods receipt |
| Procurement | procurement request, supplier, purchase order |
| Endpoint (`endpoints`) | Devices and observations, desired state, deployments and *(planned)* Deployment Rings, management identities/providers, normalized management artifacts/assignments/filters/applicability/observations, Endpoint Agent integration; software: normalized products/versions/aliases/install observations and *(planned, [ADR-0027](../decisions/ADR-0027-software-management-providers.md))* Software Approval Status, version approvals and Software Package references. Software is not a separate module (F6 design E2); a split needs an ADR. Catalog reads approved software through the `endpoints` public contract. |
| Security (`security`, F8a/F8b backend) | Security Advisories, affected criteria, Vulnerability Findings, risk acceptance, matching and review reminders; reads observations only through `endpoints/public`; owns no Endpoint data or patch deployment. `integrations/advisories` supplies normalized imports. Exposes permission-scoped reads and remediation operations under `/api/v1/security` and bounded `security/public` reads. Reuses shared Tasks and Relationships for remediation; Briefing aggregation is a later slice. |
| Infrastructure (`infrastructure`, implemented F7a backend) | buildings/rooms/racks/rack placements/VMs; Sites are Organization Locations referenced by id; Assets are referenced through the `assets` public contract; exposes `infrastructure/public.WhereIs` |
| Network/IPAM | optional native VLAN/prefix/IP/interface model; decision remains separate |
| Services (`services`, implemented F7b backend) | service ownership, criticality, status and dependencies (Relationships `Service DEPENDS_ON ...`); impact view; derives `VM RUNS_ON Asset` from Infrastructure's `VirtualMachineChanged`; uses `infrastructure/public`, `assets/public` and Organization contracts |
| Changes (`changes`, implemented F7c backend) | change lifecycle (draft to closed), risk, rollback plan, maintenance window, approval through Approvals (Approval subject `change`), execution Tasks (context type `change`), affected resources (Relationships `change AFFECTS ...`, owned by Changes), history, impact preview, notifications to the owners of affected Services and a reminder job; uses `services/public` (`Lookup`, `Impact`), `infrastructure/public`, `assets/public`, `approvals/public`, `tasks/public` and Organization contracts |
| Planning (`planning`, implemented F7d backend) | Initiatives (lifecycle, approval through Approvals with subject `initiative`), Milestones, included records (Relationships `initiative INCLUDES change\|task\|procurement_request\|service`, owned by Planning), progress summary and the maintenance calendar read model; uses `changes/public` (`Lookup`, `Calendar`), `tasks/public`, `procurement/public.Requests`, `services/public`, `approvals/public` and Organization contracts; exposes `planning/public` (`UpcomingMaintenance`, `DueMilestones`) for the F8 briefing |
| Briefing (F8c backend) | manual editorial Items and a computed, permission-filtered feed over owning modules' public read contracts; underlying entities remain authoritative and feed entries are not stored |
| Remote Access *(planned, [ADR-0026](../decisions/ADR-0026-remote-access-providers.md))* | Remote Access Sessions, unattended-access policy records, provider device mapping (as platform external references); reads Devices, Tickets and Users through public contracts and runs remote actions only through the Endpoint public contract |
| Workforce Presence (`presence`) *(planned, [ADR-0028](../decisions/ADR-0028-workforce-presence.md))* | Presence Entries, per-Team coverage minimums, Operational Availability and Team Coverage read models; reads Users, Teams and Locations through Organization's public contract; not HR |

## Integration modules

`backend/internal/integrations/` translates external systems into public domain/application contracts. Vendor SDK/types do not leak across the domain model.

Each provider kind has its own port that names the capabilities the owning domain needs; there is no shared generic provider or plugin framework. Planned ports: Remote Access Provider (owner Remote Access, HopToDesk first, ADR-0026), Software Management Provider (owner Endpoint, IntuneGet first, ADR-0027), presence sources such as Microsoft 365 and HR systems (owner Workforce Presence, ADR-0028) and AI Providers (owner platform AI runtime, ADR-0029).

## Communication

Synchronous calls use a module's small public application contract when an immediate result is required. Asynchronous reactions use domain/integration events through the outbox.

Forbidden examples:
- Service Desk importing Inventory repository code;
- Endpoint querying Assets tables directly;
- an integration writing domain tables with raw SQL;
- frontend module A reaching into private state of module B.

## Transactions across owners

A public contract may take a caller-owned `pgx.Tx` only when an invariant spans owners and must commit atomically, for example session creation that locks the Organization User row (`UserLocker.LockActiveUser`), session revocation inside a status change (`authentication.RevokeUserSessions`) or creating an emergency User together with its credential. Rules:

- the caller owns the transaction (begin, commit, rollback); the callee never commits or keeps it;
- the callee touches only its own tables through it;
- lock order is fixed: Organization User row before platform sessions/credentials;
- such methods are named for the invariant, not generic data access, and are documented on the interface.

## Database ownership

One PostgreSQL database is acceptable, using schemas such as `platform`, `organization`, `products`, `assets`, `inventory`, `service_desk`, etc. Database co-location does **not** remove logical ownership. Cross-module foreign keys are deliberate, not automatic.

## Extraction

A module becomes an independent service only for a concrete reason such as different scaling, independent security/availability boundary, network placement or incompatible runtime. "Microservices are modern" is not a reason.
