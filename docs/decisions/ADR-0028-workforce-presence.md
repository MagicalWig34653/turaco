# ADR-0028: Workforce Presence for Operational Availability

- Status: Accepted (2026-10-03). Planned capability; nothing is implemented.

## Context

IT teams plan who works where and who is reachable: on-site support needs someone in the office, a change needs its implementer present in the maintenance window, a ticket should not sit with someone on holiday. This information lives in HR systems, Outlook calendars and spreadsheets. Turaco needs it for operational decisions, not for personnel administration, and absence information is personal data that can reveal health data.

## Decision

1. Turaco gets a planned **Workforce Presence** domain (business module `presence`) for internal IT operational availability. It is **not HR management**: no leave requests or leave approval, no balances, no time tracking, no payroll data.
2. Concepts:
   - **Presence Entry** — a time-bound statement about one User: a planned work location (an organizational Location, `remote` or `travelling`) or `unavailable`, with optional recurrence, source, observed/synced time and visibility. Manual entries are `active → cancelled`; entries from external sources are kept as interval history like Directory Group memberships.
   - **Operational Availability** — derived per User and time: `available | limited | unavailable | unknown` (`limited`: working but not at the place or hours needed, for example remote when on-site support is required, or partially unavailable that day), with source and freshness.
   - **Team Coverage** — derived per Team and period from Team memberships valid in that period: available members against an optional minimum that Presence configures per Team.
3. Sources: entries entered in Turaco, HR integrations and Microsoft 365 (Outlook work location, free/busy, automatic replies). External entries keep source and freshness and are read-only in Turaco; Turaco never silently overwrites them. Real-time chat presence (Teams "available/busy") is out of scope initially.
4. Privacy by design (non-negotiable for the implementation):
   - **Turaco stores no absence reason**, only `unavailable`; connectors map every provider reason to it. Health or sickness data is never imported or stored.
   - Microsoft 365 ingestion reads free/busy, work location and out-of-office state only — never subject, body or attendees — and requests no mail- or calendar-content permissions.
   - Most viewers see only Operational Availability (for example "unavailable until Monday"). Users see their own entries. Viewing other Users' presence is scoped (for example own Teams) and needs a permission; detailed access is audited, as are manual edits by others.
   - Presence is opt-in per customer installation and requires a data protection impact assessment and, where applicable, works-council co-determination. It must not be used for performance or behaviour evaluation: no per-person history reports or exports.
   - Past entries are deleted after a retention period (default 30 days after the entry ended; configurable shorter).
5. Integrations reuse existing concepts: My Work shows own coverage and assignee availability; Tickets warn when assigning to an unavailable User (no automatic reassignment); Changes show implementer availability within a Maintenance Window; IT Briefing can show today's Team Coverage as an item referencing the read model. No parallel task, notification or calendar system is introduced.
6. Recurrence reuses the recurrence rule of Recurring Task Definitions. That rule is private to the Tasks module today, so it moves to a platform scheduling package before Presence uses it; Presence never imports Tasks internals.

## Consequences

- New module boundary row and glossary terms; the module reads Users, Teams, Team memberships and Locations through Organization's public contract.
- Which HR system is integrated first and the final retention default are open product decisions; a data protection review precedes the Microsoft 365 and HR adapters.
