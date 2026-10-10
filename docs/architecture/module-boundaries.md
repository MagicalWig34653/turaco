# Platform Services & Module Boundaries

**Status:** Accepted baseline

## Architecture style

The core is a modular monolith. Each domain owns its model, application operations, persistence and business rules. Other modules consume public contracts or events, never private repositories/tables.

## Platform services

`backend/internal/platform/` owns cross-cutting capabilities:
- authentication context and identity-provider abstraction (`platform/authentication`: sessions, directory, Kerberos, emergency and local credentials with single-use credential tokens per [ADR-0034](../decisions/ADR-0034-local-accounts-and-external-parties.md); password hashing and throttling);
- authorization (permissions + scopes; `platform/authorization/roles`: roles, assignments with expiry, built-in Role Templates as data, the evaluator and `Explain`, escalation guards and separation-of-duties rules);
- immutable audit;
- events/outbox/background job foundations;
- object/file storage abstraction;
- notifications/channels/templates/preferences;
- relationships (`platform/relationships`: registry of allowed triples registered by modules, bounded traversal; imports no module);
- global search contracts;
- secrets/encryption abstractions;
- tasks/My Work primitives, and the Work Item source contract (`platform/workitems`: a module contributes a source with `Items` and `Count` that authorizes every item; `GET /my-work/items` and `/my-work/counts` merge them with a composite keyset cursor and run a source only while its module is on);
- the one CSV writer (`platform/csvsafe`: formula and control-character neutralization and a UTF-8 byte order mark; modules never format CSV cells themselves);
- scheduling (`platform/scheduling`: the shared recurrence rule used by Tasks and Presence) and maintenance windows (a Maintenance Window is the window of a Change);
- targeting/dynamic groups;
- configuration and feature flags;
- module registry ([ADR-0032](../decisions/ADR-0032-module-switches.md), `backend/internal/platform/modules`, schema table `platform.module_switches`): catalog of modules with core/optional, dependencies, route prefixes and startup gates; the runtime switch per optional module, the HTTP gate (404 `platform.module_disabled`) and the job gate. It imports no module; module preconditions are supplied by the composition root (`internal/wiring/modules.go`);
- query engine ([ADR-0033](../decisions/ADR-0033-workbench-views-query-engine.md), `backend/internal/platform/query`): shared Filter AST validation, bounded SQL compilation, signed keyset cursors, capped counts and rate/timeout controls. It imports no business module and does not own module tables. Tickets, Devices and Tasks declare catalogs in their own application packages, run predicates in their own repositories, and supply mandatory visibility predicates. The composition root validates catalog columns against the migrated database at startup and shares the cursor/rate-limit engine;
- saved views ([ADR-0033](../decisions/ADR-0033-workbench-views-query-engine.md), `backend/internal/platform/views`, schema `views`): Saved Views, Shares, Pins and Pin Rules with audit and events. It imports no business module and reads no module table. Modules enter through three ports filled in `internal/wiring/views.go`: the Runner (the owning module's own `/fields` and `/query` endpoints, called in process with the viewer's session cookie, so the module's permission, scope and redaction apply), the Directory (Organization's public contracts for Teams, Directory Groups and Users) and the module gate (ADR-0032). It reads `platform.role_assignments` and `platform.roles` read-only to resolve a viewer's roles, as the permission evaluator does; It also exposes an embedding contract (`Resolve`, `ResolveMany`, `Degrade`, `PinAnnotator`) so a module that builds on a View (Task Boards in `tasks`, schema `tasks`: `boards`, `board_columns`, `board_card_ranks`) gets the viewer's access without reading `views` tables;
- localization and shared API/error conventions;
- Turaco AI runtime ([ADR-0029](../decisions/ADR-0029-turaco-ai.md), [F12 design](../product/f12-turaco-ai-design.md); read-only slice A-A implemented as `backend/internal/platform/ai`, schema `ai`): the provider port with its adapters (`providers/fake`, `providers/openaicompat`) and the dedicated validating provider transport (`safehttp`), the AI Tool registry, the session-bound conversation runtime, caps and usage, and the AI settings. The assistant panel and administration UI (A-B) are implemented; AI Proposals follow with A-C. `platform/ai` owns no business data, imports no module and reads no module table (`make archcheck`). Modules contribute AI Tools from their own `public` package (`servicedesk/public`, `knowledge/public`, `endpoints/public`: `AITools(service)`), each calling that module's application service as the requesting User and returning a dedicated field-allowlisted DTO; the composition root (`internal/wiring/ai.go`) registers them. A tool's permission is the module's existing permission key; the generated [AI tool reference](../reference/ai-tools.md) lists tools, permissions, risk and data classes.

A domain may use these capabilities but must not reimplement its own alternative.

## Business modules

| Module | Owns |
|---|---|
| Organization | users (profile, lifecycle, field ownership, local-account state; the credentials themselves belong to `platform/authentication`), departments (tree), teams with leads, locations (tree of sites and areas), cost centers (not implemented), external Directory Groups and observed memberships; contributes catalogs to the query engine for `users`, `teams`, `locations` and `departments` (not Saved Views). External Parties are designed (F14 A-G), not implemented |
| Tasks (`tasks`, core) | tasks, recurring definitions, My Work reads and Task Boards (`tasks.boards`, columns, per-board ranks; a Board is a Saved View plus columns, and a card move calls the existing Task operation); contributes the Tasks catalog and a My Work source |
| Products | product, optional variant, manufacturer, categories |
| Catalog | requestable services/forms/eligibility |
| Requests | service request lifecycle and fulfillment coordination |
| Service Desk | incidents, major incidents, problems/known error state, Ticket Queues (key, prefix, counter, grants), the reference registry and Ticket moves between Queues; contributes System Views (`servicedesk/public.SystemViews`) and My Work sources (`servicedesk/public`) through platform contracts |
| Knowledge | articles, procedures, runbook definitions/executions |
| Assets | asset/device identity, lifecycle, assignment |
| Inventory | warehouse, storage location, stock, transactions, reservations, goods receipt |
| Procurement | procurement request, supplier, purchase order |
| Endpoint (`endpoints`) | Devices and observations, desired state, Target Sets and Deployment planning with Deployment Rings (F9 G2) and their execution (F9 G3: ring runs, Deployment Targets and Attempts, the deployment engine job; provider writes only through the `integrations/intune` Management Assignment Writer port), management identities/providers, normalized management artifacts/assignments/filters/applicability/observations, Endpoint Agent integration; software: normalized products/versions/aliases/install observations and Software Approval Status, version approvals and Software Package references ([ADR-0027](../decisions/ADR-0027-software-management-providers.md), implemented in F9 G1). Software is not a separate module (F6 design E2); a split needs an ADR. Catalog reads approved software through the `endpoints` public contract. Deployment planning reads Change windows through `changes/public` (zero read scope: reference, status, window), requests plan Approvals (subject `deployment`) and ring promotion Approvals (subject `deployment_ring`) through `approvals/public` and Asset locations through `assets/public`. |
| Security (`security`, F8a/F8b backend) | Security Advisories, affected criteria, Vulnerability Findings, risk acceptance, matching and review reminders; reads observations only through `endpoints/public`; owns no Endpoint data or patch deployment. `integrations/advisories` supplies normalized imports. Exposes permission-scoped reads and remediation operations under `/api/v1/security` and bounded `security/public` reads. Reuses shared Tasks and Relationships for remediation; Briefing aggregation is a later slice. |
| Infrastructure (`infrastructure`, implemented F7a backend) | buildings/rooms/racks/rack placements/VMs; Sites are Organization Locations referenced by id; Assets are referenced through the `assets` public contract; exposes `infrastructure/public.WhereIs` |
| Network/IPAM | optional native VLAN/prefix/IP/interface model; decision remains separate |
| Services (`services`, implemented F7b backend) | service ownership, criticality, status and dependencies (Relationships `Service DEPENDS_ON ...`); impact view; derives `VM RUNS_ON Asset` from Infrastructure's `VirtualMachineChanged`; uses `infrastructure/public`, `assets/public` and Organization contracts |
| Changes (`changes`, implemented F7c backend) | change lifecycle (draft to closed), risk, rollback plan, maintenance window, approval through Approvals (Approval subject `change`), execution Tasks (context type `change`), affected resources (Relationships `change AFFECTS ...`, owned by Changes), history, impact preview, notifications to the owners of affected Services and a reminder job; uses `services/public` (`Lookup`, `Impact`), `infrastructure/public`, `assets/public`, `approvals/public`, `tasks/public` and Organization contracts |
| Planning (`planning`, implemented F7d backend) | Initiatives (lifecycle, approval through Approvals with subject `initiative`), Milestones, included records (Relationships `initiative INCLUDES change\|task\|procurement_request\|service`, owned by Planning), progress summary and the maintenance calendar read model; uses `changes/public` (`Lookup`, `Calendar`), `tasks/public`, `procurement/public.Requests`, `services/public`, `approvals/public` and Organization contracts; exposes `planning/public` (`UpcomingMaintenance`, `DueMilestones`) for the F8 briefing |
| Briefing (F8c backend) | manual editorial Items and a computed, permission-filtered feed over owning modules' public read contracts; underlying entities remain authoritative and feed entries are not stored |
| Remote Access *([ADR-0026](../decisions/ADR-0026-remote-access-providers.md); R-A implemented, attended only)* | Remote Access Sessions, their transitions, launch handles (hashes only) and Device-to-peer mappings (`remoteaccess.peer_mappings`, own history table instead of platform external references because those keep a set-once external id); reads Devices (`endpoints/public.Devices`), Tickets (`servicedesk/public.Tickets`, identity, status, affected and reporting User only), asset holders (`assets/public`) and approvers through public contracts; providers only through `integrations/remoteaccess`. Unattended-access policy records and typed remote actions are not implemented |
| Workforce Presence (`presence`) *([ADR-0028](../decisions/ADR-0028-workforce-presence.md); backend and UI implemented, external sources not)* | Presence Entries, per-Team coverage minimums, Operational Availability and Team Coverage read models; reads Users, Teams and Locations through Organization's public contract; not HR |

## Integration modules

`backend/internal/integrations/` translates external systems into public domain/application contracts. Vendor SDK/types do not leak across the domain model.

Each provider kind has its own port that names the capabilities the owning domain needs; there is no shared generic provider or plugin framework. Planned ports: Remote Access Provider (owner Remote Access, HopToDesk, RustDesk and AnyDesk as first providers, ADR-0026), Software Management Provider (owner Endpoint, IntuneGet first, ADR-0027; the port `integrations/softwaremgmt` with Fake and NotConfigured exists since F9 G1), presence sources such as Microsoft 365 and HR systems (owner Workforce Presence, ADR-0028) and AI Providers (owner platform AI runtime, ADR-0029).

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
