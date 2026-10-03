# F7 Infrastructure and Change — Feature Design

**Status:** Draft 2026-10-03, slice 1 (F7a backend) implemented; decisions I1–I5 adopted by default (the user asked for autonomous progress through F8). Target design; [current status](current-status.md) is authoritative for what is implemented. Related: [infrastructure change workflow](../workflows/infrastructure-change.md), [module boundaries](../architecture/module-boundaries.md), [core data model](../domain/core-data-model.md), [ADR-0030](../decisions/ADR-0030-network-ipam-integrate-not-rebuild.md), [F4 design](f4-inventory-design.md).

## Decisions

- **I1 Network/IPAM** is integrated, not rebuilt (ADR-0030). No VLAN/prefix/interface model in F7.
- **I2 Sites are Organization Locations.** A Site is an existing `organization.locations` row; Infrastructure adds Buildings, Rooms and Racks below it, referencing the Location by id (no foreign key, public lookup contract). No second site list.
- **I3 Physical devices are Assets.** A Rack Placement references an Asset by id (public contract in `assets`), with U position, height in U, face (`front|rear`) and a database exclusion of overlapping placements. Assets keep their own lifecycle; placing an Asset does not change its status.
- **I4 One shared Relationship mechanism.** `platform/relationships` implements the generic `Relationship` of the core data model: `(source_type, source_id, type, target_type, target_id, valid_from, valid_until, created_by)` with a compile-time registry of allowed `(source_type, type, target_type)` triples, one current row per triple (partial unique index), bounded breadth-first traversal with cycle protection and depth/size caps. Domain-specific invariants (AssetAssignment, RackPlacement, ServiceDependency criticality) stay in their own tables; the relationship rows carry only informational links used for impact views and show their confidence (`declared` by a person, `derived` by Turaco, `observed` from an integration).
- **I5 Changes reuse Approvals, Tasks, Runbooks and Notifications.** A Change is its own lifecycle record; approval is an Approval subject, execution steps are Tasks (optionally from a Runbook Definition), communication is a Notification category. Maintenance windows are Change fields (start/end), not a scheduling subsystem; the calendar is a read model over Changes and maintenance windows.

## Slice 1 status

**F7a backend implemented** (module `infrastructure`, migration `000043_infrastructure.up.sql`; OpenAPI and frontend follow in the same slice):

- Sites are Organization Locations referenced by id (checked active at creation through `WorkDirectory`; no foreign key). Building (name unique per Site, bounded address note), Room (name unique per Building, floor) and Rack (name unique per Room, height 1-60 U, immutable) can be created, renamed/updated and archived/unarchived. Archiving needs the level below to be archived or empty (Rooms, Racks, active placements); unarchiving needs the parent to be active.
- Rack Placement: an Asset id (no foreign key; exists and is not `disposed`, `lost` or `retired`, checked through `assets/public`), `u_position`, `height_u`, face `front|rear`. Placing never changes the Asset. `PlaceAsset`, `MoveAsset` (closes the old placement with reason `moved` and inserts a new one that links back through `previous_placement_id`) and `RemoveAsset` (reason code `relocated|replaced|decommissioned|error_correction|other`) keep every placement as history (`removed_at`). Overlap protection without `btree_gist`: the Rack row is locked `FOR UPDATE` for every placement change and the table `rack_unit_occupancy` has one row per occupied unit with primary key `(rack_id, face, u)`, so a double occupancy is impossible even if the application check is bypassed; a partial unique index allows one active placement per Asset. A device occupying both faces is not modelled (an Asset has one placement).
- Virtual Machine: name (unique among live VMs), state `running|stopped|unknown|decommissioned`, optional hypervisor Asset id, vCPU 1-1024, memory 1-16777216 MB, validated `management_address` (IP or host name), bounded network note and notes. Explicit operations create, update details, change state (`running|stopped|unknown`), assign/clear hypervisor and decommission (reason code `retired|migrated|deleted|other`; terminal tombstone).
- Reads: lists with keyset pagination, rack detail with the active placements (rack elevation data, Asset references resolved), `GET /api/v1/infrastructure/tree` (per Site: Buildings with room/rack/placed-asset counts; Site names through the Organization contract), `GET /api/v1/assets/{id}/location` (needs `infrastructure.view|manage` and `assets.view|manage`; 404 for an unknown Asset, `{"placed": false}` for an unplaced one) and `infrastructure/public.WhereIs(assetID)`. The `VM RUNS_ON Asset` Relationship waits for `platform/relationships` (F7b); until then the hypervisor is a column on the VM.
- Audit actions `infrastructure.building|room|rack.created|renamed|updated|archived|unarchived`, `infrastructure.placement.placed|moved|removed`, `infrastructure.vm.created|updated|state_changed|hypervisor_changed|decommissioned`, written in the mutation's transaction with ids, states and reason codes only (no names, addresses or notes). Events `RackPlacementChanged` and `VirtualMachineChanged` through the outbox.

### Slice 1 review outcomes

Fixed:

- **Asset disclosure.** Placement responses (rack detail, `GET /racks/{id}/placements`, `GET/POST /rack-placements...`, move/remove) omit `assetId`, `assetReference` and the `assetReferences` map unless the caller also has `assets.view` or `assets.manage`; they then show the units as occupied (position, height, face). The UI renders "occupied" without a link.
- **Hypervisor lookup errors.** Only `ErrAssetUnusable` becomes `ErrReferenceInvalid` (400); a failing Assets lookup is a 500 (logged). Hypervisor ids are lower-cased on create as on assign.
- **Move vs. disposed Assets.** `MoveAsset` checks that the Asset is usable before the transaction and rack locks (no cross-module call under lock). The check-to-commit window is accepted (TOCTOU): a move may still commit for an Asset disposed in that window. An Asset disposed while placed keeps its units until its placement is removed; `GET /api/v1/infrastructure/placement-warnings` (`infrastructure.view` and `assets.view`) lists active placements whose Asset is disposed, lost, retired or missing, scanning at most 5000 active placements in id order in batches of 500 through the Assets batch lookup and returning at most 100 (`truncated` says when a limit stopped the check). The site tree screen shows the list.
- **`expectedVersion` is required** (400 `infrastructure.invalid_request` when missing) on move, remove, VM state, hypervisor, decommission and all archive/unarchive operations. A retried remove or decommission with the same reason and the version from before the first call returns the current record without a second audit entry or event; a different reason is still refused.
- **Database invariants.** Occupancy rows reference `(placement_id, rack_id, face)` of their placement (`UNIQUE (id, rack_id, face)`), placements carry `rack_height_u` tied to the rack by `FOREIGN KEY (rack_id, rack_height_u) REFERENCES racks (id, height_u)` with `CHECK (u_position + height_u - 1 <= rack_height_u)`, so fit is enforced by the database and a rack's height cannot change under placements. Triggers back up the application rules: no active placement on an archived rack, no active rack in an archived room, no active room in an archived building, and no archive over active children (parent rows are read `FOR SHARE`). A test asserts that occupancy rows per active placement equal `height_u` and that closed placements occupy nothing.
- **Indexes.** `rack_placements` has a partial `(rack_id, id) WHERE removed_at IS NULL` index for active reads and a full `(rack_id, id)` index for the history listing; the unused `(asset_id, id)` index is dropped (the partial unique index serves active lookups).
- **Tree.** Counts are `GROUP BY` aggregates over the selected Sites. The tree returns at most 500 Sites and 10000 Buildings and sets `truncated` instead of cutting silently.

Accepted:

- `ListVMs` name search is an unindexed `ILIKE` scan: VMs are hand-entered and few; `pg_trgm` is not installed and not worth a dependency now.
- VM `hypervisorAssetId` stays visible to `infrastructure.view` without `assets.view` (a VM property, no Asset data beyond the id).
- Disposing a placed Asset is still not blocked; the warning list is the safety net.

## Slices

1. **Topology (F7a, module `infrastructure`):** Buildings, Rooms, Racks (height in U), Rack Placements, Virtual Machines (name, state, optional hypervisor Asset, vCPU/memory, `management_address`, notes, optional Service link later), `VM RUNS_ON Asset` relationship, audit, UI (site tree, rack elevation list view, VM list/detail), assets get a "Where is it?" lookup (rack/room/building/site) in the Asset detail via the public contract.
2. **Services and impact (F7b, modules `services` + `platform/relationships`):** Service (owner User/Team, support Team, criticality, status), Service dependencies and links to Assets/VMs/Sites via Relationships, impact traversal API ("what is affected if X is down", with depth, confidence and truncation flags), service view with dependency lists. Tickets may later reference Services by id (reference only, not part of F7).
3. **Changes (F7c, module `changes`):** Change lifecycle `draft → submitted → approved → scheduled → in_progress → completed | failed | cancelled → reviewed` with explicit operations, risk level, rollback plan, maintenance window, affected resources (Relationships `Change AFFECTS …`), approval through Approvals according to risk (`low` none, `medium/high` approver required, requester excluded), execution Tasks (optional Runbook), notifications to owners of affected Services, audit, UI.
4. **Planning and calendar (F7d, module `planning`):** Initiatives (title, goal, owner, status `planned|active|completed|cancelled`) group Changes, Tasks and Procurement Requests by Relationship (no copies); milestones as dated entries; maintenance calendar read model (`GET /api/v1/maintenance-calendar?from&to`) over scheduled Changes with affected Services/Sites; impact view per Change (uses slice 2 traversal); briefing integration is F8 (calendar items are exposed so F8 can aggregate them).

## Reused concepts

Location, User, Team (Organization); Asset (public contract); Approval, Task, Runbook (Knowledge), Notification, Audit, outbox events, permissions, i18n, `platform/externalrefs` pattern for provenance where needed.

## New concepts

Building, Room, Rack, Rack Placement, Virtual Machine, Service, Relationship (generic), Change, Maintenance Window, Initiative, Milestone. All appear in the glossary or are added with the slice.

## Permissions (planned)

`infrastructure.view|manage`, `services.view|manage`, `changes.view|manage|approve` (approve is not a global permission; approvals use the existing Approval deciders) and `changes.execute`, `planning.view|manage`. Employees see nothing here except future service status banners (out of scope). `manage` permissions are elevated where they can affect other people's scheduling.

## Audit, events, jobs

Every mutation audited in the same transaction (ids, states, reason codes; no free text). Events: `RackPlacementChanged`, `ServiceCreated`, `ChangeSubmitted`, `ChangeApproved`, `ChangeScheduled`, `ChangeStarted`, `ChangeCompleted`, `ChangeFailed`, `InitiativeStatusChanged`. A job sends the "starts soon" reminder for scheduled Changes (idempotent per change and window).

## Security and privacy

Infrastructure topology and service dependencies are sensitive operational data: all reads need permissions (no employee access). Free text is user-authored and untrusted (safetext, escaped rendering). Impact traversal is bounded (depth 6, 500 nodes) and reports truncation instead of silent cut-offs. Approver separation of duties as in F3/F4 (requester and editors cannot approve).

## Documentation obligations

Current status, glossary, state machines (Change, Initiative), core data model, module boundaries table (Infrastructure/Services/Changes/Planning rows now implemented), workflows (`infrastructure-change.md` status), generated references, OpenAPI, this document per slice.

## Not in F7

Native IPAM/cabling/interfaces, NetBox integration, hypervisor integrations (VM data is entered by hand; an inventory provider port is later), automatic change risk scoring, CAB meeting workflow, service-level monitoring/uptime, ticket-to-service linking UI, capacity planning, floor plans.
