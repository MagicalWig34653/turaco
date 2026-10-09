# F5 Service Desk and Knowledge — Feature Design

**Status:** Draft 2026-10-03. Decisions S1–S3 were not answered; the proposals below were adopted as defaults (the user asked to continue autonomously) and can be revised. Describes the target design; [current status](current-status.md) is authoritative for what is implemented. Related: [employee incident workflow](../workflows/employee-incident.md), [Autotask integration](../integrations/autotask.md), [state machines](../domain/state-machines.md), [F4 design](f4-inventory-design.md).

## Open decisions

- **S1 Autotask scope.** The plan ends F5 with an Autotask adapter. A native REST/webhook client needs a tenant, credentials and a sandbox to be verified; none exist here. Proposal: F5 builds the *internal* side (external-reference mapping table, an outbound port with a fake adapter, idempotent inbound update handling and observable sync state) and leaves the HTTP client behind the port as an explicit, documented stub until credentials are available.
- **S2 Device recognition.** Transparent device detection and the Device Context Snapshot need device observations (Intune, agent), which arrive in F6. Proposal: F5 lets the employee choose among the Assets assigned to them ("my equipment", F4) or "no specific device", stores that as a relationship, and snapshots the Asset's own fields (product, serial, tag, status) at creation. Probabilistic detection is F6.
- **S3 Ticket and Incident as one record.** The data model describes Ticket plus Incident. Proposal: one `tickets.tickets` table with a `kind` (`incident`, later `service_question`), because the lifecycle is shared and a second table would only duplicate it. Major Incidents and Problems are separate records that tickets link to.

## Scope (after the decisions)

1. **Tickets (incidents):** employee creation with almost no fields (what is wrong, optional device, optional free text), staff creation on behalf of someone, queues = Teams (replaced by Ticket Queues in F13 Q-C, see the [F13 design](f13-workbench-views-design.md)), priority derived from simple routing rules, assignment, public and internal comments, waiting reasons, resolve/close/reopen with reasons, notifications through the existing service, My Work integration.
2. **Known issues:** Major Incidents (lifecycle `identified → … → closed`), employee-facing banner and duplicate suppression with subscription instead of a new ticket.
3. **Knowledge:** Articles (`draft → published → retired`), visibility internal/employee, search, contextual suggestions while writing a ticket, draft suggestion from a resolved ticket.
4. **Problems and Known Errors:** Problem lifecycle with Known Error data (cause, workaround), linking tickets.
5. **Runbooks:** definitions with ordered steps that are instantiated as Tasks (reusing F2/F3 task creation) and tracked.
6. **Autotask:** per S1.

Each slice follows the F3/F4 pattern: design section, migration, application with explicit operations, audit, events, API, UI, tests, docs.

## Reused concepts

User, Team, Task/My Work, Notification, Approval (for runbook steps later), Asset (device choice), Audit, outbox events, permissions, i18n, the module and public-contract rules.

## New concepts

Ticket, Comment, Queue (a Team used for routing, no new table), Major Incident, Problem/Known Error, Knowledge Article, Runbook (definition and execution), External Reference.

## Modules and permissions

`servicedesk` (tickets, major incidents, problems), `knowledge` (articles, runbooks), `integrations/autotask` (adapter; platform must not import it). Permissions: `tickets.view`, `tickets.work`, `tickets.manage`, `majorincidents.manage`, `knowledge.view`, `knowledge.manage`, `runbooks.execute`. Employees need no role to create tickets or read their own (ownership rule, as for requests and equipment).

## Security notes

Ticket text is user-authored and untrusted (safetext, escaped rendering, no HTML). Internal comments are never returned to the reporter. Reporter and affected User are distinct fields. Autotask data keeps source and freshness and is never merged silently into platform-owned fields.

## ADR

None expected; the Autotask adapter follows the existing integration rules.

## Review outcomes and known limitations (part 1, 2026-10-03)

Fixed after the security review: a device can only be attached when the affected User holds it (staff included), so ticket handling cannot read arbitrary assets; a conversation is capped at 300 comments and the newest 500 comments or incident updates are returned; the queue and assignee are redacted in every employee response and cannot be probed through filters; linking a ticket to a Major Incident refuses closed tickets and tickets that belong to another incident and is audited on the ticket; following an incident takes no row lock; a published article's audience can only change through retire and publish; `tickets.manage` is now an elevated permission and also implies reading all tickets.

Accepted limitations:

- A ticket can be assigned to any active User; a User without ticket permissions would be notified but cannot open it. Assign only to people who work tickets.
- `majorincidents.manage` can link any open ticket to an incident without holding ticket permissions.
- Incident updates are fanned out to subscribers in chunks of 500 (each chunk continues in a follow-up event); subscribing is not audited (a personal preference).
- Roles that already granted `tickets.view` now read all tickets and internal comments; review role assignments when upgrading.
- There is no rate limit or idempotency key on ticket creation.

Database review fixes: a ticket links to one incident only (a repeated link is a no-op, moving it is refused); assigning the same values again changes nothing; visibility is checked before the version; canonical ids in audit and events; a "resolved" notification is dropped when the ticket was reopened meanwhile; suggestions match any word (`match=any`, ranked) while normal search needs all words; status and timestamp invariants are database constraints (migration 000034). Search returns one ranked page without a cursor.

## Part 2 status (2026-10-03)

Implemented: Problems and Known Errors, Runbooks with tracked executions, and the internal side of the Autotask integration (decision S1). Decision S3 holds: tickets and incidents are one `incident` kind. Not implemented: routing rules (staff set queue and priority by hand), the Autotask REST client and webhook, probabilistic device detection (F6).

### Review outcomes (part 2)

Fixed: a push that overlapped a change is detected in one statement and repeated through its own job; inbound events are claimed before they are applied; reopen, close and cancel are pushed; malformed ids on the sync state return 404; a duplicate external id fails permanently. Accepted: `problems.manage` also reads problems and linked ticket summaries; `knowledge.view` reads runbook executions; `runbooks.execute` effectively lets the executor create tasks for the teams named in a runbook (tasks are created by a system actor, correlated by `runbook:<execution>`); an execution whose tasks were all cancelled is reported as completed and does not return to running when a task is reopened; starting a runbook does not lock it against a concurrent deactivation; the inbound resolution text is English.

## Simulation round 3: history, duplicates, on-behalf and patient impact (2026-10-09)

- **Ticket history.** `GET /tickets/{id}/history` (people with view access in the Queue; others 404) derives the entries from the Ticket's audit events (no second record): `created`, `assigned`, `unassigned`, `reassigned`, `team_routed`, `status_changed`, `priority_changed`, `queue_moved` (which also clears the assignee when the assignee cannot see the target Queue) and `marked_duplicate`. Each entry has the actor, the time, the causing operation (`via`: for example `assigned`, `start`, `queue_moved`) and a reason (`POST .../assign` takes an optional `reason`; lifecycle reasons and move reason codes were already audited). Starting a ticket without an assignee assigns the actor (`via: start`); there is no automatic assignment by routing yet. Comments stay in their own list.
- **Duplicates.** `POST /tickets/{id}/duplicate` (`expectedVersion`, `duplicateOfTicketId`/`duplicateOfId` or `duplicateOfReference`, optional `reason`/`note`/`reasonCode`) is an explicit lifecycle operation: the duplicate moves from new/open/in progress/waiting to `cancelled` with statusReason `duplicate` and `duplicate_of_ticket_id`. Guards: work access in the duplicate's Queue, view access to the target (unknown and invisible are the same 404), the target is another ticket that is not cancelled and not itself a duplicate, and the marked ticket has no duplicates of its own (no chains; 409 `servicedesk.invalid_duplicate_target`). Audited as `servicedesk.ticket.marked_duplicate`, event `TicketStatusChanged`. The link is hidden from callers who cannot view the Ticket's Queue.
- **On behalf.** `affectedUserId` on `POST /tickets` was staff-only. Any active internal employee may now name another active internal employee (reporter stays the caller, affected is the colleague who then sees the ticket); external accounts neither raise tickets for others nor can be named; the audit event of creation carries `onBehalf`. The device must be one the affected person holds. People are found with `GET /people/lookup`.
- **Patient impact.** `impact` (`patient_care`, `blocked`, `impaired`, `request`; stored as `reported_impact` and returned as `impact`) and `patientImpact` on `POST /tickets` (report a problem); `patient_care` and `patientImpact: true` are the same signal, the other impacts never change the priority: an employee's ticket gets priority `high` instead of `normal` (a Queue default above high is kept, `urgent` and every other explicit priority stay staff decisions: an employee sending `priority` gets 403 as before). The flag is stored (`patient_impact`), shown to staff, filterable and in the creation audit metadata; staff who set a priority explicitly keep it.
- **Location.** `affected_location_id` snapshots the affected person's primary Location at creation (existing tickets were backfilled once) and drives the `location` ticket filter.

## Simulation round 4: abilities and shaped counts (2026-10-09)

- **Ticket abilities.** `GET /tickets/{id}` returns `abilities` (`comment`, `internalComment`, `assign`, `setPriority`, `transition`, `move`, `markDuplicate`), computed by `application.AbilitiesOf` from the same facts the operations enforce: the effective authority over the Ticket's Queue (global `tickets.view`/`tickets.manage` or a Queue grant, so a team that works its own Queue comments and assigns without any global ticket permission), the reporter/affected relation and the status (no comment, assignment or duplicate mark on a closed, cancelled or resolved Ticket). `allowedOperations` stays the list of lifecycle operations. The abilities drive the UI; every operation still authorizes by itself, and a test compares each ability with what the operation really answers.
- **No hidden counts.** `linkedTickets` of Major Incidents (list, detail) and Problems (list, detail) counts exactly the linked Tickets the reader may view (their Queue access, or reporter/affected), by the rule of `VisibleTickets`, computed in SQL by `TicketAccess.ViewScope` (all Queues, the viewable Queue ids and the user). A vendor-restricted reader therefore sees `0` where the list is empty. The briefing feed counts only for staff and stays unchanged.
