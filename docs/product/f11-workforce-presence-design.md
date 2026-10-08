# F11 Workforce Presence — Feature Design

**Status:** Draft 2026-10-07; decisions W1–W9 adopted by default (autonomous progress; revisit with the customer's data protection review). P-A (backend) is implemented, see the implementation notes at the end; P-B and P-C are not. Target design; [current status](current-status.md) is authoritative for what is implemented. Related: [ADR-0028](../decisions/ADR-0028-workforce-presence.md) (decision and privacy constraints, non-negotiable), [glossary](../domain/glossary.md#workforce-presence-planned-adr-0028), [module boundaries](../architecture/module-boundaries.md), [state machines](../domain/state-machines.md), [F7 design](f7-infrastructure-change-design.md) (Changes, Maintenance Window), [F10 design](f10-remote-access-design.md) (format reference).

## Decisions

- **W1 Operational availability, not HR.** No leave requests or approval, no balances, no time tracking, no payroll. Presence answers "who can do operational work where and when", nothing else. Anything that resembles a personnel record is rejected in review.
- **W2 New module `presence`** (schema `presence`) owns Presence Entries, per-Team coverage minimums, the presence settings and the read models. It reads Users, Teams, Team memberships and Locations only through `organization/public` (existing `work_directory.go` and `directory.go` contracts, extended where a read is missing). Other modules (Service Desk, Changes, Briefing, My Work) read availability only through `presence/public`; nobody reads `presence` tables.
- **W3 No absence reason, anywhere.** The entry kind is `work_location` (Location, `remote` or `travelling`) or `unavailable`. There is no reason column, no free text note, no category (holiday, sick, training). External connectors map every provider reason to `unavailable` before the data leaves the adapter; the adapter never passes the original value on, not even into logs or the raw payload store.
- **W4 Opt-in and kill switch.** `PRESENCE_ENABLED` (default `false`) gates the module at startup (routes, worker jobs, public contract return `presence.disabled` / "unknown" with no data). A second, runtime administrative switch `presence.settings.enabled` is audited and records the date of the data protection impact assessment and the works-council confirmation as free-text-free fields (`dpiaRecordedOn`, `councilConfirmedOn`, both dates, optional). Enabling external sources additionally needs `dpiaRecordedOn`. Disabling stops all reads at once and schedules deletion of all entries after the retention period (W7), shorter if the administrator asks for immediate purge (audited).
- **W5 Scoped visibility, derived by default.** Three levels, all enforced in the backend:
  1. **Own:** a User always sees and edits their own entries.
  2. **Availability:** with `presence.view_availability` a User sees Operational Availability (`available|limited|unavailable|unknown`, until-time, source, freshness) of Users in their scope (own Teams by default; wider scope through the existing role/scope evaluation). No entry details (no location per day) beyond what the derived value needs, for example "limited: remote".
  3. **Detail:** `presence.view_entries` (elevated) shows other Users' entries of the scope; every read is audited (viewer, subject count, scope, never entry content). Manual edits of another User's entries need `presence.manage_entries` (elevated) and are audited per entry.
- **W6 Recurrence moves to a platform scheduling package.** `backend/internal/modules/tasks/application/recurrence_rule.go` (`Rule`, `Validate`, `NextAfter`, daily/weekly/monthly, interval, IANA time zone, DST gap handling, tzdata embed) becomes `backend/internal/platform/scheduling` in a pure refactor commit before any Presence code: same behavior, tests moved with it, Tasks switches to the new package, errors become a platform sentinel that Tasks maps to its own validation error. Presence needs one addition: occurrences **within a window** (`Between(from, to)`, bounded count) and an optional `EndsOn` date; entries recur as "a duration at a local time", so an entry carries `Rule` plus `durationMinutes` or an all-day flag. Presence never imports Tasks internals, and this addition must not change Tasks behavior (existing tests stay green unchanged).
- **W7 Retention.** Past entries are deleted `PRESENCE_RETENTION_DAYS` (default 30, valid 1 to 30, never longer) after they ended; a recurring entry counts as ended after its last occurrence or `EndsOn`, cancelled entries after cancellation. A worker job `presence.purge` runs daily and writes one audit summary (counts only). Interval history of external entries follows the same rule. Aggregated coverage numbers are computed on demand and never persisted per person.
- **W8 No per-person history, no exports.** There is no endpoint, report, CSV or briefing feature that lists a person's past entries or availability over a period beyond the current retention window and the planning horizon. Read APIs take a window of at most 31 days starting no earlier than today minus 1 day; sums, averages, counts of absent days per person and rankings do not exist in any API, read model or search index. Team Coverage returns counts per day, never a list of absent names unless the caller holds `presence.view_entries`. Presence data is excluded from global search, the timeline, the relationship graph and from AI tools until a separate decision (ADR-0029 AI tools must not expose it).
- **W9 Slicing.** P-A backend core (scheduling refactor, module, entries, read models, public contract, retention, audit, events, permissions, integrations contracts) → P-B UI and integrations (My Work, Ticket assignment warning, Change window view, IT Briefing item, settings) → P-C external sources behind ports (Fake first, Microsoft 365 and HR adapters after the data protection review).

## Scope

In: Presence Entries entered in Turaco (own, and by delegated managers), recurrence, Operational Availability and Team Coverage read models, per-Team minimum, scoped visibility, retention and purge, integrations listed below, external source ports with Fakes, settings and kill switch.

Not in F11: leave requests, approval or balances, time tracking, working-time law checks, absence reasons or categories, sickness data, real-time chat presence, automatic ticket reassignment, shift or on-call rota generation, payroll or HR master data, public holidays calendar (a source later, not a requirement), mobile push, per-person reports or exports.

## State machine

**Manual entry (Turaco-owned):** `active → cancelled`. `active` entries are edited in place only through explicit operations (`Reschedule`, `ChangeLocation`, `ChangeRecurrence`, each bumping `version`); `Cancel` is terminal. An entry whose time has passed stays `active` and is removed by retention; there is no `completed` state to avoid a second source of truth. Operations: `Create`, `Reschedule`, `ChangeLocation`, `ChangeRecurrence`, `Cancel`. No generic status update.

**External entry (source-owned, read-only in Turaco):** interval history, like Directory Group memberships: `observed_from`, `observed_to` (null while the source still reports it). The importer (`ObserveSourceEntry`, `CloseSourceEntry`) is the only writer; a changed source value closes the old row and opens a new one. No Turaco user can change or cancel them; an administrator can only disable the source.

**Derived, not stored:** Operational Availability and Team Coverage are computed from entries (all sources, manual and external) at read time for a requested instant or window. The precedence rule is fixed and explained in the response: any applicable `unavailable` wins; otherwise a work location that satisfies the requested need gives `available`; a location that does not (remote when on-site is requested, travelling, or `unavailable` for part of the day) gives `limited`; no applicable entry and no fresh external signal gives `unknown`, never `available`. External and manual entries are not merged into one value silently: the response lists the contributing sources and their freshness, and an external signal that is stale (older than `PRESENCE_SOURCE_STALE_AFTER`, default 24 h) is shown as `unknown` for that source. Add the lifecycle to `docs/domain/state-machines.md` in P-A.

## Data model sketch (migration 000057 `presence`)

Schema `presence`, module-private. Times are `timestamptz` UTC; recurrence anchors use a local date, time and IANA zone.

- `presence.settings` (single row): `enabled`, `dpia_recorded_on date`, `council_confirmed_on date`, `retention_days smallint check (1..30)`, `external_sources_enabled`, `updated_by`, `updated_at`, `version`.
- `presence.entries`: `id uuid`, `user_id` (subject), `kind text check in ('work_location','unavailable')`, `location_type text check in ('location','remote','travelling')` (null for `unavailable`), `location_id` (Organization Location, null unless `location_type = 'location'`; check constraint ties them), `starts_at`, `ends_at` (for recurring entries the first occurrence), `all_day boolean`, `recurrence jsonb` (validated `scheduling.Rule` plus `ends_on`, null when single), `source text` (`manual` or a source key), `source_ref text` (external id, null for manual), `status text check in ('active','cancelled')` for manual rows, `observed_from`, `observed_to`, `observed_at` (external rows), `visibility text check in ('availability','detail')` (default `availability`: what the owner allows; see Privacy), `created_by`, `created_at`, `updated_at`, `cancelled_at`, `version`. Constraints: no column named or shaped like a reason or note (a schema test asserts the column list), `ends_at > starts_at`, span at most 31 days per occurrence, at most 366 days recurrence horizon, manual rows have `source = 'manual'` and `observed_*` null, external rows have no `status` transition (CHECK). Unique `(source, source_ref, observed_from)` for idempotent import. Indexes: `(user_id, starts_at)` and `(starts_at, ends_at) where observed_to is null`.
- `presence.team_coverage_minimums`: `team_id` (primary key, Organization Team id, no foreign key across schemas), `minimum smallint check (>= 0)`, `onsite_minimum smallint` (optional, members at the Team's coverage Location), `location_id` (optional), `updated_by`, `updated_at`, `version`.
- `presence.source_configs`: `source_key`, `enabled`, `last_success_at`, `last_error_code`, `stale_after interval`, `user_mapping_mode` (how external identities map to Users: `email` or `directory_id`, never name matching).
- `presence.source_runs`: `source_key`, `started_at`, `finished_at`, counts (`observed`, `closed`, `skipped`), `error_code` (no person identifiers).
- Triggers: entries identity columns (`user_id`, `source`, `source_ref`, `created_by`) frozen, TRUNCATE guarded like other append-sensitive tables, retention delete only through the purge function that audits.
- **No columns for history exports, no per-person aggregates, no materialized per-person views.** No new timeline, search or relationship rows.
- Platform schemas reused: `audit`, `events` outbox, `jobs`, `notifications`. Next migration number is 000057; no released migration is edited.

## Permissions

New in `backend/internal/platform/permissions` (generated reference updates in `docs/reference`):

| Permission | Level | Meaning |
| --- | --- | --- |
| `presence.manage_own` | default for staff roles | create/change/cancel own entries; implied read of own entries |
| `presence.view_availability` | scoped | see Operational Availability and Team Coverage counts of Users in scope |
| `presence.view_entries` | elevated, scoped, every read audited | see other Users' entry details in scope |
| `presence.manage_entries` | elevated, scoped | create/change/cancel entries of other Users (each audited) |
| `presence.manage_teams` | elevated | per-Team minimums |
| `presence.admin` | elevated | settings, kill switch, source configuration, immediate purge |

Employees without a staff role get none of these. Scope rules reuse Organization's role/scope evaluation (own Teams by default); the engine is not duplicated in the module. Authorization is enforced in the application layer and again in repository queries (a viewer-scope filter, so a missing check cannot leak rows).

## Audit

Audited (ids, kinds, counts, reason codes; never entry times of other people beyond ids, never a location name): `presence.settings.updated` (enabled, dpia/council dates set, retention), `presence.entry.created|rescheduled|location_changed|recurrence_changed|cancelled` (always when the actor is not the subject; own edits are audited with kind and id only), `presence.entries.detail_viewed` (viewer, scope, subject count, window), `presence.team_minimum.set`, `presence.source.enabled|disabled`, `presence.source_run.completed` (counts), `presence.purge.completed` (counts, cutoff), `presence.availability.read_denied` is not audited per attempt (rate-limited security log only). Audit payloads never contain an absence reason because none exists.

## Events and jobs

Events (outbox, reference ids only): `presence.entry_changed` (user id, window, no kind) so read-model consumers and notifications can react; `presence.coverage_below_minimum` (team id, day, observed count, minimum) only when an administrator configured a minimum and notifications are enabled for Team managers. Nothing event-driven carries personal presence detail.

Jobs (existing `platform/jobs`, idempotent, advisory-locked per source): `presence.purge` (daily), `presence.source_sync` (per enabled source, P-C), `presence.coverage_check` (hourly, writes one notification per Team and day at most, deduplicated). Retries never double-notify.

## Privacy

Privacy rules from ADR-0028 are binding; this section turns them into implementation checks:

- **Data minimization:** only `unavailable` is stored for absence; the source adapters are tested with provider fixtures that contain reasons ("sick", "vacation") and the test asserts that nothing from them reaches the database, logs or audit.
- **Microsoft 365 scope:** permissions requested are limited to work location, free/busy and automatic-reply state (for example `Calendars.ReadBasic`-class and `MailboxSettings.Read`-class scopes, to be confirmed in P-C against current Graph documentation; nothing that returns subject, body, attendees or message content). Documented in the threat model written in P-C; a data protection review precedes enabling it.
- **Visibility:** "most viewers see only availability" is the default; `visibility` on an entry lets the owner keep the detail restricted even from `presence.view_entries` holders except the audit-required break-glass case, which is out of F11 (not built).
- **No evaluation use:** no endpoint returns a person's absence statistics; briefing and search never aggregate per person (W8). Review checklist item: any new query that groups by `user_id` over more than one day is rejected.
- **Retention:** W7, enforced by a database-level cap (`retention_days <= 30`) and the purge job; purge on disable.
- **Notices:** the UI shows each User what is visible about them and to whom ("visible to: your Team leads as availability"), and lists the data protection documents' recorded dates from settings.
- **Opt-in:** W4; the installation default is off, and the documentation states the DPIA and works-council precondition.
- **Exports and backups:** generic CSV export features of the platform must exclude `presence` (listed in the allow-list test); backups follow the installation's retention, documented as a limitation.

## Integrations with existing concepts

- **Tickets assignment warning:** `servicedesk` calls `presence/public.Availability(ctx, userIDs, at)` when a Ticket is assigned and shows a non-blocking warning ("unavailable until Monday", "limited: remote") to the assigning user. No reassignment, no blocking, no stored copy; if the viewer lacks `presence.view_availability` for that User the warning shows only a neutral "may be unavailable" when the assignee is `unavailable` (the minimal fact needed to protect the ticket), and nothing otherwise. The public contract applies the viewer's scope; the Service Desk never decides visibility.
- **Changes / Maintenance Windows:** the Change detail (F7) shows the implementer's availability across the Change's window (`window_start`..`window_end`, at most 30 days), read through `presence/public`; `unavailable` or `limited` during the window is a warning on submit and schedule, never a gate. The maintenance calendar may show Team Coverage counts for the window.
- **IT Briefing:** a briefing item "Team Coverage today" (below minimum, or no on-site member) referencing the read model (`presence/public.TeamCoverage`); it lists counts and Teams, not names, unless the reader holds `presence.view_entries`. Respects the module switch (no item when disabled).
- **My Work:** shows the user's own upcoming entries and, for Team leads, the availability of their assignees and Team Coverage for the week; no new task or notification concept. Sections are hidden entirely when Presence is disabled.
- **Notifications:** only the existing notification platform; one in-app notification per Team and day for coverage below minimum, to Team managers who hold `presence.view_availability`.
- **Organization:** Users, Teams, Team memberships valid in the period (a Team member who joined mid-period counts only for valid days), Locations. Membership history is read, never copied.

## HTTP API (P-A)

`/api/v1/presence`, all handlers authorized in the application layer, windows validated (at most 31 days):

- `GET /presence/settings`, `PUT /presence/settings` (`presence.admin`, `expectedVersion`).
- `GET /presence/me/entries?from&to`, `POST /presence/entries`, `POST /presence/entries/{id}/reschedule|change-location|change-recurrence|cancel` (own, or `presence.manage_entries` for others; `expectedVersion` on each).
- `GET /presence/entries?userId=&teamId=&from&to` (`presence.view_entries`, audited).
- `GET /presence/availability?userIds=&at=` and `GET /presence/availability/window?userId=&from&to` (`presence.view_availability`; response: value, until, source list with freshness, explanation code).
- `GET /presence/teams/{id}/coverage?from&to` (counts per day, minimum, state `ok|below|unknown`; names only with `view_entries`), `PUT /presence/teams/{id}/minimum` (`presence.manage_teams`).
- `GET|PUT /presence/sources`, `POST /presence/sources/{key}/sync` (`presence.admin`, P-C).
- Public Go contract `presence/public`: `Availability(ctx, viewer, userIDs, at)`, `AvailabilityWindow(ctx, viewer, userID, from, to)`, `TeamCoverage(ctx, viewer, teamID, from, to)`; all take the viewer, all return `unknown` plus `Disabled=true` when the module is off.
- OpenAPI in `api/openapi/openapi.yaml` (`Presence*` components) in P-A; error codes `presence.disabled`, `presence.window_too_large`, `presence.not_permitted`, `presence.invalid_recurrence`.

## UI (P-B)

`frontend/src/modules/presence`, functional (visual design postponed). i18n resources only (German and English), no hard-coded strings; no user-facing wording that suggests sickness or leave ("unavailable" only).

- **My presence:** week and month view of own entries, create form (kind, location, date range, all-day or times, recurrence), cancel; a visible "who can see this" notice.
- **Team view** (`presence.view_availability`): week grid of Operational Availability per member (colored value plus text, not color only; accessible), Team Coverage row with minimum highlight; detail cells only with `presence.view_entries`.
- **Admin:** settings (enable, DPIA and council dates, retention), per-Team minimums, source status with freshness.
- Embedded: Ticket assign dialog warning, Change detail availability strip, My Work coverage panel, briefing item. Everything is hidden when the module is disabled.

## Slices

1. **P-A Backend core:** `platform/scheduling` extraction (pure refactor commit, Tasks tests unchanged), module `presence` (migration 000057, entries, Create/Reschedule/ChangeLocation/ChangeRecurrence/Cancel, settings and kill switch, minimums), derived Availability and Coverage read models with explanation codes and the viewer-scope filter, `presence/public` contract, retention job, audit, events, permissions, generated references, OpenAPI, module-boundary row and glossary/state-machine/current-status docs, `make archcheck` rules for the new module. Tests: scheduling parity, recurrence expansion across DST, privacy tests (no reason column, scope leaks, window limits, purge), disabled-module behavior, concurrency (`expectedVersion`), authorization matrix.
2. **P-B UI and integrations:** presence UI, My Work, Ticket assignment warning (Service Desk consumes the public contract; a Service Desk change is limited to the warning), Change window availability, IT Briefing item, coverage notification, i18n, docs.
3. **P-C External sources:** `integrations/presence` port `Source{Key, Observe(ctx, since) ([]ObservedEntry, error)}` with a Fake and `NotConfigured`; Microsoft 365 adapter (free/busy, work location, automatic-reply state only, against documented Graph APIs, unverified without a tenant as with F6), first HR adapter after the open decision below, identity mapping by email or directory id, source freshness, import idempotency, closing of vanished entries, adapter tests with reason-bearing fixtures, threat model `docs/security/presence-threat-models.md`, data protection review checklist in `docs/security`. Real-system verification only with a lab tenant.

## Reused concepts

Users, Teams, Team memberships and Locations (Organization), permissions and scope evaluation, audit, outbox events, jobs, notifications, Changes' maintenance window, IT Briefing items, My Work, external references where an adapter needs a stable external id (the importer keeps `source_ref` itself because it needs interval history), `safetext` for any text input (there is none in entries by design).

## New concepts

Presence Entry, Operational Availability, Team Coverage (glossary, planned) and the platform scheduling package. No new task, notification, calendar, audit or permission system.

## Open decisions (proposed defaults, need the product owner's choice)

1. **First HR system.** Default proposal: none in F11; ship P-A and P-B and the Microsoft 365 source first because Outlook is already present in most customers, and pick the HR adapter (for example Personio, BambooHR or a generic SCIM/CSV import contract) when a customer names one. A generic CSV import is rejected because it invites reason columns and per-person history.
2. **Retention default.** Proposed 30 days after the entry ended (the ADR default), configurable down to 1 day, hard cap 30. A shorter default such as 7 days is recommended if the customer's works council asks for it.
3. **Default visibility scope.** Proposed: own Teams and Teams one level below; wider scopes by role assignment.
4. **Whether Team Coverage notifications ship in P-B or later.** Proposed: ship, off per Team until a minimum is set.

## Review-risk list

- Any new column, field, log line or audit payload that could carry an absence reason, free text about a person or health information (schema test and adapter fixture tests guard this).
- Aggregation per person over time (statistics, rankings, "days absent", exports, search, timeline, AI tools) in any layer, including SQL views and briefing.
- Visibility leaks: availability or detail returned outside the viewer's scope; viewer filter missing in repository queries; counts that reveal one person (Team of one) are returned as "unknown" below a threshold of two members.
- Scheduling refactor changing Tasks recurrence behavior (DST gaps, month clamping, `NextAfter` semantics) and Tasks importing Presence or Presence importing Tasks.
- `unknown` shown as `available` anywhere, or stale external signals presented as fresh.
- Silent overwrite of external entries by manual ones or the reverse; source and freshness lost in the read model.
- Blocking or automatic reassignment from the Ticket warning, or a Change gate based on presence.
- Retention bypass: purge not covering external interval history, recurring entries, cancelled entries, backups documentation, or the disable path.
- Module active without the opt-in recorded, or external source enabled before the DPIA date is set.
- Microsoft 365 permissions broader than free/busy, work location and automatic-reply state; token and secret handling per ADR-0014.
- Cross-module reads of `presence` tables or Organization private tables; new generic status update API; missing `expectedVersion` on operations.

## P-A implementation notes

Implemented 2026-10-08 (backend only; see [current status](current-status.md)). Deviations and decisions beyond the design:

- **Scheduling package:** `backend/internal/platform/scheduling` (`Rule`, `Validate`, `NextAfter`, plus `EndsOn` and `Between(from, to)` bounded to 1000 occurrences). Tasks keeps its names through type aliases in `modules/tasks/application/recurrence_rule.go` and maps `scheduling.InvalidError` (which also matches `scheduling.ErrInvalid`) to its own `InvalidInputError`; the moved tests are unchanged apart from the error type.
- **Scope:** until scoped role assignments exist, a viewer's scope is the viewer plus the current members of the viewer's own Teams (`organization/public.WorkDirectory`). `presence.manage_entries` and `presence.view_entries` apply inside the same scope. Entry detail is additionally limited by the owner's `visibility = detail` (or the entry having been made by the viewer); the break-glass case stays unbuilt.
- **Recurrence:** the end date (`endsOn`) is mandatory and at most 366 days after the first occurrence; the stored `ended_at` is the end of the last occurrence (cancellation time for cancelled entries) and drives retention. At most 200 active manual entries per User.
- **Ticket warning hint:** for Users outside the viewer's scope `Availability` returns `unknown` with `out_of_scope` and, only through the Go contract, `MayBeUnavailable` when the User is unavailable now. The HTTP API never returns it.
- **Retention:** `presence.purge` runs daily even when the module is off. When the module has been switched off for `retention_days`, it deletes every entry. Setting the startup gate off without the runtime switch does not start that clock; use `PUT /presence/settings` (`enabled=false`) or `POST /presence/settings/purge`.
- **API additions:** `GET /presence/status` (always mounted; enabled flag and the caller's Presence permissions), `POST /presence/settings/purge`, `GET /presence/teams/{id}/minimum`. With `PRESENCE_ENABLED=false` every other route is absent (404). Errors: `presence.disabled`, `presence.window_too_large`, `presence.not_permitted`, `presence.invalid_recurrence`, `presence.invalid_request`, `presence.not_found`, `presence.version_conflict`, `presence.invalid_transition`, `presence.read_only`, `presence.too_many_entries` (422).
- **Not in P-A:** `presence.source_configs/source_runs` tables, source endpoints and jobs (P-C), the `presence.coverage_below_minimum` event and `presence.coverage_check` job (P-B with the notification), UI.

## Review outcomes

Fixed after the P-A review:

1. Enabling Presence at runtime always needs `dpiaRecordedOn` (not only external sources); otherwise `presence.invalid_request`. The works-council date stays optional as in W4 and is recorded when given.
2. Team Coverage resolves membership per coverage day through `organization/public.WorkDirectory.MembershipIntervals` (new, bounded to 2000 rows, `ErrTooManyIntervals`), not through today's members. A day with fewer than two members reports no counts.
3. Scoped entry reads are bounded at 5000 rows and fail with `presence.too_many_entries` (HTTP 422) instead of silently truncating availability.
4. Team Coverage that names unavailable members (`presence.view_entries`) writes the same `presence.entries.detail_viewed` audit as the detail list (`scope=team_coverage`, counts only).
5. Rescheduling a recurring entry applies the requested time zone to the recurrence (the stored zone only when none is given).

Accepted:

6. The database function `presence.purge_entries` is executable by the application role (the application deletes through it by design); there is no separate retention role. Authorization of purge stays in the application (`presence.admin`, the worker job).
