# F17 SLA Deadlines and Time Tracking — Feature Design

**Status:** S-A Business Calendars implemented; S-B through S-E and T-A through T-C remain design only. [Current status](current-status.md) is authoritative for what exists. Decision record: [ADR-0041](../decisions/ADR-0041-sla-clocks-time-tracking-business-calendars.md) (Proposed). Related: [F5 design](f5-service-desk-design.md) (Tickets, waiting reasons), [F13 design](f13-workbench-views-design.md) (Queues, query engine, My Work sources, manager overview), [module switches](module-switches-design.md) and [ADR-0032](../decisions/ADR-0032-module-switches.md), [F11 design](f11-workforce-presence-design.md) (privacy and works-council pattern), [state machines](../domain/state-machines.md#sla-clock-planned-f16), [glossary](../domain/glossary.md#service-management).

## Product decisions already taken

- **P1** Turaco gets **SLA deadlines** for Tickets and **time tracking** on Tickets.
- **P2** Both are **optional and independently switchable per installation** through the existing module switch (ADR-0032). Either can be on without the other.

## Decisions proposed by this design

- **D1 Business Calendar is a platform concept.** No business-hours or holiday calendar exists today (`platform/scheduling` holds only the pure recurrence rule; Changes have a single window; Presence explicitly has no holiday calendar). SLA needs one, and Changes, Presence and Task due dates are plausible later readers, so the calendar lives in `platform/scheduling` (pure arithmetic) with storage owned by the platform. It is core (always on) and has no consumer while SLA is off.
- **D2 SLA is a Service Desk capability with its own switch key `sla`.** The clocks change in the same transaction as the Ticket lifecycle operation that causes them, and the due dates are filterable, sortable Ticket list fields. Both need the Ticket's own schema; a separate module would have to read Ticket tables or be eventually consistent through events. The switch key `sla` (requires `servicedesk`) names a capability whose code and tables live in `modules/servicedesk`. ADR-0040 records this as the rule for when a switch key may name a capability instead of a code module.
- **D3 Time Tracking is its own business module `timetracking`.** Time entries do not take part in Ticket transactions, have a different privacy profile (personal data about employees) and will later attach to other work records. It reads Tickets only through `servicedesk/public`.
- **D4 The Ticket status does not change.** SLA state is a separate dimension (state-machines rule: independent concerns have independent state dimensions). No `breached` Ticket status, no generic status update.
- **D5 No per-person performance views by default.** No per-assignee SLA breach counts, no per-person time totals for anyone except the person themselves.

## Scope

1. Business Calendars: time zone, weekly working hours, full-day closures (holidays), admin UI.
2. SLA Policies: response and resolution targets per Priority, a calendar, pause rules; assigned per Ticket Queue.
3. SLA Clocks per Ticket (first response, resolution) as a state machine driven by explicit domain operations; warning and breach facts.
4. Warnings and breaches through notifications, My Work order, manager overview and an optional escalation Task.
5. Visibility: Ticket detail, Queue lists (query engine fields), manager overview, a minimal SLA report.
6. Time entries on Tickets: manual entries and a personal timer, edit and delete rules, audit, retention, aggregated reports without per-person breakdown.

## Out of scope (deliberate)

- **Billing, invoicing, rates, billable/non-billable flags, cost accounting, contracts, customer entitlements, contract-specific SLAs.** Turaco is an internal IT platform; SLAs are per Queue, not per customer contract.
- Operational Level Agreements between Teams, underpinning contracts with vendors, SLA per Service or per Location (open question O7).
- SLAs for Service Requests, Changes or Tasks (the clock model is Ticket-specific in v1; open question O8).
- Automatic reassignment or priority escalation on breach (only notifications and an optional Task).
- Working-time law checks, attendance, overtime, payroll export, per-person productivity dashboards, leaderboards.
- Time entries on records other than Tickets (open question O9), pushing time entries to Autotask (open question O10).
- External holiday feeds; customer satisfaction surveys.

## Reused concepts

Ticket and its explicit operations (`wait`, `resume`, `resolve`, `reopen`, `cancel`, `markDuplicate`, `setPriority`, `moveQueue`, public comment), Ticket Queue and its grants, Priority (`low|normal|high|urgent`), waiting reasons, Ticket history (derived), audit, outbox events and consumers, the job runner, notifications and their categories, My Work Work Item sources (`DueAt` already orders the merged list "due/SLA date nulls last"), the manager overview, the query engine Field Catalog and Saved View degradation of unreadable fields, Tasks (escalation Task with context `ticket`), module switches with preconditions and `blockedReason`, the Presence pattern for recorded DPIA and works-council dates, `safetext`, `expectedVersion`, i18n.

## New concepts

Business Calendar, SLA Policy, SLA Target, SLA Clock (with Breach and Warning facts), Time Entry, Timer. Each passes the glossary's new noun rule: no existing concept measures elapsed working time against a target (Waiting Until is an estimate and explicitly not an SLA; Task due dates are fixed instants) or records effort (Presence explicitly excludes time tracking).

---

## 1. Business Calendar (platform, slice S-A)

- `platform.business_calendars`: `id uuid`, `key text` (immutable slug), `name` (1 to 80, safetext), `timezone text` (IANA, validated with the embedded tz database already used by `platform/scheduling`), `weekly_hours jsonb` (validated in Go into a typed value: per ISO weekday zero or more `[start, end)` intervals in local minutes, sorted, non-overlapping, at least one interval in the week), `status active|archived`, `version`, `created_at`, `updated_at`.
- `platform.business_calendar_closures`: `(calendar_id, closed_on date)` primary key, `name` (safetext, 1 to 80). Full days only in v1; a closure on a non-working day is allowed and has no effect.
- Pure functions in `platform/scheduling` (no database, no clock): `AddBusinessDuration(cal, from, d) time.Time` and `BusinessDurationBetween(cal, a, b) time.Duration`. Local working intervals are converted to instants per day, so daylight-saving transitions are exact (a 08:00–17:00 day is 9 hours on both transition days, unless the transition falls inside it, where the real elapsed time counts). Look-ahead is bounded at 400 days; a target that cannot be reached within the bound is a validation error at policy save time.
- A built-in pseudo calendar `continuous` (24×7, UTC) is not stored; SLA Targets choose `time_basis = continuous` instead.
- Operations (`calendars.manage`, elevated; `expectedVersion`; audited `platform.business_calendar.created|updated|archived|restored|closures_replaced`): `Create`, `Update` (name, time zone, weekly hours), `ReplaceClosures(year, list)` (atomic per calendar and year), `CopyClosures(fromYear, toYear)` (fixed-date closures only; moving holidays are entered by hand, see O3), `Archive` (refused while an active SLA Policy uses it), `Restore`.
- Event `BusinessCalendarChanged` (calendarId, version) on every change that alters working time (hours, time zone, closures). Platform publishes it; it does not know who consumes it.
- API: `GET/POST /api/v1/business-calendars`, `GET/PATCH /business-calendars/{id}`, `PUT /business-calendars/{id}/closures/{year}`, `POST /business-calendars/{id}/copy-closures|archive|restore`. Read: `calendars.manage` or `sla.manage`.
- UI: Administration › Business calendars (week grid editor, closures list per year). German and English labels through i18n.

## 2. SLA Policy and Targets (servicedesk, switch `sla`, slice S-B)

- `servicedesk.sla_policies`: `id`, `key` (immutable), `name`, `description`, `calendar_id` (nullable only when every Target is `continuous`), `pause_waiting_reasons text[]` (subset of the Ticket waiting reasons; default `{customer}`), `warning_percent smallint` (50 to 95, default 80), `escalation_task boolean` (default false, see §5), `status active|archived`, `version`, timestamps.
- `servicedesk.sla_targets`: `(policy_id, priority, kind)` primary key with `priority in ('low','normal','high','urgent')`, `kind in ('first_response','resolution')`, `target_minutes integer` (1 to 129,600 = 90 days), `time_basis in ('business','continuous')`. A missing row means "no target" for that Priority and kind (no clock is started).
- Queue assignment: `servicedesk.queues.sla_policy_id uuid null` (FK, replaces the `sla_policy_key` hook named in F13, which was never built). One policy per Queue; Queue and Priority select the Target.
- Operations (`sla.manage`, elevated; `expectedVersion`; audited `servicedesk.sla_policy.created|updated|archived|restored`, `servicedesk.queue.sla_policy_changed`): `CreatePolicy`, `UpdatePolicy` (name, description, calendar, pause reasons, warning percent, escalation flag), `ReplaceTargets` (the full matrix, atomic, validated against the calendar bound), `ArchivePolicy` (refused while a Queue uses it), `RestorePolicy`, `SetQueuePolicy(queueId, policyId|null, expectedQueueVersion)`.
- **Policy changes are not retroactive.** A clock snapshots `policy_id`, `policy_version`, `target_minutes`, `time_basis` and `calendar_id` when it starts. Editing targets, pause reasons or assigning another policy to a Queue affects clocks started afterwards. Calendar changes are the exception (see §3.5) because a closure added for next week must move existing deadlines.
- API: `GET/POST /api/v1/sla/policies`, `GET/PATCH /sla/policies/{id}`, `PUT /sla/policies/{id}/targets`, `POST /sla/policies/{id}/archive|restore`, `PUT /sla/queues/{queueId}/policy` (body `{policyId|null, expectedQueueVersion}`). All under the route prefix `sla`, so the module gate hides them while the switch is off.
- UI: Administration › SLA policies (matrix editor Priority × response/resolution with a business/continuous toggle per cell), and the policy select in the Queue edit dialog (shown only while `sla` is on).

## 3. SLA Clock (servicedesk, slice S-C)

### 3.1 Model

One clock per Ticket and kind: `servicedesk.ticket_sla_clocks`, primary key `(ticket_id, kind)`.

| Column | Meaning |
|---|---|
| `kind` | `first_response` or `resolution` |
| `status` | `running`, `paused`, `completed`, `cancelled` |
| `policy_id`, `policy_version`, `target_minutes`, `time_basis`, `calendar_id` | snapshot at start, replaced only by `Retarget` |
| `started_at` | Ticket creation time (or retarget base, see 3.3) |
| `elapsed_seconds` | counted working time of all closed running segments |
| `running_since` | start of the current running segment; set exactly while `running` |
| `due_at`, `warn_at` | computed from `running_since`, `target − elapsed` and the calendar; set exactly while `running` (a paused clock has no due time) |
| `paused_at`, `pause_reason` | set exactly while `paused` (the waiting reason) |
| `completed_at` | set exactly while `completed` |
| `end_reason` | for `cancelled`: `ticket_cancelled`, `no_target`, `exempted` |
| `warned_at` | first time the warning point was passed; never cleared |
| `breached_at` | the instant the target was exceeded; never cleared once set |
| `version` | optimistic concurrency of the clock row |

Database CHECKs enforce the per-status columns above and `breached_at IS NULL OR breached_at >= started_at`. An append-only `servicedesk.ticket_sla_clock_transitions` (UPDATE, DELETE and TRUNCATE refused by trigger, like `change_transitions`) records every transition with operation, from/to status, cause (the Ticket operation or the system job), actor, time and the remaining time at that moment.

**Outcome** is derived, not stored: `met` (completed and never breached), `breached` (breach set, running or completed), `exempted`/`not_measured` (cancelled).

### 3.2 State machine

```
            start ─▶ running ◀─ resume ── paused
                       │  └── pause ──▶ ┘
                       ├── complete ──▶ completed ── reopenResume ─▶ running (resolution only)
                       └── cancel   ──▶ cancelled (terminal)
paused ── complete ─▶ completed;  paused ── cancel ─▶ cancelled
running|paused ── retarget ─▶ same status, new snapshot
running ── markWarned / markBreached ─▶ running (facts only)
```

| Operation | From → To | Triggered by (same transaction) | Notes |
|---|---|---|---|
| `Start(kind)` | – → `running` | Ticket `create`, when the switch is on and the Queue's policy has a Target for the Ticket's Priority and kind | `started_at` = Ticket `created_at` |
| `Pause(reason)` | `running` → `paused` | Ticket `wait(reason)` when `reason ∈ pause_waiting_reasons` | adds the segment's business time to `elapsed_seconds`; clears `due_at`, `warn_at` |
| `Resume` | `paused` → `running` | Ticket `resume`, and the automatic resume by a reporter's public reply | new segment from now |
| `Complete` | `running`/`paused` → `completed` | first response: the first public comment by a person with work access in the Queue who is neither reporter nor affected User, or `resolve`; resolution: `resolve` | if `completed_at > due_at` and no breach was recorded yet, `breached_at = due_at` in the same statement (exact without the sweep) |
| `ReopenResume` | `completed` → `running` | Ticket `reopen` (resolution clock only; the first-response clock stays completed) | elapsed time is kept: the Ticket's problem was not solved, so the original deadline continues |
| `Cancel(end_reason)` | `running`/`paused` → `cancelled` | Ticket `cancel` and `markDuplicate` (`ticket_cancelled`); `Retarget` that finds no Target (`no_target`); `Exempt` (`exempted`) | terminal |
| `Retarget(cause)` | `running`/`paused` → same | Ticket `setPriority`, `moveQueue` | new Target from the (new) Queue's current policy and the new Priority; `elapsed_seconds` kept; if the new Target is already exceeded, `breached_at = now` with cause `retargeted`. A breach is never cleared by retargeting (no gaming by lowering the priority). |
| `Exempt(reason)` | `running`/`paused` → `cancelled` | explicit operation `POST /sla/tickets/{id}/exempt`, `sla.manage`, `expectedVersion` of the Ticket, closed reason `misclassified|duplicate_of_major_incident|test_ticket|other` | audited `servicedesk.ticket.sla_exempted`; counted separately in the report |
| `MarkWarned` | `running` (fact) | job `sla.clock_sweep` when `warn_at <= now` | idempotent (`warned_at IS NULL`); event `TicketSLAWarningReached` |
| `MarkBreached` | `running` (fact) | job `sla.clock_sweep` when `due_at <= now` | `breached_at = due_at` (the factual instant, not the sweep time); idempotent; event `TicketSLABreached` |

A Ticket waiting with a reason not in `pause_waiting_reasons` keeps its clocks running. Waiting Until (`expectedAt`) has no effect on clocks. Ticket `close` changes nothing (resolution already completed). `start`/`assign` do not count as a first response (open question O1).

All Ticket operations already lock the Ticket row; clock rows are locked after it (lock order Ticket → clocks), so the sweep (which locks clocks with `FOR UPDATE SKIP LOCKED`, then the Ticket projection row) cannot deadlock with them: the sweep takes the Ticket row with `SKIP LOCKED` too and leaves a busy Ticket for the next pass.

### 3.3 Audit and history

Clock transitions caused by a Ticket operation are not separate audit records; the Ticket operation's audit metadata gains an `sla` entry (kind, from, to, remaining seconds) so the existing audit-derived Ticket history shows "SLA paused", "SLA resumed", "Response target met". Retarget, Exempt and breaches are history kinds of their own (`sla_retargeted`, `sla_exempted`, `sla_breached`); breaches come from the transitions table because the system sweep writes no audit record (it is not a security or business decision; the transition row and the event are the trace). Policy and calendar administration is audited (§1, §2).

### 3.4 Projection on the Ticket for lists

For indexed filtering and sorting the clock owner writes a projection onto `servicedesk.tickets` in the same statement group as every clock change: `sla_response_due_at`, `sla_resolution_due_at` (null unless that clock runs), `sla_warn_at` (earliest `warn_at` of running clocks), `sla_breached_at` (earliest breach of any clock, never cleared), `sla_tracked boolean`. Only the clock repository function writes these columns (a test asserts that no other statement does). Writing the projection does **not** bump the Ticket `version`, so a sweep never causes `expectedVersion` conflicts for people working the Ticket.

Indexes (partial over the open statuses `new|open|in_progress|waiting`, like the existing Queue indexes): `(queue_id, sla_resolution_due_at, id)`, `(queue_id, sla_response_due_at, id)`, `(queue_id, sla_breached_at) WHERE sla_breached_at IS NOT NULL`. Sweep indexes on the clock table: `(due_at) WHERE status = 'running' AND breached_at IS NULL`, `(warn_at) WHERE status = 'running' AND warned_at IS NULL`, `(calendar_id) WHERE status IN ('running','paused')`.

### 3.5 Background work

- `sla.clock_sweep` (recurring, every minute; **disposable** while the switch is off): batches of 500 clocks by the sweep indexes, `MarkWarned`/`MarkBreached`, projection update, events. Precision of notifications is one minute; reads compute "overdue" from `due_at < now()` so the UI is exact without the sweep.
- `BusinessCalendarChanged` consumer `sla.calendar_changed` enqueues the durable job `sla.recompute_due` (dedupe by calendar id) that recomputes `due_at`/`warn_at` of running clocks with that calendar in batches. A clock whose new `due_at` is already past gets `breached_at` = the new `due_at` on the next sweep; a recomputation never clears a recorded breach.

## 4. Visibility (slices S-C and S-E)

- **Ticket detail** (`GET /tickets/{id}`): an `sla` object with one entry per clock: `kind`, `status`, `outcome`, `targetMinutes`, `timeBasis`, `calendarName`, `dueAt`, `remainingSeconds` (business time, computed at read), `pausedAt`, `pauseReason`, `completedAt`, `warnedAt`, `breachedAt`, plus `policyName`. Returned only to callers with view access in the Ticket's Queue, only while `sla` is on. Never returned to the reporter, the affected User or Ticket participants (external accounts): employees see goals, not ITIL fields (constitution 17; open question O5). New ability `abilities.exemptSla`.
- **Queue lists / query engine** (Tickets field catalog): `slaResponseDueAt`, `slaResolutionDueAt` (datetime, sortable, indexed), `slaState` (enum `none|on_track|at_risk|breached|paused|met`, derived in SQL from the projection: `at_risk` = `sla_warn_at <= now()` and no breach), `slaBreached` (boolean), `slaResponseOutcome`, `slaResolutionOutcome` (enum `met|breached|not_measured`, for closed Tickets and reports). The fields declare the module `sla`; while it is off they are absent from `/tickets/fields`, and Saved Views that use them degrade exactly as for unreadable fields (ADR-0033). System View "At risk" per viewed Queue. List columns "Response due" and "Resolution due" with relative time and state chip.
- **My Work**: the Ticket Work Item source sets `DueAt` to the earliest running clock's `due_at` (none while off), so the existing shared order puts the most urgent Tickets first. The "unassigned in my queues" count gains an `atRisk` part.
- **Manager overview** (`GET /tickets/overview`): per Queue `atRisk` and `breached` counts of open Tickets and a "next to breach" list (10 rows). **No per-assignee breach or at-risk counts** (D5). Every number links to the Ticket list with the equivalent `slaState` filter (constitution 28).
- **AI tools**: the Ticket AI tool DTO gains `slaState` and `resolutionDueAt` in S-E (same Queue visibility).

## 5. Warnings and breaches (slice S-D)

- Events `TicketSLAWarningReached` and `TicketSLABreached` (payload: ticketId, queueId, clockKind, dueAt, assigneeId nullable; no title).
- Consumer `sla.notify` (deferred while the switch is off) creates notifications of the new categories `ticket.sla_warning` and `ticket.sla_breached` (reference and title only, like `ticket.assigned`). Recipients: the assignee; when there is none, the active members of the Queue's default Team who have view access in the Queue. The consumer re-reads the clock and drops the notification when the clock is no longer running or the event is older than one hour (a backlog delivered after re-enabling must not flood people with stale alerts). Notification preferences and delivery (email, later Teams) are the existing ones.
- Optional escalation Task: when the policy's `escalation_task` is on, a breach creates one Task through `tasks/public` (context `ticket`, assignee the Queue's default Team, title from i18n "SLA breached: <reference>", due now; dedupe key `sla:<ticketId>:<kind>`). Default off: the Ticket already is the work item; the Task is for Queues where a lead must act explicitly.

## 6. SLA report minimum (slice S-E)

`GET /api/v1/sla/report?from&to&groupBy=queue|priority|month&queueId?` over clocks completed or cancelled in the period, limited to Queues the caller may view (the manager overview's scope; no new permission). Per group and kind: measured count, met, breached, exempted, met rate, median and 90th percentile business time to complete. Not by assignee or Team. Each count links to the Ticket list (`slaResolutionOutcome = breached`, resolved in period, Queue). Period at most 366 days. No export in v1.

## 7. Time Tracking (module `timetracking`, slices T-A to T-C)

### 7.1 Data model

- `timetracking.settings` (single row): `codetermination_basis` (`agreement|not_applicable`, null until recorded), `codetermination_recorded_on date`, `dpia_recorded_on date null`, `edit_window_days smallint` (1 to 90, default 14), `retention_months smallint` (3 to 120, default 24), `show_entry_authors boolean` (default true, O11), `min_report_group_size smallint` (2 to 20, default 3), `version`, `updated_by`, `updated_at`.
- `timetracking.time_entries`: `id uuid`, `subject_type text check in ('ticket')`, `subject_id uuid`, `queue_id uuid` (snapshot of the Ticket's Queue when logged; used for report scoping, documented as a snapshot), `user_id` (whose time), `work_date date`, `duration_minutes integer check 1..1440`, `started_at timestamptz null` (timer entries), `source in ('manual','timer')`, `note text null` (internal, safetext, at most 500), `created_by`, `created_at`, `updated_at`, `version`, `deleted_at`, `deleted_by`, `delete_reason in ('wrong_ticket','duplicate','wrong_duration','other')`. CHECKs: delete fields set together. Indexes: `(subject_type, subject_id) WHERE deleted_at IS NULL`, `(user_id, work_date DESC) WHERE deleted_at IS NULL`, `(queue_id, work_date) WHERE deleted_at IS NULL`, `(work_date)` for retention.
- `timetracking.timers`: `user_id` primary key (at most one running timer per person), `subject_type`, `subject_id`, `started_at`, `note`.

### 7.2 Rules

- **Who logs.** A person logs their own time on a Ticket when they hold `timetracking.log` and work access in the Ticket's Queue (global `tickets.manage` or a `work` grant), checked through `servicedesk/public`. Logging for someone else needs `timetracking.manage` and a reason; the other person must be an active internal User. Reporters, affected Users and external accounts never log time and never see entries (O12).
- **Which Tickets.** Any Ticket status, including resolved, closed and cancelled, as long as the work date lies within the edit window (people log after closing).
- **Work date.** Not in the future; not older than `edit_window_days` (unless `timetracking.manage`). A person's total per work date may not exceed 24 hours (checked under a per-person-and-date advisory lock).
- **Edit and delete.** Own entries: duration, work date, note and Ticket (a corrected Ticket must again be workable) while the work date is within the edit window. Delete is a soft delete with a closed reason. Outside the window, or another person's entry: `timetracking.manage` with a reason. Every change needs `expectedVersion`. Deleted entries are excluded everywhere and purged with retention.
- **Timer.** `Start(subject)` (refused if a timer runs; the UI offers "stop and start"), `Stop(durationMinutes?)` creates a `timer` entry (duration rounded up to the minute; a timer older than 12 hours requires an explicit duration, at most 24 hours), `Discard`. Timers are personal state: not audited, not visible to anyone else, not counted anywhere until stopped.
- **Retention.** Job `timetracking.purge` (daily, **always run** even while the switch is off) hard-deletes entries whose work date is older than `retention_months` and soft-deleted entries 30 days after deletion; timers older than 30 days are deleted.

### 7.3 Reading and reporting

- Ticket panel: `GET /api/v1/time-tracking/entries?subjectType=ticket&subjectId=` returns the Ticket total and the entries to callers with work access and `timetracking.log|manage`; the author is shown when `show_entry_authors` is on, otherwise only the caller's own entries are attributed and the others are summed as "others". View-only Queue access sees the total only.
- Own time: `GET /time-tracking/entries?mine=true&from&to` and a weekly "My time" screen (own entries only).
- Report: `GET /time-tracking/summary?from&to&groupBy=queue|month|priority|ticket` with `timetracking.report`, limited to Queues the caller may view (via `servicedesk/public`). **No `groupBy=user` and no per-person filter.** Any cell with fewer distinct contributors than `min_report_group_size` is suppressed (shown as "fewer than N people"), so a Queue staffed by one person does not become a personal report; `groupBy=ticket` is exempt because a Ticket's effort is operational documentation already visible on the Ticket. Period at most 366 days, no export in v1.

### 7.4 Privacy and works council

Time entries are personal data about employees and are technically suitable for monitoring performance and behaviour. In Germany that triggers works-council co-determination (§ 87 (1) no. 6 BetrVG) where a works council exists, and employee data processing rules (Art. 88 GDPR, § 26 BDSG). Turaco does not decide that for the customer, but it makes the decision explicit:

- **Precondition for enabling** (ADR-0032 pattern, like Presence): the module switch is `blocked` with the new reason `codetermination_not_recorded` until `timetracking.admin` records `codetermination_basis` (`agreement` with the date of the agreement, or `not_applicable` when no works council exists). `dpia_recorded_on` is optional and shown in the settings.
- Purpose limitation in the product: effort per Ticket and aggregated effort per Queue. No per-person totals except one's own, no rankings, no comparison between people, no AI tool over time entries, no export of entries in v1. A per-person report would need a new design and an explicit, recorded agreement (O13).
- Audit minimisation (Presence pattern): own create, edit and delete are audited with entry id, subject and action only; on-behalf and out-of-window changes are audited with before/after duration and work date, the owner and the reason. Note text is never audited. Settings changes are audited with all fields.
- Retention as in 7.2; the operator documentation states the precondition and retention.

### 7.5 Permissions

| Permission | Risk | Meaning |
|---|---|---|
| `calendars.manage` | elevated | Create and change Business Calendars and their closures. |
| `sla.manage` | elevated | Create and change SLA Policies and Targets, assign them to Queues, exempt a Ticket from its SLA. |
| `timetracking.log` | normal | Log, edit and delete own time on Tickets the caller may work; use the personal timer; read own entries. |
| `timetracking.manage` | elevated | Log and correct time for other people and outside the edit window, with a reason. |
| `timetracking.report` | normal | Read aggregated effort for viewable Queues (no per-person breakdown). |
| `timetracking.admin` | high | Settings: co-determination record, DPIA date, edit window, retention, author visibility, minimum report group size. |

Reading SLA data needs no new permission (Queue view access). Role templates: agent templates get `timetracking.log`; lead templates `timetracking.report`; administrators all. The generated [permissions reference](../reference/permissions.md) is updated when they are implemented.

### 7.6 Events and audit (time tracking)

No domain events in v1: nothing consumes them, and an event would carry personal effort data into the outbox without a purpose. Audit actions: `timetracking.entry.created|updated|deleted`, `timetracking.settings.updated`.

## 8. Module switch behaviour

| | `sla` off | `timetracking` off |
|---|---|---|
| Catalog | category `service_management`, requires `servicedesk`, default **off**; route prefix `sla` | own module, category `service_management`, requires `servicedesk`, default **off**; route prefix `time-tracking`; `/time-tracking/settings*` stays reachable (admin permission) so the precondition can be recorded first |
| Routes and UI | 404 `platform.module_disabled`; Ticket `sla` object, list columns, catalog fields, overview counts, Queue policy select and SLA history entries hidden; `/business-calendars` (core) stays | 404; Ticket time panel, My time and report hidden |
| Existing data | kept unchanged | kept unchanged |
| Clocks / entries | **No new clocks are started.** Existing clocks keep being maintained by Ticket operations (pause, resume, complete, cancel, retarget) because they are the Ticket's own data and must stay truthful for re-enabling; no warnings, breaches notifications or Tasks. | No logging; running timers stay and can be stopped after re-enabling (a timer older than 12 hours needs an explicit duration) |
| Jobs | `sla.clock_sweep` disposable; `sla.recompute_due` durable | `timetracking.purge` always run |
| Consumers | `sla.notify`, `sla.calendar_changed` deferred; stale notifications dropped on delivery (§5) | none |
| Re-enable | sweep marks breaches that happened meanwhile (exact `breached_at`, no notification older than one hour); Tickets created while off show "SLA not measured" | entries reappear |
| Disable `servicedesk` | refused while `sla` or `timetracking` is on (existing `required_by_enabled`) | same |

Tickets created before a Queue got a policy, or while the switch was off, have no clocks. There is no retroactive start in v1 (O4).

## 9. Data model and migrations

Forward-only migrations, numbers taken at implementation time (the latest today is `000093`):

1. S-A: `platform.business_calendars`, `platform.business_calendar_closures`.
2. S-B: `servicedesk.sla_policies`, `servicedesk.sla_targets`, `servicedesk.queues.sla_policy_id` (nullable FK, no backfill).
3. S-C: `servicedesk.ticket_sla_clocks`, `servicedesk.ticket_sla_clock_transitions` (append-only triggers), projection columns on `servicedesk.tickets` (nullable, `sla_tracked` default false, no backfill; adding nullable columns without defaults is metadata-only on PostgreSQL 18), partial indexes created `CONCURRENTLY` in a separate migration if the migration tool allows, otherwise documented as a short lock on upgrade.
4. T-A: schema `timetracking` with `settings`, `time_entries`, `timers`.

[Core data model](../domain/core-data-model.md) and [state machines](../domain/state-machines.md) get their implemented sections with each slice.

## 10. Security notes

- All SLA reads reuse `TicketAccess.ViewScope`; IDs of Queues the caller cannot view never appear in reports or the overview.
- Exempt is the only manual SLA mutation, elevated, reasoned and audited; there is no "edit due date" operation.
- Breach facts are immutable once recorded (never cleared by retarget, recalculation or exempt; exempt only ends the clock).
- Time entry notes are untrusted text (safetext, escaped rendering) and never leave the module through events, audit or AI tools.
- Time tracking reads Tickets only through `servicedesk/public` (`make archcheck`); it never reads `servicedesk` tables.
- Report suppression below the minimum group size is applied in SQL before results leave the store; the API cannot be asked for smaller cells.

## 11. Slice plan

Two independent tracks; each slice is one pull request with migration, explicit operations, audit, API (OpenAPI), UI, tests and docs.

| Slice | Content | Depends on |
|---|---|---|
| **S-A** Business Calendars | pure business-time functions with a DST and closure test matrix; storage, admin API and UI; `BusinessCalendarChanged` | – |
| **S-B** SLA policies | switch key `sla` in the module catalog; policies, targets, Queue assignment; admin UI; permissions `sla.manage`, `calendars.manage` in the reference | S-A |
| **S-C** SLA clocks | clock table, transitions, projection; integration into every Ticket operation listed in §3.2; Exempt; Ticket detail `sla` object and history entries; tests: the full transition matrix, lock order under concurrency, retarget never clears a breach, off-switch starts no clocks | S-B |
| **S-D** Warnings and breaches | `sla.clock_sweep`, `sla.recompute_due`, events, `sla.notify` with categories, stale-drop, optional escalation Task, My Work `DueAt` | S-C |
| **S-E** Lists and reporting | catalog fields with module gating, System View "At risk", list columns, overview counts and "next to breach", `/sla/report` and screen, AI tool fields; `EXPLAIN` checks on the partial indexes | S-C (S-D for `at_risk` notifications only) |
| **T-A** Time entries | module `timetracking`, catalog entry with precondition `codetermination_not_recorded`, settings, manual entries API, Ticket panel, My time, audit minimisation, retention purge; permissions | – (parallel to S-A) |
| **T-B** Timer | timers table, start/stop/discard, Ticket panel timer, stale-timer handling | T-A |
| **T-C** Effort report | `/time-tracking/summary` with group-size suppression, report screen | T-A |

## 12. Documentation obligations per slice

Glossary entries (added as planned with this design), state machines (planned sections added), core data model, module boundaries (time tracking row, Service Desk SLA), module-switches design (catalog rows and the new blocked reason), permissions and events references (generated), configuration reference if a setting becomes an environment variable (none planned), operator documentation for the co-determination precondition and retention, current status and implementation plan.

## 13. Open questions

- **O1 First response definition.** Is it only the first public reply by staff (proposed), or also `start`/assignment, or an outbound phone call recorded as an internal note?
- **O2 Pause reasons default.** Proposed default pauses only on `customer`. Should `vendor` and `scheduled_change` pause by default as well?
- **O3 Holiday presets.** Ship computed presets for German federal states (Easter-dependent holidays), or keep manual entry plus copy-to-next-year only (proposed for v1)?
- **O4 Retroactive start.** When a Queue first gets a policy, should open Tickets get clocks (from their creation time or from now)? Proposed: no.
- **O5 Requester visibility.** Should employees see an expected resolution time on their own Ticket? Proposed: no in v1.
- **O6 Reopen semantics.** Proposed: reopening continues the original resolution clock with its elapsed time. Alternative: a new clock per reopen.
- **O7 SLA per Service or Location** (for example a stricter target for patient-care Services) instead of only per Queue and Priority.
- **O8 SLAs for Service Requests** (fulfilment time) reusing the clock model; would need the clock to move out of `servicedesk` or a second owner.
- **O9 Time on Tasks, Changes and Requests.** The `subject_type` column is prepared; which record types are next?
- **O10 Autotask.** Should time entries be pushed to Autotask for installations that bill there (would be integration only, not billing in Turaco)?
- **O11 Entry authors on the Ticket.** Show the author of each time entry to the Queue's workers by default (proposed), or only totals plus one's own?
- **O12 External accounts.** May vendor accounts (Ticket participants) log time on Tickets shared with them?
- **O13 Per-person reports.** Is there any installation that needs them (for example for internal cost allocation)? If yes, a separate design with a recorded agreement and its own permission is required; nothing in v1 prepares it.
- **O14 Calendar per Queue vs per policy.** Proposed: per policy (a Queue served by two locations with different hours would need two Queues or two policies).
