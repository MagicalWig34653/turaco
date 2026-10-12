# ADR-0040: SLA Clocks Belong to the Service Desk, Time Tracking Is Its Own Module, Business Calendars Are a Platform Concept

- Status: Proposed (2026-10-12). Not implemented. Design: [F16](../product/f16-sla-time-tracking-design.md).

## Context

The product decided to offer SLA deadlines for Tickets and time tracking on Tickets, each optional and switchable per installation (ADR-0032). Three architectural questions follow:

1. SLA deadlines are measured in working time. Turaco has no business-hours or holiday calendar: `platform/scheduling` holds only the recurrence rule, a Change has one window, and Workforce Presence explicitly has no holiday calendar.
2. An SLA clock changes whenever the Ticket changes (wait, resume, resolve, reopen, priority, Queue), and its due dates must be filterable and sortable Ticket list fields with indexes. Module switches so far map one switch key to one code module.
3. Time entries are personal data about employees and technically suitable for performance monitoring (works-council co-determination in Germany). They are not part of Ticket transactions and will later attach to other work records.

## Decision

1. **Business Calendar is a core platform concept** in `platform/scheduling`: pure business-time arithmetic (no database, no clock) plus platform-owned storage (`platform.business_calendars`, `platform.business_calendar_closures`), permission `calendars.manage`, event `BusinessCalendarChanged`. It knows no consumer. No external holiday feed.
2. **SLA is a capability of the Service Desk module with its own switch key `sla`** (requires `servicedesk`, default off). Policies, Targets, clocks, the clock transition history and a due-date projection on `servicedesk.tickets` live in the `servicedesk` schema; clock transitions are made in the same transaction as the causing Ticket operation. A module switch key may name a capability inside an existing module only when its data cannot be separated from the owner's transactions; the catalog entry then owns its route prefix (`sla`), jobs and consumers (`sla.*`) like any module, and the owning module checks `Require("sla")` for the parts inside shared routes (Ticket detail fields, list fields, overview counts).
3. **SLA state is a separate dimension.** The Ticket status set is unchanged; clocks have their own explicit operations (start, pause, resume, complete, reopen-resume, cancel, retarget, exempt, and the system facts warned/breached). A recorded breach is never cleared.
4. **Time Tracking is a separate business module `timetracking`** (requires `servicedesk`, default off) with schema `timetracking`. Entries name a subject (`ticket` only in v1) and read Tickets only through `servicedesk/public`. Enabling is blocked until an administrator records the co-determination basis (`agreement` with a date, or `not_applicable`), the same precondition mechanism Presence uses. There are no per-person aggregates for anyone but the person themselves, reports suppress cells below a minimum number of contributors, and retention is bounded.
5. **No billing, rates, contracts or customer entitlements.** SLA Policies are per Ticket Queue and Priority.

## Alternatives considered

- *SLA as its own module reading Ticket events.* Rejected: clocks would be eventually consistent with the Ticket, and list filters and sorts on due dates would need a cross-module join, which the module rules forbid.
- *SLA fields directly on the Ticket row only.* Rejected: two clocks with about a dozen fields each, plus history, do not belong in the Ticket row; a narrow projection is kept there for indexes.
- *Business Calendar inside the Service Desk.* Rejected: Changes, Presence and Task due dates are likely readers, and moving it later would be a cross-module migration.
- *Time entries inside the Service Desk.* Rejected: different privacy profile and lifecycle, and future subjects outside Tickets.
- *A `breached` Ticket status.* Rejected: mixes two independent dimensions.

## Consequences

- One more platform table family and an elevated permission for calendars, even in installations without SLA.
- The module catalog gains two keys; `sla` is the first key whose code lives in another module, and the module-switch documentation must state the rule above.
- Ticket operations gain clock handling and a projection write; tests must cover every transition and the lock order Ticket → clocks.
- Time tracking needs operator documentation on co-determination and retention; per-person reporting requires a new decision.
