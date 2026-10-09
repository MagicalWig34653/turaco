# F13 Workbench Views — Feature Design

**Status:** Q-A backend query engine and Tickets, Devices and Tasks catalogs implemented 2026-10-08; Q-B backend (Saved Views, Shares, Pins, Pin Rules, migration 000060, review fixes of Q-A) implemented 2026-10-09; the Q-B UI (view bar, sharing, pins, grouped sidebar) implemented 2026-10-09; Work Item sources, Boards and Queues remain planned. [Current status](current-status.md) is authoritative for what is implemented. Related: [ADR-0033](../decisions/ADR-0033-workbench-views-query-engine.md), [ADR-0032](../decisions/ADR-0032-module-switches.md) (module registry), [UI design system](../development/ui-design-system.md), [F2 design](f2-work-foundation-design.md) (Tasks, My Work), [F5 design](f5-service-desk-design.md) (Tickets), [module boundaries](../architecture/module-boundaries.md), [F11 design](f11-workforce-presence-design.md) (format reference).

## Starting point (verified in the repository)

- Lists filter through hand-written parameters. Tasks use `ListQuery` with the shared order due date, priority rank, id and a composite cursor (`tasks/repository/tasks.go`, `encodeCursor`); Tickets use `Filter{UserID, Status, AssigneeID, QueueID, OpenOnly}` and a plain `id < cursor` keyset in `servicedesk/repository/store.go`. Every other list has its own variant.
- Frontend: `DataTable` (client-side `sortRows`, `onLoadMore` with `nextCursor`), `FilterBar` (`ActiveFilter` chips, `DateFilter`, `SegmentedFilter`) and `filterQuery.ts` (`mergeFilterQuery`: URL-owned simple key/value filters). There is no operator model and no saved state.
- A Ticket has `queue_team_id` and `assignee_user_id`; "queue" is only a Team filter. Counts per queue do not exist.
- **My Work shows only Tasks:** `tasks/application.Service.MyWork` lists unfinished Tasks assigned to the caller or the caller's Teams; the frontend adds permission-filtered Briefing entries. Tickets assigned to the caller appear only in the Ticket queue screen. Verified gap, fixed by V8.
- The sidebar is a flat rail of permission-gated destinations (`Shell.tsx`, `paletteCommands.ts`); the module registry (`platform/modules`, `GET /modules/status`) already hides navigation of disabled modules.

## Decisions

- **V1 One query model.** A platform package `platform/query` provides Field Catalog, Filter AST, validator, compiler and keyset pagination. Modules declare catalogs; they do not write filter parsers (ADR-0033).
- **V2 Catalog-driven, allow-listed SQL.** The client sends field keys, operator names and values. The compiler maps a field key to a catalog-declared SQL expression; operators map to fixed SQL fragments; all values are bind parameters. Unknown field, operator or type mismatch is a validation error. No raw SQL, column names, ORDER BY text or functions from the client, ever.
- **V3 Mandatory scope outside the user filter.** The module's visibility predicate (own Tickets, Team tasks, `tickets.view` sees all, and so on) is built by the module's application service and ANDed around the compiled user filter. A filter cannot widen it.
- **V4 No oracle.** A field requiring a permission or redaction the caller lacks is absent from their catalog and rejected in filters, sorts and search. Search ("any field") covers only fields the caller may filter and that are marked searchable.
- **V5 Saved Views are a platform concept** (`platform/views`, schema `views`): a View stores resource, Filter AST, sort, visible columns and a version; it never stores results or authorization. Results are always evaluated as the viewer.
- **V6 Sharing is explicit and revocable.** Visibility `private`, or Shares to users, Teams, roles or everyone. Share level `use` (see results, pin) or `edit` (change the definition). Publishing to everyone and pinning for groups need `views.publish` (elevated). A shared View never grants data access (the viewer's own scope applies; a View can show an empty list).
- **V7 Pins are per user.** A Pin places a View in the user's sidebar group; the user can reorder, hide and collapse. An administrator can create **Pin Rules** for a Team or role (`views.pin_for_groups`, elevated, audited); a Pin Rule shows the View to members who can use it, and the user may hide it for themselves.
- **V8 My Work Work Item sources.** A small contract in `platform/workitems` (interface `Source{Key, Items(ctx, principal, page)}`), implemented in each contributing module's `public` package and registered in `internal/wiring`. My Work merges sources into one read model by a shared sort (due/SLA date, priority). Tasks and Tickets are the first sources; Approvals, Requests and Changes can follow without new tables. The Overview counts come from the same contract (`Count`).
- **V9 Boards and Queues are Views.** A Task Board is a Saved View over `tasks` plus a **Board** record (columns and the column mapping). A Ticket Queue is either a Team (`queue_team_id`) or a Saved View over `tickets`; the sidebar shows counts through the same compiled filter. No separate Kanban or queue engine.
- **V11 Ticket Queue is a Service Desk entity, distinct from a Saved View.** A Queue (IT, HR, Facility) is a real owned record in `servicedesk` with its own key and prefix, number sequence, Team(s), visibility, per-queue permissions, defaults and routing. A Ticket belongs to exactly one Queue. Saved Views stay cross-queue filters (they may filter by `queue`, by several queues, or by none) and a sidebar entry may pin either a Queue or a View; they are never conflated. This supersedes the earlier idea of "queue = Team or Saved View".
- **V12 Per-queue Ticket numbers.** Display ID `<PREFIX>-<n>` (for example `IT-1042`, zero-padded to a minimum of 4 digits, growing without truncation like the existing `TKT-` width fix in migration 000024). Each Queue has its own gap-tolerant counter; the full reference is globally unique and never reused, including after archiving a Queue or deleting a prefix.
- **V13 Moving a Ticket issues a new number and keeps the old one as an alias.** Decision and justification below ("Moving between Queues"). Old numbers always resolve; the Ticket's UUID and its external references never change.
- **V10 Slicing and compatibility.** Q-A to Q-E (below). Existing list endpoints keep their parameters; new `POST .../query` endpoints are added per resource and old parameters are mapped onto the AST in a compatibility layer, then removed per resource when the UI has moved.

## Scope

In: field catalogs and filter builder for tickets, devices, assets, tasks first, then the remaining lists; Saved Views, sharing, pins, pin rules; grouped collapsible sidebar; Ticket Queues with counts; Task Boards; Tickets in My Work and Overview.

Not in F13: cross-resource joins in filters, full-text search engine changes (global search stays as is), user-defined computed fields, charts or exports of Views (export needs its own decision), Views over Presence data (excluded by ADR-0028 W8), saved filter subscriptions or notifications, Board automation rules, swimlanes, WIP limits (later).

## Query model

### Field Catalog (declared by modules, `platform/query`)

```
Resource{ Key "tickets", Module "servicedesk", Table, Alias, IDColumn,
          VisibilityPredicate (supplied per request by the module),
          DefaultSort [], MaxPageSize, Fields [] }
Field{ Key "assignee", LabelKey "tickets.field.assignee" (i18n),
       Type  text | number | boolean | date | datetime | enum | reference | tags,
       Column (allow-listed SQL expression, e.g. "t.assignee_user_id"),
       Operators (subset of the type's operators),
       Filterable, Sortable, Searchable,
       Permission ("" or permission key),
       Redaction (none | hidden_when_not_permitted | masked),
       EnumValues / ReferenceResource (for pickers), Collation, Nullable, IndexHint }
```

- Catalog entries are trusted Go code (never loaded from the database or the client). Each Resource declares a **closed set of SQL expressions and a closed join graph** (only tables of its own module); startup validation builds every expression from typed constructors (`Col(alias, column)`, `Lower(Col)`, `Coalesce(...)`), checks the columns against the module's schema through a test, and fails startup on anything else. There is no regex-allowed free identifier. Projection, filter and sort are **separate declarations** per field (a field can be shown without being filterable or sortable). Values are bind parameters only.
- `GET /api/v1/<resource>/fields` returns the catalog **as the caller may use it** (labels as i18n keys, type, operators, enum values, sortable, filterable). The filter builder is rendered from this response, so the UI never hard-codes fields.
- Reference fields (assignee, queue Team, asset, location) filter by id; the picker uses the owning module's existing search; the catalog carries only the picker resource key. "Contains" on a reference field is not offered (no join on foreign tables).
- Source and freshness fields (observed data) are catalog fields like any other; observed and Turaco-owned values stay separate fields (Desired State, Assignment, Expected Applicability and Observed result are four fields, never merged).

### Operators

| Type | Operators |
| --- | --- |
| text | equals, not equals, contains, not contains, starts with, ends with, is empty, is not empty, in, not in |
| number | equals, not equals, greater, greater or equal, less, less or equal, between, is empty, is not empty, in, not in |
| boolean | is true, is false, is empty |
| date, datetime | equals (day), before, after, between, is empty, is not empty, **relative**: `last_n_days`, `next_n_days`, `today`, `this_week`, `this_month`, `older_than_n_days`, `within_n_days_from_now` |
| enum | equals, not equals, in, not in, is empty, is not empty |
| reference | equals, not equals, in, not in, is empty, is not empty, `is_me`, `is_my_teams` (resolved server-side from the principal) |
| tags / multi | has any, has all, has none |

Text matching is case-insensitive and unaccented by default via the catalog collation expression; `contains`/`starts with` escape `%`, `_` and `\` in the value and use `ILIKE ... ESCAPE`. Relative dates are evaluated server-side in the principal's time zone at query time (the AST stores `{"op":"last_n_days","value":7}`, never absolute timestamps), so a saved View stays correct.

### Filter AST (JSON, versioned)

```json
{
  "v": 1,
  "root": {
    "type": "group", "logic": "and",
    "children": [
      {"type": "condition", "field": "status", "op": "in", "value": ["open", "in_progress"]},
      {"type": "group", "logic": "or", "children": [
        {"type": "condition", "field": "assignee", "op": "is_me"},
        {"type": "condition", "field": "queue", "op": "in", "value": ["<team-id>"]}
      ]},
      {"type": "condition", "field": "created_at", "op": "last_n_days", "value": 7}
    ]
  },
  "search": "printer",
  "sort": [{"field": "priority", "dir": "asc"}, {"field": "created_at", "dir": "desc"}]
}
```

Validation limits (constants in `platform/query`, tested): at most 25 conditions, nesting depth 4, 3 sort keys, `in` lists at most 100 values, string values at most 200 characters, `search` at most 100 characters, page size 1 to 100 (default 50). Violations return `query.too_complex`. Empty groups are rejected. The AST is canonicalized (sorted keys, defaults filled) before storage and hashing, which also supports idempotent identical-view detection.

### Compiler and execution

1. Decode strictly (unknown properties rejected), validate against the caller's catalog, resolve relative dates and `is_me`/`is_my_teams` against the principal.
2. Compile to `(sql, args)`: `WHERE (<visibility>) AND (<user predicate>)`. `not in`/`not equals` on nullable columns use `IS DISTINCT FROM`/`NOT (... AND IS NOT NULL)` consistently, tested for NULL semantics. `search` becomes an OR over the searchable fields' `ILIKE` (or a catalog-declared tsvector/trigram column where one exists).
3. The module's repository executes the compiled predicate inside its own `SELECT` (the platform never connects to module tables itself and never builds the `SELECT` list). Transaction is read-only with `SET LOCAL statement_timeout` (default 5 s).
4. Counts for the sidebar use the same compiler with `SELECT count(*) FROM (SELECT 1 ... LIMIT 1001) c` (capped subquery, "1000+"), under `statement_timeout`. The cache key is (view id, **view definition version**, principal id, **permissions fingerprint**, Team and role membership fingerprint, visible-Queue set) with a 15 s TTL, and entries are invalidated on share revocation, View change, and membership or permission change events.

### Keyset pagination

Sort keys come from the catalog (`Sortable`) plus an implicit unique tiebreaker (`id`). The cursor is an opaque, base64url, HMAC-signed JSON of `{viewHash, sortSpec, lastValues[], id}`; a cursor whose hash or signature does not match the request is rejected (`query.invalid_cursor`). The predicate is built **lexicographically per key**, not as a row comparison, because keys may mix ASC and DESC and NULLs: for sort keys k1..kn the "after cursor" predicate is `OR` over i of (`k1 IS NOT DISTINCT FROM c1 AND ... AND k(i-1) IS NOT DISTINCT FROM c(i-1) AND after(ki, ci)`), where `after(k, c)` has an explicit NULL branch per direction and NULLS placement (ASC NULLS LAST: `k > c` or, when c is not null, `k IS NULL`; if c is null nothing follows within the key; the DESC variants mirror it), and the final key is the unique `id`. Rows updated between pages may move or repeat once but never crash the cursor; the contract is "no duplicates of unchanged rows, no skips of unchanged rows".

### Index guidance (per catalog, reviewed in database-change)

Each Resource documents its indexed filter and sort fields. Defaults: btree on status/assignee/queue/priority and created/updated/due timestamps with `id` suffix; trigram (`pg_trgm`, already a PostgreSQL extension, no new dependency) on title/name fields only when `contains` is enabled on a large table; fields without index hint are `filterable` but flagged `slow` and rely on the visibility predicate and statement timeout. Q-A adds only indexes proven necessary by query plans in tests (`EXPLAIN` assertions on the default views).

**Resolution after the Q-A review (implemented in Q-B, migration 000060):** substring matching had no index, so `contains`, `not_contains`, `ends_with` and the OR over all searchable fields were table scans bounded only by the statement timeout. The rules are now: (1) `pg_trgm` is accepted (open decision 10; it ships with every PostgreSQL distribution and is trusted since PostgreSQL 13, so the database owner creates it; a database without contrib packages fails the migration loudly instead of running slow). (2) A GIN trigram index exists on ticket `reference` and `title`, task `title` and device `name` and `serial_number`; each of those catalog fields declares `Index: IndexTrigram`, which is the only way a field can be `Searchable` (checked when the catalog is built, so a searchable field without index fails startup and every test). (3) A trigram index serves a pattern of three or more characters, so a `contains`, `starts_with` or `ends_with` value shorter than that is costed as an unindexed match (not allowed in an OR group with siblings), and `search` needs at least three characters. (4) `not_contains` is `NOT ILIKE` and no index can serve it: it is always costed slow, so it cannot be combined by OR and is budgeted by the cost limit. (5) Ticket and Task `description` has no trigram index (multi-kilobyte text, write amplification) and is not offered for substring matching or search: only `is_empty` and `is_not_empty`. Full-text search over descriptions needs its own decision (tsvector) and is out of F13. (6) Text equality compares `lower(column)`; ticket `reference` gets a `lower(reference)` index. Tests assert by `EXPLAIN` (sequential and plain index scans disabled, so only an index that can serve the emitted `ILIKE ... ESCAPE` statement remains) that title, reference, task title, device name and search use the trigram indexes, and by index definition for the device serial number (the planner cannot choose between the trigram index and an equivalent partial btree on an empty test table).

The per-principal rate limiter of the engine keeps at most 10 000 buckets (hard bound). An insertion at the limit inspects at most 64 entries and removes the idle ones, otherwise the least recently used of that sample; an active principal that is evicted starts over with a full bucket, which can only return a burst.

## Authorization and redaction

- `views.*` permissions control View management only. Reading rows is always the resource's own permission and scope; **every request re-evaluates the viewer's scope**, even when it runs a View someone else shared.
- Field `Permission` examples: Ticket internal comment text (`tickets.manage`), Device serial/BitLocker-like sensitive attributes (`endpoints.view_sensitive` equivalent), Asset cost (`inventory.view_cost`). Without the permission the field is absent (V4). `masked` fields are displayed masked and are only filterable with `equals` against the masked-safe value or not at all (default: not filterable); a test asserts that no operator can reveal a masked value by bisection (`starts with`, `between`, sort are disallowed on masked fields).
- Principals who cannot see a reference target (for example another Team's private queue) can still filter by its id only if the catalog allows id filters; the picker lists only visible targets.
- Saved View with an unreadable field: evaluation fails closed per condition. The condition is reported as `query.field_unavailable` in the response's `warnings`, and the View runs with that condition **replaced by "no rows"** inside AND groups (never dropped), so permissions loss cannot widen results; the UI shows the broken condition so the owner can fix it.
- Search, Views and Boards never read another module's private tables. A catalog may join only tables of its own module.

## Data model sketch (migration 000060 `workbench_views`)

Schema `views`, owned by `platform/views`; Board tables are owned by the tasks module in schema `tasks`.

- `views.saved_views`: `id uuid`, `resource text` (catalog key), `name text` (1 to 80, `safetext`), `description text` (optional, 500), `owner_user_id`, `definition jsonb` (canonical Filter AST, sort, columns), `definition_hash`, `schema_version smallint`, `visibility text check in ('private','shared')`, `version int`, `archived_at`, `created_at`, `updated_at`. Unique `(owner_user_id, resource, lower(name)) where archived_at is null`. Constraints: `octet_length(definition::text) <= 16384`; `resource` checked against registered catalogs in the application layer.
- `views.view_shares`: `view_id`, `subject_type check in ('user','team','role','everyone')`, `subject_id` (null for everyone), `level check in ('use','edit')`, `granted_by`, `granted_at`. Unique `(view_id, subject_type, coalesce(subject_id, zero-uuid))`. `everyone` shares need `views.publish`; `edit` shares to `everyone` are rejected.
- `views.pins`: `user_id`, `view_id`, `group_key text` (sidebar group), `position int`, `hidden boolean`, `created_at`; primary key `(user_id, view_id)`.
- `views.pin_rules`: `id`, `view_id`, `subject_type check in ('team','role')`, `subject_id`, `group_key`, `position`, `created_by`, `created_at`, unique per `(view_id, subject_type, subject_id)`.
- `views.sidebar_state`: `user_id`, `collapsed_groups text[]`, `updated_at` (per-user presentation; localStorage is only a cache).
- `tasks.boards`: `id`, `name`, `owner_user_id`, `owner_team_id` (nullable; one of the two), `view_id` (the Saved View over `tasks`), `column_mapping jsonb` (see below), `version`, `archived_at`, timestamps. `tasks.board_shares` is not separate: sharing is the board's View Share set plus `board_level` (`view` or `edit`) stored in `views.view_shares` of its View (one sharing mechanism).
- `tasks.board_columns`: `board_id`, `id`, `position`, `title` (1 to 40), `maps_to text check in ('open','in_progress','blocked','completed','cancelled')`, `collapsed boolean`. A board has 2 to 8 columns; every column maps to exactly one Task status; multiple columns may map to the same status only for display grouping and then cards move by rank (see Boards).
- `tasks.board_card_ranks`: `board_id`, `task_id`, `rank text` (fractional index, orders cards inside a column), primary key `(board_id, task_id)`; absent rows sort by the View's default sort after ranked cards.
- Triggers/constraints: View identity (`owner_user_id`, `resource`, `created_at`) frozen; shares of archived Views ignored; deletion is archive (restorable by owner) except hard delete by retention job after 90 days. Audit and outbox use the existing platform schemas. Migration numbers are reserved once: Views 000060, Queues and numbering 000061, Boards 000062; no released migration is edited. No migration is needed for the query engine itself; catalogs are code, and indexes arrive as small forward migrations in the slice that needs them.

## Permissions

New in `backend/internal/platform/permissions` (generated reference updates `docs/reference/permissions.md`):

| Permission | Level | Meaning |
| --- | --- | --- |
| `views.use` | default for all signed-in users | create private Views, pins, Boards on resources one may already read |
| `views.share` | normal | share a View with users, Teams and roles one is allowed to see in Organization |
| `views.publish` | elevated | share with everyone; mark a View as organization-wide default |
| `views.pin_for_groups` | elevated | the one permission for pinning for groups: create Pin Rules for Teams and roles (publishing to everyone uses `views.publish`, unrelated to pins) |
| `views.admin` | elevated | list, take over, unshare or archive any View (audited) |
| `tasks.boards.manage_team` | normal | create and edit Team-owned Boards (otherwise Team Boards need `tasks.manage`) |

A View for a resource requires that the creator may read that resource (`<resource>.view`-equivalent; Tickets: signed-in users see their own). Resource permissions never come from a View.

### Sharing rules

- `use`: the user sees the View in lists and can pin it; results use the user's scope.
- `edit`: can change the definition, name and columns (version-guarded); cannot change shares or owner. Only the owner (or `views.admin`) manages shares and archives.
- An edit to a shared View by an editor shows an "edited by" note and is audited; viewers of the View see the new definition at once. To avoid surprise, the share dialog states this, and an owner can "duplicate for me".
- Team/role shares are resolved at read time (membership changes apply immediately; no snapshot).
- IDOR rule: View ids are guessable-safe UUIDs but never trusted; every read of `GET /views/{id}` checks ownership or an applicable Share; otherwise 404. A user who cannot see a View cannot learn its name or owner.

## Audit

`views.view.created|renamed|definition_changed|archived|restored|ownership_taken`, `views.share.granted|revoked` (subject type and id, level), `views.published` (everyone), `views.pin_rule.created|deleted`, `tasks.board.created|changed|archived`, `tasks.board.column_changed`. Payloads carry ids, resource key, version, counts of conditions; never the filter values (they can contain personal data), never row data. Personal pins and reordering are not audited. Query execution is not audited per request; slow or rejected queries go to the structured log with the definition hash only.

## Events and jobs

Events: `views.view_shared` (view id, target subject) for an in-app notification "X shared a view with you" through the existing notification platform (category `views`, default on, user can disable), `views.view_archived`. No events for pins. Jobs: `views.purge_archived` (daily, hard-delete after 90 days, advisory-locked), `views.counts_refresh` is **not** a job (counts computed on demand with a short cache).

## Ticket Queues, numbering and the sidebar

### Queue entity (`servicedesk` module, migration 000061)

Queue is Service Desk-owned business data; Views and the sidebar only reference it. Today a Ticket has `queue_team_id` and one global reference `TKT-000123` from `servicedesk.ticket_number_seq` with a unique constraint (migration 000031, width fix 000024). That Team-as-queue is replaced by the entity below.

- `servicedesk.queues`: `id uuid`, `key text` (immutable slug, `^[a-z][a-z0-9-]{1,30}$`), `prefix text` (`^[A-Z][A-Z0-9]{1,7}$`, unique among **all current and former** prefixes), `name_i18n jsonb` or `name` (1 to 80, `safetext`), `description`, `status check in ('active','archived')`, `visibility check in ('internal','public')` (public = employees may choose it when raising a Ticket), `default_priority`, `default_team_id` (assignment hint, not authorization), `sla_policy_key text null` (hook for a future SLA module; nothing computed in F13), `routing_mode check in ('employee_choice','automatic','both')`, `default_for_intake boolean` (exactly one, partial unique index), `next_number bigint not null default 1` (counter row), `number_padding smallint default 4`, `version`, `created_at`, `updated_at`, `archived_at`. Prefix and key are frozen by trigger after the first Ticket exists (prefix is part of issued numbers).
- `servicedesk.queue_teams`: `queue_id`, `team_id` (Organization Team id, no cross-schema FK), `role check in ('workers','managers')`. A Queue may have several Teams; Team membership resolves through `organization/public`.
- `servicedesk.queue_grants`: `queue_id`, `subject_type check in ('user','team','role')`, `subject_id`, `level check in ('create','view','work','manage')` (levels are cumulative: manage includes work, work includes view; `create` is independent so that employees can raise tickets into a Queue without seeing it). `public` Queues grant `create` to every signed-in employee implicitly.
- `servicedesk.queue_routing_rules`: `queue_id` (target), `position`, `match jsonb` (the same Filter AST over the intake fields of a new Ticket: category, affected Asset type, requester Team, keywords in title) , `enabled`. First matching rule wins; evaluated by the existing intake path; no rules engine beyond this ordered list. Rules reference only the Ticket intake catalog (V2 allow-list), never raw SQL.
- `servicedesk.tickets`: `queue_id uuid not null` (replaces `queue_team_id` semantically; `queue_team_id` kept deprecated and written as the Queue's primary Team for one release because Briefing and Remote Access read it), `reference` stays the display ID of the current number, `number bigint` (the per-queue number), unique `(queue_id, number)`, unique `(reference)`.
- `servicedesk.ticket_aliases`: `reference text primary key` (the old display ID, globally unique across `tickets.reference` and aliases, enforced by one check function plus a unique index on a union-free helper table `servicedesk.reference_registry(reference primary key, ticket_id, kind check in ('current','alias'), issued_at)` that every issued number writes to), `ticket_id`, `queue_id` (queue at issue), `retired_at`. Aliases are never deleted and never reassigned.

### Numbering and concurrency

- A number is issued in the same transaction as the Ticket insert (or move): `UPDATE servicedesk.queues SET next_number = next_number + 1 WHERE id = $1 RETURNING next_number - 1` takes the row lock, so concurrent creations in one Queue serialize on that row only; different Queues do not contend. Only **committed** references are permanent: a rolled-back transaction rolls the counter back, so a number that never committed may be issued again, which is harmless because nobody could have seen it; a committed reference is never reused. Gaps are allowed (for example from deleted drafts or future changes of the scheme). A PostgreSQL SEQUENCE per Queue was rejected: DDL per Queue and no transactional rollback of values, but it would also not contend; the counter row is chosen because Queue creation stays plain data (no DDL at runtime) and it is easy to test. Unique indexes on `(queue_id, number)` and `reference_registry.reference` are the safety net; a unique violation retries once.
- The migration seeds the **default Queue** `{key: 'it', prefix: 'TKT' kept as the legacy prefix, next_number = max existing + 1}` and assigns all existing Tickets to it with `number` parsed from their reference (`TKT-000123` becomes number 123, reference unchanged, so every existing number, link, email and Autotask reference stays valid). New Queues start at 1. An administrator may later rename the default Queue's display name (not the prefix) and create `IT` as a new Queue; the legacy `TKT` prefix stays reserved forever. Migration is forward-only; the old global `ticket_number_seq` stays until the default Queue's counter is verified, then is unused (never dropped in the same release).
- `GET /tickets/by-reference/{ref}` resolves a current number or any alias in `reference_registry`, authorizes with the caller's Queue and Ticket scope, redirects (`301`-style response body with the current reference) to the current Ticket, and answers `404` for tickets the caller cannot see.

### Moving between Queues (`move`, explicit operation, not a status update)

Options considered: (A) keep the original number as the permanent display ID; (B) issue a new number from the target Queue and keep the old one as an alias.

**Decision: B.** The prefix is part of how people and the system recognize which desk owns the work ("HR-0007" must not appear as "IT-1042" in HR's queue, in emails to the requester, or in a Facility report), and numbers must stay unique per prefix with no collisions. Keeping an alias preserves every link, email subject match, search and external reference, which is the property A was meant to protect. The cost is that two numbers exist for one Ticket; the UI shows the current number prominently, "previously IT-1042" beside it, and the timeline records the move. Internal UUID, `externalrefs`, relationships and Task context never change. Inbound email or text referring to an old number resolves through the registry. Chains of moves keep every earlier number as an alias.

Operation `MoveToQueue(ticketID, targetQueueID, reason code, expectedVersion)`: in one transaction check permissions, lock the target Queue row to issue the number, write the alias, update the Ticket, clear the assignee if the assignee lacks `view` in the target Queue (the assignee never keeps hidden access; the Ticket becomes `open` unassigned and the actor is told), keep status, comments, attachments and relationships, append the audit entry `servicedesk.ticket.queue_moved` (ticket id, from/to Queue ids, old and new reference, reason code; no titles) and emit `TicketQueueChanged` (reference ids only) so notifications and Briefing update. Permissions: `work` or higher in the source Queue **and** `create` (or higher) in the target Queue; moving into a Queue the actor cannot see requires `servicedesk.queues.manage`. A requester who cannot see the target Queue still sees their own Ticket (own-ticket rule), with the new number and Queue name shown only if the Queue is `public` or the requester holds `view`; otherwise a neutral label "Handled by <desk display name>" (the Queue's `public_label`, default the Queue name) is shown, so internal Queue names do not leak.

### Visibility and permissions per Queue

- New platform permissions: `servicedesk.queues.manage` (elevated: create/archive/rename Queues, grants, routing rules, prefixes) and the per-Queue grant levels above (data, not permission keys). Existing `tickets.view` and `tickets.manage` keep their meaning as **global** grants that implicitly hold `view` and `manage` (respectively `work`) in every Queue, so upgrade changes nothing for current roles; installations that want separation grant Queue-level access instead and remove the global permission from those roles. Every Ticket read, search, count, View result and My Work item applies `queue_id IN (queues the caller can view)` OR the own-ticket rule; this predicate is the `tickets` catalog's mandatory visibility predicate (V3). The `queue` field is a reference field in the catalog; its picker lists only visible Queues (V4: no queue-existence oracle).
- Reporters and affected Users always see their own Tickets regardless of Queue grants (current F5 rule).
- Internal comments remain `tickets.manage`-or-Queue-`work`; presence of other Queues is never disclosed by counts (counts are computed with the same predicate).

### Raising and routing

- **Employee choice:** the Ticket create form lists Queues with `visibility = public` and the caller's `create` grant and routing mode `employee_choice|both`, with name and description; nothing else.
- **Automatic routing:** for `automatic|both`, the first enabled routing rule that matches the intake fields assigns the Queue; if none matches, the `default_for_intake` Queue is used. The employee can never route into a hidden Queue by guessing an id: the create API validates the same `create` grant as the UI. Staff creating a Ticket on behalf of someone choose any Queue where they hold `create`.
- Intake from other modules (Requests, Security, Monitoring-sourced Tickets) pass a Queue key through `servicedesk/public`, defaulting to the default Queue; Queue keys are stable contract values.

### Autotask and external references

Autotask is integrated through `externalrefs` with `ticketID` (uuid) as the entity id, so the sync identity does not depend on the display number (verified in `servicedesk/application/external.go`: `externalrefs.Ensure(ctx, tx, ExternalSystem, ExternalEntity, ticketID)`). Moving a Ticket keeps the external reference. Open points for the adapter slice (unverified without an Autotask tenant, as in F5): map a Turaco Queue to an Autotask Queue (a `queue_external_mappings` row per system, `external_queue_id`), push a queue change as an update, and import tickets into the mapped Queue or the default Queue; Autotask's own ticket number is stored as external reference data and never as the Turaco display ID. A move pending sync is marked through the existing `MarkPending` path.

### Counts and the sidebar

- Sidebar entries reference either a **Queue** (counts: open Tickets in the Queue visible to the caller, plus "unassigned" sub-count) or a **Saved View**; a View may span Queues. System entries (code-defined): "My tickets", "Unassigned in my Queues" and each Queue where the caller holds `view`. They use the same compiler, so a Queue entry is the Saved-View filter `queue = X` plus the Queue's special permissions; they remain separate objects.
- `GET /api/v1/views/counts?ids=` accepts View ids and `queue:<id>` handles (max 30) and returns `{id, count, capped}` for the caller (same compiler, 15 s cache keyed by the caller's visible-Queue set).
- **Sidebar groups** (frontend `shell`): Work (My Work, Overview), Tickets (Queues with counts, pinned Views), Tasks (Boards, pinned Views), then the existing destination groups. Groups are collapsible; state is stored in `views.sidebar_state` and cached in `localStorage`. A group with no visible item (module disabled by ADR-0032, no permission) is not rendered. The collapsed rail (64 px) shows group icons with a flyout. Keyboard path: groups are disclosure buttons; arrow keys move through items; counts have accessible labels.

### Queue API and UI sketch

- `GET /service-desk/queues` (visible to caller; `?for=create` for intake), `POST /service-desk/queues` , `GET|PATCH /service-desk/queues/{id}` (`expectedVersion`), `POST /service-desk/queues/{id}/archive|restore`, `PUT /service-desk/queues/{id}/teams|grants|routing-rules` (full replace, versioned), `POST /tickets/{id}/move-queue` (`targetQueueId`, `reasonCode`, `expectedVersion`), `GET /tickets/by-reference/{ref}`. Error codes `servicedesk.queue_not_found`, `servicedesk.queue_prefix_taken`, `servicedesk.queue_archived`, `servicedesk.queue_not_permitted`.
- Admin UI "Service Desk queues": list with prefix, Teams, counts; edit dialog (name, prefix once, visibility, routing mode, default priority/Team, SLA hook key), Teams, grants table (user/Team/role x level), routing rules ordered list using the shared filter builder over the intake catalog. Ticket detail shows the current number, "previously X, Y" aliases, Queue name and a Move action with reason dialog. Ticket create form shows the Queue choice (employee view) or Queue select (staff). Search by any alias is handled in `by-reference` and in the Ticket catalog's `reference` field (matches current and alias numbers; alias hits are labelled).
- Archiving a Queue is refused while open Tickets exist unless they are moved first (explicit bulk `move-queue` job with audit, idempotent per Ticket).

## Task Boards (Kanban)

- A Board = Saved View over `tasks` (any filter, for example "my team's tasks") + ordered Columns, each mapped to a Task status. Default new Board: Open, In progress, Blocked, Done (completed). Cancelled tasks are hidden unless a column maps to `cancelled`.
- **Moving a card never sets a status field.** The UI maps a drop to an explicit operation: `open → in_progress` = `start`; `in_progress → completed` = `complete`; `open|in_progress → blocked` = `block(reason)` (reason dialog); `blocked → open` = `unblock`; `completed|cancelled → open` = `reopen(reason)` (needs `tasks.manage`); a drop onto `cancelled` = `cancel(reason)`. Transitions the state machine forbids (for example `completed → in_progress`) are rejected in the UI before dropping (disabled drop target with reason) and again by the backend; the response is the existing Task error. Existing Task authorization (`tasks.manage` or `tasks.work` on own/Team tasks) applies per card, and expected `version` is sent so a concurrent change shows a conflict and a reload.
- Rank writes re-check Board edit access and that every task id is currently visible to the caller. Card moves call the existing Task operation with a **mandatory** `expectedVersion` (stricter than the Task API default). Moving within a column or between columns that map to the same status only changes the card **rank** (`PUT /boards/{id}/ranks`), which is Board presentation data, not Task state, and needs only Board edit level.
- Board access: the Board uses its View's shares (`use` = view the board, `edit` = also change columns and ranks; moving cards additionally needs the Task permissions). A user who can see the Board but not a particular Task sees neither card nor count (visibility predicate), and column counts are computed from visible cards only.
- Several Boards per user and per Team (`owner_team_id` Boards appear for Team members via an implicit use share resolved from membership, plus explicit shares). Limits: 20 Boards per owner, 8 columns, 200 cards loaded per column (then "load more" by keyset per column).
- Columns are configurable (title, order, mapping, collapse); `maps_to` can be changed by explicit `ChangeColumnMapping`; cards are not mutated by column changes.

## My Work and Overview: Tickets

- `platform/workitems`: `Source` interface `{Key; Items(ctx, Principal, Page) ([]WorkItem, cursor); Count(ctx, Principal) int}`; `Count(ctx, Principal) (n int, capped bool, err error)` is capped and may fail (a failing source shows an "unavailable" notice, never a zero); `Items` and `Count` both authorize the principal and check the module state at execution (disabled module: not executed). Each source returns its own continuation cursor with deterministic tie-breakers (sort key, then source key, then id), and My Work merge-sorts the per-source keyset streams, returning a composite cursor of per-source cursors. `WorkItem{ID, SourceKey, Kind, TitleKey/Title, Reference, Status, Priority, DueAt, UpdatedAt, Href}`; modules implement it in their `public` package and the composition root registers it; sources of disabled modules are skipped (ADR-0032 `Require`). Titles are returned only after the module applied its own redaction.
- `servicedesk/public.WorkItemSource`: items are open Tickets (`new|open|in_progress|waiting`) that are assigned to the caller, or **accepted** by the caller (`start` assigns the caller when nobody is, so "accepted" equals "assigned to the caller"; no new relation is introduced). Tickets assigned to one of the caller's Teams as queue only (not yet accepted) appear in the sidebar queue and in My Work under an "unassigned in my queues" count, not as the caller's own items. Waiting tickets are shown with their waiting reason code and sort after active ones.
- My Work response: `GET /api/v1/my-work` stays compatible (tasks, existing shape) and gets `GET /api/v1/my-work/items?sources=tasks,tickets&cursor=` returning merged items sorted by shared order (due/SLA date nulls last, priority rank, updated). Overview metric cards use `Count` per source and keep honest labels (counts are exact from the source, not "loaded" counts, because the source provides a capped count). Permissions: each source applies its own scope; My Work has no extra permission.
- No second task system: Tickets stay Tickets; My Work only lists them. Ticket lifecycle actions open the Ticket detail.

## HTTP API sketch

All under `/api/v1`, handlers authorize in the application layer; error codes `query.invalid_filter`, `query.too_complex`, `query.field_unavailable`, `query.invalid_cursor`, `query.unindexed_sort`, `views.not_found`, `views.conflict` (version), `views.not_permitted`, `views.limit_reached`.

- `GET /{resource}/fields` — catalog for the caller.
- `POST /{resource}/query` — body `{filter, search, sort, columns, cursor, limit}` → `{items, nextCursor, warnings[]}`; per resource (tickets, devices, assets, tasks first). Existing `GET` list endpoints are kept until migrated.
- `GET/POST /views`, `GET/PATCH /views/{id}` (`expectedVersion`), `POST /views/{id}/archive|restore|duplicate`, `GET /views/{id}/results?cursor` (runs the stored definition for the viewer), `PUT /views/{id}/shares` (full replace, `expectedVersion`), `GET /views?resource=&scope=mine|shared|system`.
- `GET /views/counts?ids=` — capped counts.
- `GET /me/sidebar` → groups with pinned items (user pins, applicable Pin Rules, system Views), counts omitted; `PUT /me/pins` (order, group, hidden), `PUT /me/sidebar-state`.
- `POST|DELETE /views/{id}/pin-rules` (`views.pin_for_groups`).
- `GET/POST /tasks/boards`, `GET/PATCH /tasks/boards/{id}`, `POST /tasks/boards/{id}/columns` operations `add|rename|reorder|remap|remove`, `GET /tasks/boards/{id}/cards?column=&cursor`, `PUT /tasks/boards/{id}/ranks`. Card moves call the existing `POST /tasks/{id}/start|block|unblock|complete|cancel|reopen`.
- `GET /my-work/items`.
- OpenAPI additions in `api/openapi/openapi.yaml` (`Query*`, `View*`, `Board*`). `views` routes are core (always on, like My Work); a resource's `/query` route belongs to the owning module and is hidden with it.

## UI sketch

`frontend/src/platform/ui/query` (shared) and `frontend/src/modules/views`; functional first (visual design postponed per project decision), semantic tokens only, i18n for every string.

- **Filter builder:** a panel under `FilterBar`: rows `[field select] [operator select] [value control]` where the value control follows the type (text, number, date picker, relative date number plus unit, enum multiselect, reference picker, boolean); group box with AND/OR toggle, "add condition", "add group", remove; up to depth 4 and 25 conditions with inline limit messages. Fields and operators come from `/fields`. A text search box on top = `search`. Active conditions are summarized as chips (existing `ActiveFilter`). The builder is controlled by a pure model (`filterModel.ts`: AST ⇄ UI state, validation mirroring server limits) with unit tests.
- **View bar** above each list: view selector (system, mine, shared), "unsaved changes" indicator, Save, Save as, Share (dialog: users/Teams/roles/everyone, level), Pin to sidebar (own; admin: "Pin for…"), columns chooser, sort. URL carries `?view=<id>` or a compact filter state (`?q=<base64 AST>` for ad-hoc filters; sharing via link works only through saved Views because ad-hoc AST links are validated like any other request).
- **`DataTable`** gains server-driven sorting (`onSortChange`) instead of client `sortRows` when a resource is query-backed, column chooser support and cursor "load more" (already present); `filterQuery.ts` stays for simple legacy filters until migrated.
- **Sidebar:** grouped, collapsible, pinned Views and Queues with count badges; drag or keyboard reorder in a "Manage pins" dialog.
- **Board screen:** columns with cards (title, reference, assignee avatar, due date), drag and drop with a keyboard alternative (card menu "Move to column…"), reason dialogs through the existing `ReasonDialog`, conflict toast on version mismatch, per-column count and "load more", Board settings dialog (columns, mapping, sharing, linked View).
- **My Work/Overview:** ticket rows with a source badge; metric cards drill into the source list.

## Slices

1. **Q-A Query engine and first catalogs:** `platform/query` (types, validator, compiler, cursor, limits), catalogs and `/fields` + `/query` for tickets, devices, tasks (and assets if the Inventory list fits the same pattern), compatibility mapping of old list parameters, frontend filter builder and server-driven `DataTable` sort for those lists, Work Item source contract with Tasks and Tickets sources and `/my-work/items` (the My Work/Overview ticket gap closes here because it needs no Views). Tests: operator matrix per type including NULL semantics, injection corpus, cost-limit rejection, cursor tamper, field-permission matrix and bisection oracle test, `EXPLAIN` checks, scope-not-widenable tests. Docs: ADR-0033 accepted, module-boundaries row, glossary (Field Catalog, Filter, View, Board, Queue, Pin), current-status.
2. **Q-B Views, sharing, pins:** migration 000060 (`views` schema), `platform/views`, permissions, audit, events, notifications, View bar UI, share dialog, pins, pin rules, archive purge job. Tests: share authorization matrix, IDOR, revocation, membership change, version conflicts, unreadable-field degradation.
3. **Q-C Queues, numbering and sidebar:** Queue entity, grants, routing rules, per-queue counters and reference registry (migration 000061, forward-only, default Queue seeded from existing `TKT-` numbers), `MoveToQueue` with aliases, Queue-aware visibility predicate, Ticket create with Queue choice and automatic routing, Queue admin UI, by-reference resolution, `/me/sidebar`, counts endpoint with cache, grouped collapsible shell with Queue counts, collapsed-rail flyouts, Autotask queue mapping contract. Tests: concurrent numbering under load (N goroutines, no duplicates, no reuse after rollback or archive), migration of existing Tickets keeps every reference, alias resolution after chains of moves, grants matrix (create without view, global `tickets.view` compatibility), move permission and assignee clearing, ID oracle (404 parity for invisible Tickets and unknown references), routing rule determinism, accessibility tests.
4. **Q-D Kanban:** Board tables (migration 000062), Board/column/rank operations in the tasks module, Board screen, card-move to lifecycle-operation mapping, sharing through Views. Tests: forbidden transitions, concurrent move, visibility of cards, rank ordering, limits.
5. **Q-E Remaining catalogs:** assets, software, requests, changes, approvals, knowledge, procurement, security findings, and so on, one PR per module family; each needs the catalog review checklist (permissions, redaction, indexes). Gate: a docs-check/test that lists every list endpoint without a catalog so unfinished lists are visible.

## Reused concepts

Principal/authorization and scope evaluation, Organization Teams/roles/users (shares resolve through `organization/public` `WorkDirectory`), module registry (`Require`, `/modules/status`), audit, outbox events, notifications, jobs, `safetext`, `ReasonDialog`, `DataTable`, `FilterBar`, existing Task lifecycle operations, existing cursor conventions, My Work and Briefing read models.

## New concepts

Field Catalog, Filter AST, Saved View, View Share, Pin and Pin Rule, Work Item source contract, Board (with Columns and ranks). Ticket Queue (V11) is a Service Desk table, distinct from Views. All are added to the glossary in Q-A/Q-B/Q-D.

## Open decisions (proposed defaults)

1. Can a user create a View on a resource they can only partly read? Default: yes, scope is always applied at run time.
2. Everyone-visible Views: who may publish? Default: `views.publish` (elevated) only.
3. Do Team members automatically get Team Boards? Default: yes (implicit `use`), `edit` needs `tasks.boards.manage_team`.
4. Should "accepted by me" for Tickets become a separate acceptance step? Default: no, assignment (including `start`) is acceptance.
5. Ad-hoc filter links (`?q=`) allowed? Default: yes, validated like any request; saved Views are the supported sharing mechanism.
6. Counts freshness. Default: 15 s cache, 60 s poll, capped at 1000+.
7. Export of View results (CSV)? Default: out of F13; needs its own decision (privacy, rate limits, allow-list).
8. Cancelled tasks on Boards. Default: hidden unless a column maps to `cancelled`.
9. Existing list endpoints removal timeline. Default: keep one release after the UI has moved, then remove per resource.
11. Number format: minimum padding 4 and `-` separator? Default: yes. Move gives a new number (V13)? Default: yes, with permanent aliases; the alternative (keep the original number) is documented above.
12. Legacy prefix for existing Tickets: keep `TKT` on the migrated default Queue? Default: yes; renaming would break every issued reference.
13. Should a requester see the new Queue number after a move? Default: yes if the Queue is public, otherwise a neutral desk label.
10. Is `pg_trgm` acceptable? Default: yes (extension of the existing PostgreSQL, no new dependency); otherwise `contains` is limited to small tables. **Resolved: yes** (see "Resolution after the Q-A review").

## Review-risk list

- **SQL injection:** only catalog expressions and bind parameters; operators from a fixed map; the catalog test rejects non-allow-listed expressions; fuzz/injection corpus on every text operator including LIKE metacharacters and unicode; `ORDER BY` built only from catalog sort keys.
- **IDOR via shared Views:** every View read checks owner or Share; 404 otherwise; shares resolved by membership at read time; counts and results run as the viewer; View ids never imply data access.
- **Filter-based oracle leaks:** fields without permission are absent from filter, sort, search and counts; masked fields support no range or prefix operators; errors do not distinguish "field exists but forbidden" from "unknown field"; saved conditions on newly unreadable fields evaluate to no rows, never dropped; search covers permitted fields only; reference fields do not allow `contains` across modules.
- **Performance:** bounded conditions, depth, IN size, statement timeout, unindexed-sort rejection, capped counts with short cache, pg_trgm only where justified, `EXPLAIN` regression tests for default Views, per-principal rate limits on `/query` and `/counts`.
- **View visibility changes:** unsharing, owner leaving (View becomes archived, shares stop; `views.admin` can take over), Team deletion, role loss, field removal in a later release (schema_version migration of definitions), shared-edit surprises (audited, "edited by" note, duplicate option).
- **Stored definition abuse:** size cap, canonicalization, strict decoding, no HTML in names (`safetext`), no filter values in audit or logs.
- **Board integrity:** drag-and-drop never writes status directly; transitions go through Task operations with `expectedVersion`; rank updates are presentation-only; column mapping cannot map to a status the Task machine cannot reach without UI pre-checks and backend enforcement.
- **My Work leakage:** sources apply their own scope; the merged feed never calls another module's tables; disabled modules are skipped.
- **Number collisions under concurrency:** counter row lock inside the creating transaction, unique `(queue_id, number)` and global registry, retry once on unique violation, load test; no reuse after archive; prefix frozen once used; migration verified by a before/after reference diff.
- **ID oracle:** sequential per-queue numbers reveal volume per desk; lookup by reference or alias answers identical 404 for unknown and invisible Tickets; autocomplete over references only inside visible Queues; public APIs never return counters or `next_number`.
- **Moves and permissions:** assignee loses access to the target Queue, requester sees internal Queue names, move used to dump a Ticket into a Queue the actor cannot see, alias resolution revealing a Ticket's previous Queue to someone who no longer may see it (the alias lookup applies the current visibility), bulk move on archive.
- **Global permission compatibility:** `tickets.view`/`tickets.manage` keep working as global grants; a Queue-level design must not silently narrow or widen current roles.
- **Module boundaries:** `platform/query` and `platform/views` import no module; catalogs live in modules; `make archcheck` rules added in Q-A and Q-B.
- **Privacy:** Presence data excluded (ADR-0028 W8); no Views over personal absence data; sidebar counts never expose names.

## Review outcomes

Independent review (2026-10-08) found nine issues; all are resolved in this document.

1. **Queue grant matrix.** Levels `create` (independent), `view`, `work`, `manage` (cumulative: manage includes work includes view). Read ticket and comments: `view`; work lifecycle, assign, internal comments: `work`; create into a Queue: `create`; move: `work` in the source and `create` in the target, plus ticket-level access. Ticket, source Queue and target Queue are authorized in the same transaction as the number issue (rows locked in id order).
2. **Row-specific disclosure.** A field whose visibility varies per row (for example the Queue of a Ticket for employees who see only their own tickets) is not filterable, sortable, searchable or countable for those callers, and aliases and queue labels are returned only after the same per-row disclosure check; reference lookup of an alias applies the current visibility.
3. **View execution.** Share access and resource scope are re-checked at every execution; authorization and the definition version are read in one snapshot (one `REPEATABLE READ` read or one version-guarded query). Count cache keys and invalidation are specified in the Compiler section.
4. **SQL allow-list.** Replaced the pattern idea by trusted catalog code with closed expressions, closed join graph, separate projection/filter/sort declarations and bind-only values.
5. **Cost bound.** A field exposes only operators that have an index or a measured plan (catalog `IndexHint` per operator); expensive combinations (a broad OR with an unindexed `contains`) are rejected as `query.too_complex`; counts use a capped subquery; `statement_timeout` and a per-caller rate limit apply.
6. **Keyset.** Lexicographic per-key predicates with explicit NULL branches; tests for ties, NULLs, mixed ASC/DESC and updates between pages.
7. **Permissions.** `views.pin_for_groups` is the single permission for group pins; card moves require `expectedVersion`; rank writes re-check Board edit access and task visibility.
8. **My Work contract.** Per-source continuation cursors with deterministic tie-breakers, capped and failable counts, authorization on items and count, module state checked at execution.
9. **Numbering and modules.** Only committed references are permanent; migrations are reserved once (000060, 000061, 000062); Views and fields of modules disabled through ADR-0032 are not executable (the query endpoints are hidden with the module, the sidebar drops their entries, and a stored View of a disabled module reports `views.module_disabled`).

## Q-B implementation record (2026-10-09)

What was built and where it differs from the sketch above. [Current status](current-status.md) lists the routes.

- **Execution goes through the owning module's own query endpoint.** `platform/views` holds no catalog and no module code. `GET /views/{id}/results` resolves access, then calls the resource's `/fields` and `/query` endpoints in process (`HTTPRunner`, wired in `internal/wiring/views.go`) with the viewer's session cookie. The module authenticates the call as the viewer and applies its own permission, row scope, redaction, rate limit and error codes, so a View can never return more than a direct `POST /{resource}/query` of the viewer, and the views platform never reads a module table. The same dry run (one row) validates a definition on create and update, so a View can only be saved with conditions the saver could run; the module's query errors are passed through unchanged.
- **Degradation is done by the views platform on the viewer's `/fields` catalog** (not by the module's strict endpoint): a condition on a field, operator or enum value the viewer's catalog does not offer becomes FALSE and is reported as `query.field_unavailable` (AND group: the group is FALSE and no query runs; OR group: the branch is removed). Unusable sort keys are removed with a warning. Conditions are never dropped, so a lost permission only narrows a result.
- **Access** is computed in SQL from owner, shares and the viewer's current Teams and roles (roles through `platform.role_assignments` of the User and the User's Directory Groups). `views.use` is not a registry permission: every signed-in User may use Views of a resource they may read (a resource declares `Use` permissions in the composition root; Tickets: any signed-in User). New permissions: `views.share`, `views.publish`, `views.pin_for_groups` (the only one for group pins), `views.admin`. A View whose owner was deactivated stops being shared with anyone; `views.admin` sees, archives and takes it over but never runs a View that is not shared with them.
- **Concurrency.** One `version` counter covers definition, name, shares, archive and ownership; every mutation requires `expectedVersion` (missing: 400, stale: 409 `views.conflict`), locks the View row, re-derives the caller's level under the lock and audits in the same transaction. Replacing shares needs the permission per added share (removal needs none), and an unchanged set is a no-op. A running View re-reads access and version after the module query: revoke or archive in flight answers 404, an edit in flight answers 409, and the rows are discarded. Creation per owner is serialized with an advisory lock for the 200-View limit.
- **Cache keys (for the counts of Q-C).** `views.CacheKey(viewID, version, principalID, permissions, teamIDs, roleIDs, extra...)` hashes every input that can change what a viewer sees, order-independently and length-prefixed; the 15 s TTL (`views.CacheTTL`) only bounds staleness of the rows themselves. Because the View version, the principal and the membership fingerprints are in the key, a revoked share, an edited View or a membership change can never be answered from an older entry and needs no explicit purge. Q-A and Q-B cache nothing yet.
- **Pins.** Group keys are the registered resources' groups (`tickets`, `tasks`, `endpoints`) and `work`. `PUT /me/pins` replaces the user's own rows all or nothing; `hidden: true` on a rule-pinned View is the user's override. The sidebar response contains pins only (no counts, no system Views, no Queues until Q-C). Pins of Views the user may no longer use are omitted and disappear with the next replacement.
- **Audit and events.** Audit actions as designed (`views.view.created|renamed|definition_changed|archived|restored|ownership_taken|purged`, `views.share.granted|revoked`, `views.published`, `views.pin_rule.created|deleted`) with ids, resource, version and counts only; the audit test checks that neither filter values nor names appear. Events `ViewShared` and `ViewArchived` are in the outbox; the in-app notification "X shared a view with you" is not built yet. The retention job `views.purge_archived` runs daily in the worker.
- **UI (built 2026-10-09):** see [UI design system](../development/ui-design-system.md#saved-views-and-sidebar-f13-q-b-ui): view bar, Views list, save/save as/share/pin, per-View Pin Rule dialog and the grouped sidebar. Differences from the sketch: the sidebar "Manage pins" dialog is a context menu (move up/down, unpin); the sidebar's groups are the Work, Service Desk, Assets & Endpoints, Infrastructure & Changes, Security, Knowledge, People and Administration sections with a Pinned section above them.
- **Not built:** notifications, `GET /views/counts`, system Views and Queue entries in the sidebar (slot reserved), Boards, a restore screen for archived Views. `GET /views?scope=system` of the sketch arrives with Q-C.

## Q-C implementation record (2026-10-09)

What was built and where it differs from the sketch above. [Current status](current-status.md) lists the routes.

- **Queue model.** `servicedesk.queues`, `queue_grants` and `reference_registry` as designed; `queue_teams` and `queue_routing_rules` are not built. The Queue's Team is a hint: `default_team_id` is written to the Ticket's `queue_team_id` (the routing Team) on creation and on a move, and `queue_team_id` stays what `POST /tickets/{id}/assign` (`queueTeamId`) and the list parameter `queueId` mean. In the ticket catalog the field `queue` is the Queue and `routing_team` the Team hint; `queue` has no `is_empty` operator. `routing_mode` only decides who may pick a Queue: `employee_choice` and `both` appear in the employee intake list, `automatic` Queues are chosen by people who view them or by the intake default (without rules, `automatic` means "not offered to employees"). The migration seeds Queue `it` (prefix `TKT`, width 6, public, `both`, intake) with the counter above every existing number and the old sequence value; it raises if a reference does not match `TKT-<digits>`.
- **Numbering** is in the database: `servicedesk.issue_reference(queue)` (row lock, `SD404` for an unknown or archived Queue) is called by a BEFORE INSERT trigger, so any insert path (also SQL) gets a number from the Queue counter in its own statement and transaction; an AFTER INSERT trigger registers it; a BEFORE UPDATE trigger allows Queue, number and reference to change only together; an AFTER UPDATE trigger turns the old reference into an alias. Key, prefix, padding and the id of a Queue are frozen and the counter cannot move backwards. The service retries a Ticket creation once on a unique violation. Tests: 80 parallel creations in two Queues without a duplicate or gap, rollback leaves no gap, delete, archive and restore never reuse a number.
- **Authorization.** `servicedesk.queues.manage` (elevated) administers Queues; levels `view`, `work` and `manage` are cumulative and `create` is independent; the level `manage` currently behaves like `work` (reserved for Queue-local administration). Global `tickets.view` is a view grant and `tickets.manage` a work grant in every Queue, including future ones. A caller's access is read in the transaction that decides the operation, after the Ticket row lock, from User, Team and role grants (Teams from Organization, roles from the permission evaluator). Employees who can raise into a Queue need no view: public Queues grant `create` implicitly. An unknown, archived (for non-viewers) and forbidden Queue are one answer (`servicedesk.queue_not_permitted` on create and move, 404 `servicedesk.queue_not_found` on read), so Queue ids cannot be probed.
- **Move.** `MoveToQueue`: version required; Ticket row, then source and target Queue rows locked in id order; needs `work` in the source and `create` or higher in the target (or `servicedesk.queues.manage`); resolved, closed and cancelled Tickets are not moved; an assignee who cannot view the target (global permissions or grant) is cleared and an `in_progress` Ticket becomes `open`; the routing Team becomes the target's default Team (or empty). Assigning someone who cannot view the Ticket's Queue is refused (`servicedesk.assignee_no_queue_access`). Reason codes: `misrouted`, `different_skill`, `reorganization`, `other`. Archiving needs no open Tickets and the lock order makes the archive/move/create race safe (tested). Bulk move on archive is not built.
- **Row-specific disclosure.** The Queue of a Ticket, its number and its routing are returned only after a per-row check. A caller may know a Queue when they view it, hold any grant in it, or it is public; otherwise the Ticket carries `queueLabel` (the Queue's public label, default its name) and `reference` is the newest number issued from a Queue the caller may know (the oldest if none), `queue` and `queueId` are absent and aliases from unknown Queues are not listed. `queue`, `routing_team` and `assignee` in the catalog are filterable only for callers who view at least one Queue; for callers who view only some Queues, a request that uses one of them is narrowed to the Tickets of the Queues they view (their own Tickets elsewhere drop out), so a filter cannot reveal the routing of an undisclosed Ticket. `GET /tickets?queue=<id>` (and `QueryScoped`'s `inQueue`) narrows to one viewed Queue; an unviewed one answers like an empty one. Reference lookup is `GET /tickets/by-reference?reference=` (the path form of the sketch collides with `/tickets/{id}/known-errors` in the router); the alias search inside the catalog field `reference` is not built.
- **System Views and counts.** `views.SystemProvider` (composition root registers `servicedesk/public.SystemViews`) offers per caller `system:tickets:my-open`, `system:tickets:unassigned` and one `system:tickets:queue:<id>` (handle `queue:<id>`) for each active Queue the caller views, only to callers who view Tickets beyond their own. They are ordinary Filter ASTs run through `POST /tickets/query` as the viewer, so the visibility and narrowing rules above apply. `GET /views/counts` and the counts of `GET /me/sidebar` cache the capped counts of System Views for `views.CacheTTL` per (key, principal, permissions, Queue scope digest), bounded to 5000 entries; saved View counts are not cached. At most 20 sidebar entries are counted per request. A count that fails (rate limit, timeout, module error) is `unavailable`, never 0, and ids the caller does not have are omitted from `/views/counts`.
- **My Work.** `platform/workitems.Service` merges the sources; the contract is `Key`, `Module`, `Items(principal, cursor, limit)` and `Count(principal)`. Sources: `tickets` (open Tickets assigned to the caller), `tasks` and `team_tickets` (unassigned open Tickets routed to one of the caller's Teams, the "to pick up" list); each row is authorized again for the caller (Ticket assigned to you but its Queue no longer viewable: not listed, not counted). Shared order: due date (none last), rank (priority; waiting Tickets after active ones), id. The composite cursor holds one position per source, advanced only by the items returned; a failing source is listed in `unavailable` and keeps its position, so its items arrive later. Disabled modules are skipped before the source runs. `GET /my-work/items` and `GET /my-work/counts` are core routes (prefix `my-work`); the old `GET /my-work` keeps its shape. Not built: Approvals, Requests and Changes sources.
- **Audit and events.** `servicedesk.queue.created|updated|archived|restored|made_default|grants_replaced` (ids, version, key and prefix on create, changed property names, grant counts; no names), `servicedesk.ticket.queue_moved` (from/to Queue ids, old and new reference, reason code, whether the assignee was cleared). Events: `TicketCreated` gains `queueId`; new `TicketQueueChanged`.
