# ADR-0040: Location-Scoped Role Assignments

- Status: Accepted (2026-10-12) as product direction. Slices L-A (data model, evaluator, `RequireScoped`, harness), L-B (assignment operations, scope-aware guards, API), L-C (Location move guard and the L-B review gates), L-D (Tickets), L-E (Assets, reviewed), L-G (Tasks) and L-F part 1 (access UI) are implemented; L-D and L-G await their independent security review. `assets.view`, `assets.manage`, `tickets.view`, `tickets.manage`, `tasks.view` and `tasks.manage` are scopable; see [current status](../product/current-status.md).
- Related: [F16 design](../product/f16-location-scoped-access-design.md), [identity and access design](../security/identity-access-design.md) (E3: scope `global` only "until the first scoped module"), [F14 design](../product/f14-administration-design.md) sections 2.3 to 2.5 (guards), [F13 design](../product/f13-workbench-views-design.md) (Queue grants, row-specific disclosure), [ADR-0033](ADR-0033-workbench-views-query-engine.md), [ADR-0034](ADR-0034-local-accounts-and-external-parties.md).

## Context

Every Role Assignment is global today (`platform.role_assignments.scope` has `CHECK (scope = 'global')` since migration 000010). Multi-site organizations (the hospital simulation has one `it-site-lead` per Site) need staff who manage Tickets and Assets of their own Site without seeing other Sites. The product owner decided: rights are scoped to Sites through role assignments that carry a Location scope, and a scoped role applies only within that Location and its sub-Locations (the Organization Location tree, at most four levels).

Constraints: `platform/authorization` must not import modules; modules must not read another module's tables; Queue grants (F13) already give per-Queue access; `Principal.Has` is called in hundreds of places that assume a global meaning.

## Decision

1. **Scope lives on the Role Assignment, not on the Role.** An assignment is either `global` or `location` with one `scope_location_id`. A scoped assignment applies to that Location and all its descendants in the *current* tree. Roles, templates and the permission picker stay unchanged.
2. **Scope is a property of a permission in the registry.** Each registered permission is `global` (default) or `location` (scopable). Only scopable permissions take effect through a scoped assignment; the global-only permissions of a scoped role are inert and reported as such. A permission becomes scopable only in the same change in which its owning module enforces scope everywhere it is used. High-risk permissions are never scopable.
3. **Fail closed by construction.** `Principal.Permissions` and `Has` keep their meaning: permissions held *globally*. Scoped holdings are a separate field (`Principal.Scopes`) read only through `ScopeOf(permission)`. A route admits scoped holders only through a new `RequireScoped` middleware that panics at startup for a non-scopable permission. Code that has not adopted scope therefore never sees a scoped right.
4. **Subtrees are expanded at evaluation time into a set of Location ids** through an `organization/public` contract (`LocationTree.SubtreeIDs`). Modules filter with `location_id = ANY($ids)` on their own columns. No closure table or materialized path is shared across modules, no object stores an ancestor path, and Location moves take effect on the next request.
5. **Objects without a Location are visible to global holders only** (deny by default). A caller who holds a permission only scoped cannot create or move an object to a Location outside their scope, or to no Location.
6. **Queue grants compose by union.** User and Team Queue grants stay whole-Queue grants. A Queue grant to a role subject applies only within the scope of the assignment through which the caller holds that role. Scoped `tickets.view`/`tickets.manage` act as `view`/`work` in every Queue for Tickets located in scope. The own-ticket and participant rules are unchanged.
7. **Guards respect scope.** The grant ceiling, removal ceiling, role-holding rule and dominance rule compare scopes: an actor covers a scoped grant at Location L only if they hold each permission globally or at L or an ancestor of L (for global-only permissions: globally). The built-in `platform-administrator` role is always global, and external accounts receive no scoped assignment in this version.
8. **Enforcement is central where it can be and per-module where it must be.** Platform owns the data model, the evaluator, `Principal.Scopes`, the `LocationScope` predicate helper, `RequireScoped`, the guards and a shared adversarial test harness. Each adopting module composes the scope predicate into its one existing access function and its query-engine `Visibility` fragment, because only the module knows which column carries an object's Location and how scope combines with its other grants (for example Queue grants).
9. **Migration is backward compatible.** Existing assignments stay `global` with no Location; nothing changes for current installations until an administrator creates a scoped assignment.

## Alternatives rejected

- **Scope on the Role** ("IT Site Nord" as its own role): multiplies roles per Site, and template drift becomes per-Site work.
- **A generic resource-scope or ABAC policy engine**: a parallel permission system the constitution prohibits; the need is one dimension (Location).
- **Materialized path or closure table read by every module**: either cross-module table access or a denormalized path per object that goes stale on Location moves.
- **Making `Has` scope-aware**: every unaudited call site would silently treat a Site-scoped right as global.
- **Using Team membership as the Site boundary**: Teams are not Locations, Team management is delegated (A16) and would become an access side door.

## Consequences

- One new column, one widened check and a replaced unique index on `platform.role_assignments`; one new `organization/public` contract.
- Moving a Location becomes access-relevant: it is audited with the affected scoped assignments and needs a platform administrator when it changes the subtree of a scoped assignment.
- Each module that wants Site scoping must adopt it explicitly (registry flag, predicate, adversarial tests); until then its permissions are global-only. Wave 1 is Tickets and Assets, then Tasks.
- `platform.audit.view`, role administration, configuration and catalog permissions stay global-only.
- The effective-permissions read, the holders read, `/me` and the frontend permission gates must show and use scope.

## Implementation notes (slice L-A)

Details settled while implementing the evaluator and model; the [F16 design](../product/f16-location-scoped-access-design.md) section "Slice L-A as implemented" has the full list.

- Migration `000096` (not `000094` as first planned). The built-in administrator role is refused a Location scope by trigger (SQLSTATE `ACC02`).
- The Location tree contract is `roles.LocationTree.SubtreeIDs`, embedded in `roles.GroupResolver` and implemented by `organization/public.AuthorizationSubjects`, so every evaluator has it by construction. It is called once per distinct set of scoped roots and only when scoped holdings exist.
- Every pre-scope read stays global-only, beyond `Has`: `Evaluator.Permissions`, `RoleIDs` (role-subject Queue grants), the guards' holdings and the AI/MCP callers. A scoped role is therefore invisible to code that has not adopted scope, including Service Desk's Queue grants until L-D moves them to `RoleGrants`.
- Scoped assignments of external accounts and of the built-in administrator role, an unknown scope value, and a Location that does not exist yield nothing effective (fail closed).
- `LocationScope.SQL` uses the query engine's `?` placeholders and `?::text[]::uuid[]`.
- The open questions of the F16 design take their stated defaults until decided otherwise.

## Implementation notes (slice L-B)

Full list in the F16 design section "Slice L-B as implemented".

- No migration. The scope Location is validated (exists, active) through a second `organization/public` read, `SubjectDirectory.LocationPaths`, which also names the Location and yields its ancestors for holders by Location.
- The guards' holdings carry expanded scoped sets per permission and per role; ceilings and the role-holding rule compare at the assignment's scope, dominance compares the target's scoped holdings Location by Location, and separation-of-duties checks use the union. Role permission edits keep the global comparison (point 7 above).
- Location names in responses need `organization.view`; without it a response carries the scope Location id only.
- Fail-closed caps: more than 1000 scoped roots per permission or role are cut (in id order) and logged instead of failing the session; a Location walk that reaches its depth bound is logged as corrupt data.
- `Principal.Scopes` stayed exported in L-B (archcheck rule only); L-C unexported it (see below).

## Implementation notes (slice L-C)

Full list in the F16 design section "Slice L-C as implemented".

- No migration. Organization's `MoveLocation` counts the active scoped assignments a move affects through `roles.Guards.ScopedAssignmentsAffectedBy` (Organization passes the Location ids: the moved subtree and the ancestors of the old or the new position that are not ancestors of both). A non-zero count needs a platform administrator or the CLI (403 `organization.location_move_requires_administrator`); `dryRun` (shared locks only) previews whether an administrator is required and the count, which only administrators see; a move of more than 10000 Locations answers 409 `organization.location_move_too_large`. The move is audited with `scopedAssignmentsAffected` and publishes `ScopedLocationMoved`, whose consumer notifies the other administrators.
- Scoped assignments and revocations take Organization's Location tree lock in shared mode through `SubjectDirectory.LockLocationPath` before any guard evaluates coverage; moves, creations and (de)activations take it exclusively. A move therefore never races an in-flight scoped assignment.
- The guards validate the scope Location after the actor guards, refuse an assignment beyond `MaxScopedRoots` scope Locations (409 `access.scoped_roots_exceeded`), and treat a target's inert permissions as global holdings for dominance. Scoped assignment audit records snapshot the Location name and path; a replacement after expiry records who triggered it.
- `Principal`'s scoped map is unexported (`WithScopes`, `ScopeOf`, `ScopedPermissions`); archcheck restricts `WithScopes` and `Access.Scopes` to the evaluator, authentication and `scopetest`.
