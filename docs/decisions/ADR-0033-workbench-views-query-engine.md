# ADR-0033: Lists are queried through a platform query engine with module-declared field catalogs, and Saved Views are a platform concept

- Status: Accepted (2026-10-08). Q-A backend query engine, Tickets, Devices and Tasks catalogs and the Q-B backend for Saved Views, Shares, Pins and Pin Rules and the Q-C backend for Ticket Queues, System Views, sidebar counts and the My Work source contract are implemented; the UI and Boards remain planned. See the [F13 design](../product/f13-workbench-views-design.md).

## Context

Every list in Turaco (Tickets, Devices, Assets, Tasks and so on) has its own hand-written filter parameters, ordering and cursor. Users need every property of every list to be searchable and filterable with real operators, to save a filter as a View, to share and pin Views in the sidebar, to run Task Boards (Kanban) and several Ticket queues with counts, and to see accepted Tickets in My Work. Building this per module would create ten parallel filter systems, and exposing a generic filter language naively would allow SQL injection, access to private columns and "oracle" leaks (filtering on a field the caller may not read).

## Decision

1. New platform packages `backend/internal/platform/query` (field catalog, filter AST, validator, SQL compiler, keyset pagination) and `backend/internal/platform/views` (Saved Views, View Shares, Pins, schema `views`). Neither imports a business module (`make archcheck`).
2. A module **declares** a Field Catalog per Resource in its own code (a plain table of field key, type, operators, sortable, filterable, searchable, required permission, redaction rule, allow-listed SQL expression, optional Join). The client sends only field keys and values. SQL text is built only from catalog entries and always uses bind parameters. There is no endpoint that accepts raw SQL, raw column names or free ORDER BY.
3. The filter is a JSON **Filter AST** (groups of AND/OR with conditions), validated server-side against the catalog with hard cost limits, and compiled once. The module's repository executes the compiled predicate inside its own query, together with the module's mandatory **visibility predicate** (row-level scope), which is ANDed outside the user's filter and can never be removed by it.
4. A field the caller may not read is not filterable, sortable or searchable for that caller and does not appear in the catalog response for them (fail closed, no oracle).
5. **Saved View** is one platform concept: owner, Resource, Filter AST, sort, columns, version. Visibility is `private` or shared through View Shares (user, Team, role, everyone). A View stores intent only; results are always computed with the caller's own authorization. Pins are per-user rows; administrators can pin for groups through a share-like pin rule. Audited operations: create, change, share, unshare, publish, pin for group, delete.
6. **Task Board** is not a new list system. A Board is a Saved View over Tasks plus a column mapping; a **Ticket Queue** is a distinct Service Desk entity (own prefix and number sequence, Teams, per-queue grants; moving a Ticket issues a new number and keeps the old as an alias), not a Saved View. Views may filter across Queues and the sidebar shows counts for both. Moving a card calls the existing explicit Task lifecycle operations; no generic status update is introduced.
7. **My Work** gains a **Work Item source contract** (`platform` interface, implemented by each contributing module's `public` package). Tasks and Tickets are the first sources. My Work remains a read model; it creates no parallel task or notification concept.

## Consequences

- One query model for all lists; a module becomes searchable and filterable by publishing a catalog and running the compiled predicate. Existing list parameters stay as thin compatibility wrappers until migrated.
- Catalogs are security-relevant code: every field has a reviewed permission and redaction rule, and a test asserts that each SQL expression is allow-listed and each catalog is covered by an authorization matrix test.
- Saved Views can reference fields that later disappear or become unreadable; they degrade to "invalid condition" markers, never to widened results.
- Cost limits (conditions, depth, IN list size, statement timeout) bound the damage of a pathological filter; index guidance is part of each catalog.
- Cross-module field joins (for example Ticket by Device property) are out of scope; a module exposes only its own columns plus explicitly declared public lookups.

## Review outcomes (2026-10-08)

- Catalog code is trusted and uses a closed expression set and join graph validated at startup; no pattern-based identifier allow-list.
- Fields whose visibility varies per row are neither filterable, sortable, countable nor searchable, and alias or label lookups apply the same disclosure check.
- Views re-check share access and scope at every execution; count caches are keyed by principal scope, permission and membership fingerprint, and definition version.
- Only operators backed by an index or measured plan are exposed; keyset pagination uses lexicographic predicates with explicit NULL handling.
- Group pinning has one permission (`views.pin_for_groups`); Queue numbers are permanent once committed; Views of modules disabled under ADR-0032 are not executable.
