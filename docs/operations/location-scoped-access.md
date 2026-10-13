# Location-scoped access (operator guide)

Status: usable for Assets, Tickets and Tasks (F16 slices L-A to L-E, L-G and L-F part 1). Design and decisions: [F16 design](../product/f16-location-scoped-access-design.md), [ADR-0040](../decisions/ADR-0040-location-scoped-role-assignments.md). Everything not listed under "What is scoped" ignores scope and behaves as before.

## What it does

A role assignment can be limited to a **Location** (a Site or an area; it covers the Location and everything below it). A person who holds `assets.view` and `assets.manage` only through such an assignment sees and changes only Assets at those Locations; a person who holds `tickets.view` and `tickets.manage` only through such an assignment sees and works only Tickets whose **affected Location** is at those Locations, in every Queue. Existing assignments stay global; nothing changes until someone creates a scoped assignment.

## What is scoped

| Permission | Scoped to | Where it applies |
| --- | --- | --- |
| `assets.view`, `assets.manage` | the Asset's Location | Asset list, query and counts, lookup, detail, global search, create, update, lifecycle and moves |
| `tickets.view`, `tickets.manage` | the Ticket's affected Location | Ticket detail and every single-Ticket operation, ticket query and counts, saved views, search, attachments |
| `tasks.view`, `tasks.manage` | the Task's Location (set at creation or with "set location"; callback and SLA escalation Tasks take the Ticket's affected Location) | Task detail, list, saved views, board cards and the manager operations |

Not scoped (a holder who has these only at a Location gets nothing from them): Queue grants through scoped roles, Problems and Major Incident ticket lists, the manager overview, the SLA report, My Work ticket sources, system views in the sidebar, AI and MCP ticket tools, the tasks overview and board administration, every other permission. Permissions that are not location-based inside a scoped role stay inactive; the effective-permissions view lists them as inert.

## How to use it

1. Create a role with the permissions the site lead needs, for example `assets.view`, `assets.manage`, `tickets.view`, `tickets.manage` and the global-only permissions they should hold elsewhere (those stay inactive when the role is scoped).
2. Assign it to a person under **Administration, Role assignments**: choose "Only at a Location (and its sub-Locations)" and pick the Site. The administrator role and Directory Group subjects cannot be scoped. A person cannot grant a scoped role more widely than they hold it themselves.
3. Check the result with the person's effective permissions: the assignment list shows where each role applies.

Objects without a Location (an Asset with no Location, a Ticket with no affected Location) are visible to global holders only.

## Moving Locations

Moving a Location changes who can see what below it. If the move affects any active scoped assignment, only a platform administrator may do it; the move dialog runs a dry run first and says so. The other administrators are notified, and the audit event carries the number of affected assignments.

## Limits and checks

- At most 1000 scoped Locations per person (an assignment that would exceed it is refused).
- Uniqueness of asset tags and serial numbers stays installation-wide, so a scoped manager can learn that a value exists elsewhere when saving a duplicate.
- Audit and role administration stay global-only.
- Run the adversarial scope tests with `go test ./backend/internal/wiring -run 'LocationScope'`.
