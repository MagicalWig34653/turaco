# F5 Service Desk and Knowledge — Feature Design

**Status:** Draft 2026-10-03. Decisions S1–S3 were not answered; the proposals below were adopted as defaults (the user asked to continue autonomously) and can be revised. Describes the target design; [current status](current-status.md) is authoritative for what is implemented. Related: [employee incident workflow](../workflows/employee-incident.md), [Autotask integration](../integrations/autotask.md), [state machines](../domain/state-machines.md), [F4 design](f4-inventory-design.md).

## Open decisions

- **S1 Autotask scope.** The plan ends F5 with an Autotask adapter. A native REST/webhook client needs a tenant, credentials and a sandbox to be verified; none exist here. Proposal: F5 builds the *internal* side (external-reference mapping table, an outbound port with a fake adapter, idempotent inbound update handling and observable sync state) and leaves the HTTP client behind the port as an explicit, documented stub until credentials are available.
- **S2 Device recognition.** Transparent device detection and the Device Context Snapshot need device observations (Intune, agent), which arrive in F6. Proposal: F5 lets the employee choose among the Assets assigned to them ("my equipment", F4) or "no specific device", stores that as a relationship, and snapshots the Asset's own fields (product, serial, tag, status) at creation. Probabilistic detection is F6.
- **S3 Ticket and Incident as one record.** The data model describes Ticket plus Incident. Proposal: one `tickets.tickets` table with a `kind` (`incident`, later `service_question`), because the lifecycle is shared and a second table would only duplicate it. Major Incidents and Problems are separate records that tickets link to.

## Scope (after the decisions)

1. **Tickets (incidents):** employee creation with almost no fields (what is wrong, optional device, optional free text), staff creation on behalf of someone, queues = Teams, priority derived from simple routing rules, assignment, public and internal comments, waiting reasons, resolve/close/reopen with reasons, notifications through the existing service, My Work integration.
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
- Incident subscribers beyond the first 5000 are not notified; subscribing is not audited (a personal preference).
- Roles that already granted `tickets.view` now read all tickets and internal comments; review role assignments when upgrading.
- There is no rate limit or idempotency key on ticket creation.
