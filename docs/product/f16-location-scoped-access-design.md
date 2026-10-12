# F16 Location-Scoped Access — Feature Design

**Status:** Design (2026-10-12). Nothing in this document is implemented; [current status](current-status.md) is authoritative. Decision record: [ADR-0040](../decisions/ADR-0040-location-scoped-role-assignments.md).

**Product owner decision (fixed input):** rights are scoped to Sites through Role Assignments with a Location scope; a role applies only within a Location and its sub-Locations (the Location tree).

## Starting point (verified in the repository)

- `platform.role_assignments` (migration 000010, expiry 000067): `scope text NOT NULL DEFAULT 'global' CHECK (scope = 'global')`, partial unique index `(role_id, subject_type, subject_id, scope) WHERE revoked_at IS NULL`, subject `user` or `directory_group`. The evaluator (`authorization/roles/evaluator.go`, `grantsSQL`) and `Holders` filter `a.scope = 'global'`.
- `authorization.Principal` carries one flat `Permissions` set; `Has`/`HasAny`, `Require`/`RequireAny` gate routes. `Evaluator.RoleIDs` feeds role-subject Queue grants in Service Desk (`Memberships.RoleIDs`), and `Memberships.Permissions` checks that an assignee can still see a Ticket.
- Guards (`roles/guards.go`): grant ceiling, removal ceiling, role-holding rule, high-risk only by administrators, no self-assignment, dominance (R1), last administrator, local-account and external-ceiling exclusions in `reduce`.
- `organization.locations`: tree of kind `site` (root) and `area`, at most four levels, cycle and depth trigger, re-parenting through an Organization operation under an advisory lock; `organization.locations.manage` is `normal` risk.
- Objects with a Location: `servicedesk.tickets.affected_location_id` (snapshot of the affected person's primary Location, corrected by `POST /tickets/{id}/location`, partial index), `assets.assets.location_id` (no index), `infrastructure.buildings.site_location_id`, `inventory.warehouses.location_id`, presence entries, `organization.users.primary_location_id`. `platform.tasks` has no Location (context only).
- Query engine: modules pass a trusted `Visibility` fragment to `query.Run`; Saved Views, My Work, search (`platform/search` federation), AI tools and MCP all execute through module code as the caller.

## Decisions

### L1 Data model

Forward migration (next free number at implementation, currently `000094`):

- `ALTER TABLE platform.role_assignments` drop the `scope = 'global'` check; add `CHECK (scope IN ('global', 'location'))`.
- Add `scope_location_id uuid NULL` with `CHECK ((scope = 'location') = (scope_location_id IS NOT NULL))`. No foreign key into `organization` (cross-module, same rule as `queue_teams.team_id`); the role service validates the Location through `organization/public` (exists and active) inside the assignment transaction.
- Replace `role_assignments_active_unique` by a unique index on `(role_id, subject_type, subject_id, scope_location_id) NULLS NOT DISTINCT WHERE revoked_at IS NULL`. The same role may be assigned to one subject at several Locations and globally; effective rights are the union.
- Add `role_assignments_scope_location_idx ON (scope_location_id) WHERE revoked_at IS NULL AND scope_location_id IS NOT NULL` (holders at a Location, Location move check).
- Extend the trigger pattern of `role_assignments_admin_no_expiry`: the built-in administrator role is never location-scoped (defence in depth for the service check).
- Existing rows remain `scope = 'global'`, `scope_location_id IS NULL`. No data rewrite.

`platform.role_assignments` remains the only grant table. No scope column is added to roles, Queue grants, View shares or pin rules.

### L2 Subtree semantics

- A scoped assignment at Location L applies to L and every descendant in the **current** tree. Areas created below L later are included; a sub-tree moved away from L stops being included on the next request.
- Expansion happens in the evaluator, per request (the evaluator has no cache today and keeps none): it collects the distinct `scope_location_id`s of the caller's active scoped assignments and calls `LocationTree.SubtreeIDs(ctx, rootIDs) ([]string, error)`, a new `organization/public` contract implemented with a recursive CTE in Organization's own repository (depth at most four). The result per permission is a sorted, de-duplicated id set.
- Inactive Locations stay in the tree and in the expanded set (deactivation does not orphan existing objects); the admin UI flags scoped assignments on inactive Locations. New objects cannot be placed on inactive Locations (existing Organization rule).
- Path or closure storage is not part of the contract. Organization may add an internal closure table later if the CTE becomes slow; consumers only see id sets.

### L3 Registry: scopable and global-only permissions

`permissions.Permission` gains `Scope string` (`global` default, `location`). The generated [permission reference](../reference/permissions.md) gets a Scope column. Registry tests: no `high`-risk permission is scopable; every scopable permission is listed in an adoption table with the module test that proves enforcement (L9).

| Wave | Scopable permissions | Object Location |
| --- | --- | --- |
| 1 (slice L-D) | `tickets.view`, `tickets.manage` | `tickets.affected_location_id` |
| 1 (slice L-E) | `assets.view`, `assets.manage` | `assets.location_id` |
| 2 (slice L-G) | `tasks.view`, `tasks.manage` | new `platform.tasks.location_id` (L8) |
| later, each by its own slice | candidates: `requests.view`, `requests.manage`, `endpoints.view`, `infrastructure.view`, `infrastructure.manage`, `inventory.view`, `inventory.manage`, `presence.view_availability` | to be defined per module (open question 9) |

Global-only by nature: every `platform.*`, `modules.manage`, `ai.*`, `integrations.*`, `organization.*`, `servicedesk.queues.manage`, `views.*`, `catalog.manage`, `products.*`, `knowledge.*`, `briefing.*`, every `*.approve`, every `high`-risk permission (including `remote_access.start_attended`), `tasks.work` (already personal: own and Team Tasks).

### L4 Evaluator semantics

- `grantsSQL` loads `scope` and `scope_location_id` for all active, unexpired assignments (global and location).
- `reduce` returns two structures from the same pass: the **global set** (permissions from global assignments, exactly today's behaviour) and the **scoped map** `permission -> root Location ids`, filled only for permissions whose registry scope is `location` and that the global set does not already contain. Global-only permissions reached only through a scoped assignment go into `inert` (reported by Explain, never effective). Local-account and external-ceiling exclusions run before both (a scope never widens anything).
- `authorization.Principal` gains `Scopes map[string]LocationScope`. `LocationScope{Global bool; Locations []string}` with `Allows(locationID string) bool` (empty id: true only when `Global`), `Any() bool` and `SQL(column string) (string, []any)` producing `TRUE`, `FALSE` or `<column> = ANY(?::uuid[])` for a module's trusted fragment. `Principal.ScopeOf(p)` returns `Global: true` when `Has(p)`, otherwise the expanded scoped set (possibly empty).
- `Has`, `HasAny`, `Require` and `RequireAny` are unchanged and mean "held globally". `RequireScoped(auth, perms...)` admits callers with `Has` or a non-empty `ScopeOf` for any of the permissions; it panics at construction when a permission is not registered or not scopable.
- **Deny by default:** no assignment, an expired one, a revoked one, an unknown Location, an object without Location for a scoped-only caller, and any module that has not adopted scope all mean no access.
- `Evaluator.RoleIDs` is replaced by `RoleGrants(ctx, userID) ([]RoleScope, error)` (`RoleID`, `Global bool`, `Locations []string` expanded) so modules with role-subject grants can scope them (L6). `Memberships.Permissions` becomes `AccessOf(ctx, userID)` returning the global set and the scoped map of any User, so assignee checks respect scope.
- `/me` returns `permissions` (global, unchanged) and `scopedPermissions: {permission: [locationId]}` (expanded set capped at the Location count); the frontend gates navigation on either.
- `Explain` reports per permission `scope: global` or `locations: [{rootLocationId, grantedBy: [assignmentId]}]`, plus `inert` per scoped assignment. The test "Explain equals Permissions" extends to the scoped map.

### L5 Guards with scope

"Actor covers (p, L)" means: the actor holds p globally, or p is scopable and L is in the actor's expanded set for p (L at or below one of the actor's scoped roots, hence the whole subtree of L is covered). "Actor covers (p, global)" means the actor holds p globally.

- **Grant ceiling (`AssignRole` at scope S):** the actor covers every permission of the role at S, scopable or not (global-only permissions must be held globally even though they are inert under S, so a later registry change cannot turn an inert grant into an uncovered one). 403 `access.grant_exceeds_holder`.
- **Role-holding rule:** the actor holds the role through a global assignment or an assignment at S or an ancestor of S.
- **Removal ceiling (`RevokeAssignment`, expiry change):** the actor covers every permission of the role at the assignment's scope.
- **Role permission edits** (`SetRolePermissions`, `CreateRole`, `DeleteRole`) affect every assignment of the role in every scope, so they keep comparing against the actor's **global** set (today's behaviour; unchanged code path).
- **Dominance (R1, account operations):** the actor dominates the target when for every target holding (p, global) the actor covers (p, global) and for every (p, locations) the target's expanded set is a subset of the actor's. Administrators dominate everyone, as today.
- **High-risk only by administrators**, **no self-assignment**, **last administrator** unchanged. New data rules, refused also for the CLI: the administrator role is global only (409 `access.admin_scope_global`); a scoped assignment to an external account is refused (409 `access.external_scope_unsupported`); the Location must exist and be active at assignment time (422 `access.scope_location_invalid`).
- **Separation of duties:** evaluated over the union of global and scoped permissions (conservative; open question 13).

### L6 Composition with Queue grants (Service Desk)

A caller's Ticket access is the union of these paths, resolved once per operation in the existing `access` object:

| Path | Applies to |
| --- | --- |
| Own-ticket rule (reporter, affected person) and Ticket participants | that Ticket, unchanged |
| User or Team Queue grant | whole Queue, unchanged |
| Role Queue grant through a **global** role assignment | whole Queue, unchanged |
| Role Queue grant through a **scoped** role assignment at L | Tickets of that Queue with `affected_location_id` in subtree(L) |
| `tickets.view`/`tickets.manage` global | every Queue, unchanged |
| `tickets.view`/`tickets.manage` scoped at L | Tickets of every Queue with `affected_location_id` in subtree(L) |

The row predicate becomes `queue_id = ANY(:wholeViewQueues) OR affected_location_id = ANY(:scopedViewLocs) OR (queue_id, scoped pair) ... OR own-ticket`. Scoped role Queue grants compile to at most 20 `(queue_id = ? AND affected_location_id = ANY(?))` disjuncts per caller; more answers `query.too_complex` and is logged (a real installation has a handful).

**Know vs work.**

- *Know* (row-specific disclosure of Queue, current number, assignee, routing Team, affected Location, patient impact) follows the row: a Ticket visible through any non-own path discloses its Queue. Queue metadata (names, prefixes, Queue picker, `queue` filter field) is known to a scoped `tickets.view` holder for every active Queue, like a global holder; counts per Queue are computed with the same row predicate, so they only ever count Tickets in scope. A filter on `affected_location` outside the caller's scope matches nothing and is indistinguishable from an empty Location (no oracle).
- *Work* (`canWork`): `work` or higher through a path that covers the Ticket's Queue **and** Location. Internal comments, lifecycle, assignment and priority follow `canWork`.
- *Assignee validity:* an assignee must be able to view the Ticket under their own scope (`AccessOf`), otherwise 422 `tickets.assignee_cannot_view`. When a Ticket's Location changes, an assignee who loses view is cleared (same rule as `MoveToQueue`) and told.
- *Location change* (`POST /tickets/{id}/location`): needs `canWork` on the Ticket, and a scoped-only caller may only set a Location inside their scope (403 `access.location_out_of_scope`); clearing the Location is refused for scoped-only callers.
- *Move between Queues:* unchanged rules, evaluated with the scoped paths (work in source, create in target).
- *Create:* intake is unchanged (`create` grants and public Queues); a scoped-only staff member creating a Ticket for someone else must set a Location in scope.

### L7 Assets

`assets.view` scoped: list, query, count, detail, search provider, relationship labels and Device links (Endpoints reading Asset names through `assets/public`) return only Assets with `location_id` in scope. `assets.manage` scoped: mutations need the Asset in scope; create and move need the target Location in scope; Assets without a Location are global-holder only. Assignment of an Asset to a User or Team does not change its Location. References from an in-scope object (Ticket affected Asset) to an out-of-scope Asset render as the neutral label `asset.other_location` without name, tag or serial. Index `assets.assets (location_id, id DESC) WHERE location_id IS NOT NULL` is added in the adoption slice.

### L8 Tasks (wave 2)

`platform.tasks` gains `location_id uuid NULL`. A module that creates a Task for a context (Ticket, Asset, Change, Request fulfillment) passes the context's Location at creation; the creator may set or change it through an explicit `SetTaskLocation` operation (audited, version-guarded). Context-free Tasks without Location are visible to scoped `tasks.view` holders only when assigned to them or their Teams (the existing `tasks.work` rule). Propagation of later context Location changes is not automatic in this design (open question 8).

### L9 Where scope is enforced

| Surface | Mechanism | Owner |
| --- | --- | --- |
| Route admission | `RequireScoped` (startup panic for non-scopable permissions) | platform |
| List, query, count, keyset cursors | module `Visibility` fragment built from `LocationScope.SQL` | module |
| Detail, mutations, abilities | module access object with `LocationScope.Allows` (404 for invisible, 403 for visible but not workable, as today) | module |
| Saved Views, System Views, Boards | execute through the module query endpoint as the viewer (ADR-0033) | inherits |
| My Work sources | module sources reuse the module predicate | inherits |
| Global search / Cmd+K | `platform/search` passes the Principal; providers use the module predicate | inherits |
| Turaco AI and MCP tools | call module services as the requesting User (ADR-0029) | inherits |
| Notifications | recipients chosen by relation (assignee, reporter, watcher) are unchanged; recipients chosen by permission (for example all workers of a Queue) are filtered per recipient with `AccessOf` before the intent is written; opening the link re-authorizes | module |
| Audit log | `platform.audit.view` and export stay global-only; object timelines built from audit follow object access | platform / module |
| Reports and counts | no separate report module; sidebar counts, Saved View counts and overviews use the module predicate (constitution rule 28) | module |
| Manager overview | `GET /tickets/overview` moves to `RequireScoped(tickets.view, tickets.manage)` and uses the L6 predicate; `GET /tasks/overview` follows in wave 2 | module |
| Relationships and audit resolvers | label resolvers call the module's scope-aware read; out-of-scope targets resolve to a neutral label | module |
| CSV exports of module lists | same predicate as the list | module |

The query engine is not given a Location concept: only the module knows the column and how scope combines with its other grants. Centralization lives in one helper type, one middleware, the evaluator and a shared test harness (`authorization/scopetest`) that every adopting module must run.

### L10 Location moves are access-relevant

Re-parenting a Location changes who can see what. Organization's `MoveLocation` operation:

- calls `roles` through a new narrow contract `ScopedAssignmentsAffectedBy(ctx, tx, locationID) (int, error)` (active scoped assignments on the moved Location, its descendants, and the ancestors of its old and new position);
- when the count is non-zero, needs a platform administrator or the CLI (403 `organization.location_move_requires_administrator`); a dry-run response lists the count so the UI can warn first;
- audits `organization.location.moved` with old and new parent and `scopedAssignmentsAffected`, and notifies the other platform administrators (category `access`).

`organization.locations.manage` therefore cannot be used to pull another Site's area under one's own scoped Site. Creating a new area under a Location is not guarded (it is empty).

### L11 API

- `POST /role-assignments` body gains `scopeLocationId` (optional; absent means global). Assignment responses gain `scope` (`global` or `location`), `scopeLocationId`, `scopeLocationName` and `scopeLocationPath` (resolved names, `organization.view`).
- `GET /role-assignments` filter `scopeLocationId` (exact) and `scope`.
- `GET /users/{id}/effective-permissions` adds per permission `scope` and per scoped assignment `inertPermissions`.
- `GET /access/holders?permission=&locationId=` lists holders whose scope covers the Location (global holders plus scoped holders at the Location or an ancestor).
- `GET /me` adds `scopedPermissions`.
- OpenAPI and error codes are updated in the same slices.

### L12 Audit

- `authorization.role.assigned` and `authorization.role.assignment_revoked` metadata gain `scope` and `scopeLocationId`; expiry by `system:access-expiry` likewise.
- `organization.location.moved` gains `scopedAssignmentsAffected` (L10).
- A scope denial is not audited (consistent with other 403/404 answers), but the 404-for-invisible rule applies.
- `RoleChanged`/assignment events carry the scope so notifications to administrators can name the Location.

### L13 Performance

- Evaluator: one extra Organization query per request, only when the caller has scoped assignments; the subtree CTE is bounded by depth four. The expanded set is at most the number of Locations (hundreds in the largest known installation).
- Module predicates: `affected_location_id = ANY($1::uuid[])` uses `tickets_affected_location_idx`; Assets get `(location_id, id DESC)`. Composite `(queue_id, affected_location_id)` is added only if the load test shows a need.
- The load test (`docs/development/load-testing.md`) gets a scoped site lead persona with three Sites and 200 areas; acceptance: no p95 regression above 10 percent for Ticket query and My Work.

### L14 UI

- **Assignment dialog:** scope selector "All Locations" or "Location" with the Location tree picker (only Locations the actor covers for the role are selectable; the rest is shown disabled with the reason). When a scoped role contains global-only permissions, the dialog lists them as "has no effect at a Location" before saving. Default selection is none: the actor chooses explicitly.
- **Assignment lists** (role detail, User detail, Access page) show the scope as a Location path chip; a filter by Location.
- **Effective permissions view:** per permission "Everywhere" or the list of Location paths with the granting assignment; inert permissions shown separately; a "Location" filter answers "what may this person do at Site Nord".
- **Location detail:** "Who has rights here" (holders by Location) and a warning in the move dialog with the affected assignment count.
- **Frontend gates:** `hasPermission` keeps its global meaning; `hasPermissionAnywhere` is added for navigation of scope-adopting modules. All texts through i18n.

### L15 Test strategy

- **Evaluator unit and database tests:** global only (unchanged results for every existing fixture), scoped only, both, two scoped assignments of one role, Directory Group subject with scope, expired and revoked scoped assignments, inert global-only permissions, local-account and external exclusions with scope, Location moved between two calls, inactive Location, unknown Location id, randomized fixtures where Explain equals the evaluated set.
- **Guard matrix:** for each guard, actor global versus scoped at the same Location, at an ancestor, at a descendant, at a sibling, at another Site; global-only permission in the role; administrator role scoped (refused, also by the trigger); external subject scoped (refused); revocation by a narrower actor (refused); dominance with scoped targets.
- **Shared adversarial harness `authorization/scopetest`** run by every adopting module with fixtures Site A (with area A1), Site B, an unlocated object and a caller scoped at A: list, query, count, detail (404), by-reference lookup, search, My Work, overview counts, Saved View execution, AI tool, export, relationship labels, notification fan-out and every mutation must never touch B or the unlocated object; filters and sorts on Location, Queue and assignee must not reveal B (equal answers for "B with data" and "B empty"); keyset cursors from a global session replayed by the scoped caller do not leak; a cursor issued before a revocation returns nothing after it.
- **Composition:** role Queue grant through a scoped assignment; User Queue grant plus scoped `tickets.view` (union); assignee outside scope refused; Location change clears assignee; scoped worker setting a Location outside scope refused.
- **Fail-closed tests:** a module that has not adopted scope answers 403 for a caller with only scoped holdings; `RequireScoped` with a non-scopable permission panics; registry test for high-risk and adoption table.
- **Location move:** moving an area from Site B under Site A changes access on the next request; refused for a non-administrator when scoped assignments are affected; audit and notification written.
- **Migration test:** existing assignments keep identical effective permissions before and after.
- **Playwright smoke:** create a scoped assignment, sign in as the site lead, see only the Site's Tickets.

## Reused concepts

Role Assignment, Role, Role Template, the evaluator and its guards, Location tree and `organization/public`, Queue grants, query engine `Visibility`, Saved Views, My Work, `platform/search`, notifications, audit, `Explain`.

## New concepts

Location scope of a Role Assignment, permission registry scope (`global`/`location`), inert permission (a global-only permission reached only through a scoped assignment). Glossary entries are added with slice L-A.

## Slices

Each slice is one implementer; the order is a dependency chain with at most three in parallel.

| Slice | Content | Files (ownership) | Depends on |
| --- | --- | --- | --- |
| **L-A Evaluator and model** | Migration (L1), `Permission.Scope` registry field (all `global`), docgen Scope column, `LocationTree.SubtreeIDs` in `organization/public`, evaluator load and reduce (L4), `Principal.Scopes`, `LocationScope`, `RequireScoped`, `RoleGrants`, `/me` `scopedPermissions`, Explain, `authorization/scopetest` harness, glossary. No scopable permission yet, so no behaviour change. | `platform/authorization/**`, `platform/permissions`, `organization/public` (new contract), migration | — |
| **L-B Assignment operations and guards** | `AssignRole` with scope, guards (L5), data rules and trigger, audit (L12), holders by Location, list filters, effective-permissions API, OpenAPI, error codes | `platform/authorization/roles/**` (service, guards, access, transport), `docs/reference` | L-A |
| **L-C Location move guard** | `ScopedAssignmentsAffectedBy` contract, `MoveLocation` guard, dry run, audit, administrator notification | `modules/organization/**`, one read in `roles` | L-A (parallel to L-B; the `roles` read file is owned by L-C) |
| **L-D Service Desk adoption** | `tickets.view`/`tickets.manage` scopable; L6 composition in `access`, predicates, detail and mutations, assignee and Location rules, overview, search provider, My Work source, notification fan-out, harness run | `modules/servicedesk/**`, registry line | L-B |
| **L-E Assets adoption** | `assets.view`/`assets.manage` scopable; L7, index migration, `assets/public` reads, neutral labels, harness run | `modules/assets/**`, registry line, migration | L-B (parallel to L-D) |
| **L-F Access UI** | Assignment dialog scope picker, lists, effective-permissions view, Location detail holders and move warning, `hasPermissionAnywhere`, i18n, Playwright smoke | `frontend/src/modules/access/**`, `frontend/src/modules/organization/**` (Location detail), `frontend/src/modules/auth` (permission gates) | L-B, L-C (parallel to L-D and L-E) |
| **L-G Tasks adoption** | `platform.tasks.location_id`, creation from context in the creating modules, `SetTaskLocation`, `tasks.view`/`tasks.manage` scopable, tasks overview, harness run | `platform/workitems`, tasks module, call sites | L-D |
| **L-H Simulation, load test and docs** | Hospital seed assigns `it-site-lead` scoped per Site, load-test persona, operator guide, current-status | `backend/cmd/turaco-admin` seed, `docs/**` | L-D, L-E |

L-A and L-B need an Opus security review before merge; L-D and L-E need the adversarial harness green and a security review.

## Open questions

1. **Inert permissions:** a scoped assignment of a role that contains global-only permissions is allowed with a warning (default) — or refused so that roles intended for Sites are kept pure?
2. **Unlocated objects:** invisible to scoped-only holders (default) — or a shared "no Location" pool visible to every scoped holder of the permission?
3. **Queue knowledge of scoped holders:** all active Queues (default) — or only Queues where they hold a grant or in which in-scope Tickets exist?
4. **Cross-Site handover:** a scoped-only worker cannot set a Ticket's Location outside their scope (default). Is a "hand over to Site" operation (Location change plus unassign, without keeping access) wanted?
5. **Delegated access administration:** should a site lead assign roles within their Site? That needs a new non-high-risk permission (for example `platform.roles.assign_scoped`) that is itself scopable; out of this design.
6. **Scoped remote support:** `remote_access.start_attended` is high-risk and therefore global-only. Site-bound remote support would need a separate decision.
7. **Site-scoped audit reading** for site leads: audit events carry no Location; not in this design.
8. **Task Location propagation:** should a Task follow later Location changes of its context record (event-driven) or stay a snapshot (default)?
9. **Wave 2 Location sources:** Requests (requested-for User's primary Location snapshot?), Devices (linked Asset's Location?), Infrastructure (Building Site), Inventory (Warehouse Location), Presence (User primary Location).
10. **Ticket Location source:** only `affected_location_id` (default), or also the affected Asset's Location when the person has none?
11. **Organization data:** scoping `organization.users.manage` to Users whose primary Location is in scope (site-local account administration) is not in this design.
12. **Location moves:** require a platform administrator when scoped assignments are affected (default), or block such moves entirely and require revoke and re-assign?
13. **Separation of duties per scope:** warnings computed over the union (default) or only where scopes overlap?
14. **Inactive Locations:** scoped assignments on a deactivated Location stay effective and are flagged (default), or are revoked by an audited job?
