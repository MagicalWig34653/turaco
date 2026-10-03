# F6 Endpoint Intelligence — Feature Design

**Status:** Draft 2026-10-03; decisions E1–E4 were adopted by default (the user chose the fake-adapter path). Target design; [current status](current-status.md) is authoritative for what is implemented. Related: [Intune integration](../integrations/intune.md), [Intune Assignment Intelligence](../integrations/intune-assignment-intelligence.md), [ADR-0020](../decisions/ADR-0020-management-assignment-intelligence.md), [F4 design](f4-inventory-design.md).

## Decisions

- **E1 No tenant yet.** F6 builds everything behind the `integrations/intune` port: the contract (normalized records, never Graph DTOs), an in-memory `Fake` and a JSON import path (`turaco-admin endpoints import`) that feeds the same ingestion code as a real client. The Graph client (authentication, paging, delta, throttling) is an explicit, documented stub until a tenant and app registration exist.
- **E2 One new module `endpoints`** (schema `endpoints`) owns Devices, observations, software, Management Artifacts/Assignments/Filters/Observations and Device group memberships. Directory Groups and User memberships stay in Organization and are read through its public contract. Assets stay in the `assets` module; a Device links to an Asset by id (no foreign key, public contract for lookups).
- **E3 Expected applicability is a pure function.** `endpoints/application/evaluation` takes normalized assignments, memberships, filters and exclusions and returns `applicable | excluded | not_applicable | unknown` with confidence and reason codes. It stores nothing authoritative; results are computed per request (and may later be cached as a rebuildable table). Anything it cannot evaluate safely is `unknown`; filters are evaluated only for the small deterministic subset (see below).
- **E4 Slicing.** The 14 plan items are delivered in four slices; each is merged separately.

## Slices

1. **Devices and observations (items 1–3, 13):** Device identity (provider id, serial number, name, linked Asset), reconciliation findings (no Asset match, serial conflict, duplicate), OS/hardware/compliance observations with source and freshness, installed-software observations, software normalization (`software_products` plus aliases, deterministic matching, unmatched names kept as raw).
2. **Management model and ingestion (items 4–6 partly, 12 partly):** Artifacts, Assignments (include/exclude, intent, filter reference), Filters, Observations, Device group memberships; idempotent snapshot ingestion with tombstones and revision history; sync run record and job.
3. **Evaluation and views (items 7–9):** evaluator, Assignment Paths, Device / Group / User views separating Assigned / Expected Applicable / Observed, artifact reverse lookup with counts.
4. **History, diff, findings, queries (items 10–12, 14):** meaningful assignment history, Device-vs-Device and Group-vs-Group diff, provider-reported vs Turaco-derived findings kept distinct, device list filters (saved views reuse the platform mechanism if one exists, otherwise they stay list filters).

## Reused concepts

User, Directory Group and membership (Organization), Asset (F4), Audit, outbox events, job runner, permissions, i18n, external-reference style provenance (source, freshness, revision). No new task, notification, search or permission system.

## New concepts

Device (endpoint identity), Device Observation, Software Product / Alias / Installation, Management Artifact / Assignment / Filter / Observation, Device Group Membership, Sync Run, Finding. All except Finding are in the glossary; Finding is added with slice 1 (data-quality) and extended in slice 4.

## Data (forward migrations, schema `endpoints`)

Key constraints: unique `(provider, external_id)` per Device and Artifact; at most one current observation row per (device, kind) with history appended only when the normalized value changes; tombstone columns (`deleted_observed_at`) instead of deletes; check constraints on enums; software installation unique per `(device, software_product or raw name+version)`; assignment unique per `(artifact, provider assignment id)`; memberships as intervals like Directory Group memberships. Every provider-sourced row carries `source`, `observed_at`, `last_synced_at` and the provider revision where available.

## Evaluation scope (E3)

Supported: `all_devices`, `all_users`, group targets with membership from the current Device Group Membership and User Group Membership (nested groups via `directory_group_nesting`), exclude-over-include precedence as documented by the provider, app intent carried through. Filters: evaluated only for rules built from `device.platform`, `device.ownership`, `device.manufacturer`, `device.model`, `device.osVersion` with `eq/ne/startsWith/contains/in`; anything else yields `unknown` with reason `filter_unsupported`. Stale inputs (membership or device data older than the freshness limit) lower confidence to `low` and add reason `inputs_stale`.

## API and permissions

Read: `endpoints.view` (Devices, software); manual link/unlink and sync: `endpoints.manage` (elevated). Artifact and assignment views use the existing `endpoint.management.view`, integration credentials/configuration the existing `integrations.intune.manage` (slices 2–4). No management actions in F6. All endpoints are backend-authorized; an Assignment Path never reveals a User, Device or Group the caller cannot read (Devices need `endpoints.view`; Users and Groups are visible to holders of that permission only as names already visible in Organization). Employees see no endpoint data except their own Assets (F4).

## Audit, events, jobs

Audit: integration configuration, imports, manual device-to-asset link/unlink with reason. Sync runs are recorded in their own table, not per row in audit. Events: `DeviceLinked`, `ManagementAssignmentChanged` (meaningful changes only, not per sync read), `EndpointFindingRaised`. Sync is an idempotent job; large snapshots are ingested in batches inside short transactions.

## Security and privacy

Provider data is untrusted (length limits, safetext on display names, no HTML). Device names and installed software are business data; the user assignment of a Device is personal data and follows the Asset visibility rules. No credentials are stored in F6 (the Fake and import need none). The Endpoint Agent is a separate trust boundary and out of scope.

## Documentation obligations

Current status, glossary (Finding), state machines (sync run, finding), core data model, generated references, OpenAPI, `docs/integrations/intune.md` (status block: what is implemented vs stubbed), this document updated per slice.

## ADR

None expected: no new framework or dependency. ADR-0020 already covers the model.

## Not in F6

Graph client, Intune actions, script/remediation execution, undocumented Intune precedence rules, Endpoint Agent telemetry, live provider calls on page render.

## Slice 1 status

Backend implemented (migration 000038). Link/unlink reasons are codes (`serial_confirmed|correction|duplicate|wrong_asset|other`), a manual unlink blocks automatic re-linking until a manual link, an empty snapshot tombstones nothing, `last_checkin_at` alone is not a meaningful change. The sync runs in the request (no job yet). Software Products are registered through `Service.RegisterSoftwareProduct` only.

## Slice 1 review outcomes

Fixed (migration 000038 edited in place, unreleased):

- One ingestion run per provider: a session-level PostgreSQL advisory lock on a dedicated pooled connection; a second run (sync or import) answers 409 `endpoints.sync_running`. `last_synced_at` is written with `GREATEST`; tombstone candidates are locked in id order.
- Partial snapshots: an invalid record with a valid external id counts as seen; an unsafe device name becomes the placeholder `unnamed-device`; `Snapshot.Complete` gates tombstoning (sync true, import as given); tombstone guard: more than 10 live devices and more than half missing means no tombstones, reported as `tombstonesSkipped` (updates still apply); at most 50000 devices per snapshot.
- One live device per Asset (partial unique index; a lost race on auto-link becomes a `duplicate_device` finding, on manual link a 409). A tombstone drops a serial link (audited, history row) and keeps a manual link, which is re-validated on revival.
- Installations: bulk `unnest` upsert, batches limited to 2000 installations, tombstoned with their device, indexed `normalized_name` computed in Go for relink. Registering a product reconciles `unmatched_software` findings immediately; findings refresh their detail; partners of tombstoned devices are re-checked.
- Reappearing devices are a change (version bump, history row); a sighting sets the source. Serial-change, duplicate-serial, tombstone and revival unlinks are audited as `endpoints.device.unlinked` and published as the new `DeviceUnlinked` event (also for manual unlink).
- Auto-link skips personal devices, placeholder serials and disposed/lost/retired Assets; devices sharing a serial get no link and all get `duplicate_device`. Manual link needs `assets.view` as well, an existing non-terminal Asset, and `expectedVersion` on link and unlink.
- Audit: every run records `endpoints.sync.completed` or `endpoints.sync.failed` with counts; derived links use the actor `intune-sync` (or `endpoint-import`) with the triggering user in metadata `triggeredBy`.
- Indexes: findings `(status, id)`, `text_pattern_ops` serial index, `BEFORE TRUNCATE` guard on the history.

Accepted limitations: `endpoints.manage` stays the single permission and covers the sync (documented in the permission). Looking up Assets by serial number alone has no matching index in the Assets schema (its unique index starts with `product_id`); a forward migration in the Assets module should add one before large fleets. The manual-link `assets.view` check is done at the HTTP layer from the caller's permissions, not per Asset. Failed runs record counts up to the failure; applied batches are not rolled back (each run is idempotent).

## Slice 2 status

Backend implemented (migration `000040_management_model.up.sql`; 000039 was taken by the permission rename). No frontend, no evaluator.

- Tables (schema `endpoints`): `management_artifacts`, `management_filters`, `management_assignments`, `management_observations` plus append-only `management_observation_history`, `device_group_memberships`. The findings kind check gains `provider_reported_error`.
- Assignments are interval rows: unique current row per `(artifact, provider_assignment_id)` (`valid_until IS NULL`); a changed assignment closes the old row and opens a new one; unchanged writes freshness only. `target_group_external_id` is the provider's Directory Group external id (no foreign key; slice 3 resolves it to the organization group). If an artifact's assignments are unknown (`AssignmentsKnown=false`), any assignment is invalid (unknown target/mode, missing or unreported filter, duplicate id) or there are more than 1000, the artifact's previous assignments are left untouched and invalid ones are counted as `assignmentsRejected`.
- Ingestion order per run (one provider lock shared with device ingestion, `Sync` holds it across both): filters, artifacts with assignments (batches of at most 50 artifacts / 2000 assignments per transaction), tombstoning of missing artifacts and filters (Complete, non-empty list, half-of-live guard, reported-but-invalid objects are kept), observations (500 per transaction; only for live devices and artifacts, other records are counted as skipped), memberships (1000 per transaction; closed when missing from a Complete snapshot, same guard). Filter rules longer than 2000 characters or with unsafe characters reject the filter (a rule is never shortened); names with unsafe characters become the placeholder `unnamed-object`; raw statuses are shortened to 200 characters; unknown observation states become `unknown`.
- Observations a Complete snapshot (with at least one observation) no longer reports are retired (`retired_at`, same half-of-live guard, chunks of 5000 per transaction), excluded from counts, device lists and `provider_reported_error`, and revived by a later sighting; consumers also judge staleness from `last_synced_at` (slice 3).
- `ManagementAssignmentChanged` (owner `endpoints`) is published once per artifact and run when assignments were opened or closed, including a tombstone closing them and the first load; unchanged re-reads publish nothing. `provider_reported_error` is raised per device while it has live `failed`/`conflict` observations on live artifacts (detail holds counts only) and resolved when they clear, when the artifact is tombstoned or the device is.
- Audit: `endpoints.management_sync.completed|failed` with counts and the triggering user as actor, no provider text.
- API: reads under `endpoint.management.view` or `endpoints.manage`; `GET /devices/{id}/management-observations` additionally needs `endpoints.view` or `endpoints.manage`. The management ingestion is part of `POST /endpoint-sync` (still gated by `INTUNE_SYNC` and `endpoints.manage`).

Accepted limitations: a full snapshot is held in memory (bounded: 20000 artifacts/filters, 1000000 observations/memberships); the sync still runs in the request; no job.

### Slice 2 review outcomes

Fixed: complete snapshots retire unreported observations; an observation older than the stored one never rolls back state or raw status (freshness still advances) and history rows are written only when the normalized state changes (the latest raw status lives on the current row); `provider_reported_error` is reconciled once per touched device after the observation phase, in sorted batches of 500, and artifact tombstoning commits before its devices are reconciled; filters rejected or not reported in the run are not bound to assignments (`ResolveFiltersTx` only returns filters seen in the run); stale memberships and observations are closed in chunks of 5000 per transaction; `management_assignments.provider` (copied from the artifact) leads the group reverse-lookup index; a reopened assignment starts at `GREATEST(run time, previous valid_until)` (no `btree_gist` extension is installed, so no exclusion constraint); history CHECKs, `ON DELETE RESTRICT` for observations and memberships, and `findings_kind_check` added `NOT VALID` then validated. `POST /endpoint-sync` returns 429 `endpoints.sync_cooldown` within 30 seconds of the previous completed sync (`endpoints.provider_sync_state`, checked under the provider lock, `DefaultSyncCooldown`); a management ingestion failure after the committed device phase counts in `managementErrors` (HTTP 200); `targetGroupExternalId` is returned only to callers who also hold `organization.directory.view`; the artifact read returns the current assignments and at most 200 most recently closed ones; `ManagementApplicabilityChanged` is owned by `endpoints`.

Accepted limitations: the provider read loads the whole snapshot into memory, so the real adapter must stop paging at the size limits; the first sync publishes one `ManagementAssignmentChanged` per artifact; the detail of `provider_reported_error` findings (failed/conflict counts) is visible to everyone with `endpoints.view`.

## Slice 3 status

Backend implemented (no migration, no new tables, no frontend, no provider calls). Everything is computed on demand from local normalized data.

- Evaluator: pure package `endpoints/application/evaluation`, table-tested. Inputs: current assignments, the Device's provider-group memberships, the Device's primary User (holder of the linked Asset, via the Assets public contract `UserHolders`), the User's Directory Group memberships and group nesting (Organization public contract `DirectoryGraph`, bounded lookups, current intervals only). Include/exclude with exclusion winning, `all_devices`, `all_users`, nested groups (shortest trace), origin `device` vs `user`. Results: `applicable`, `excluded`, `not_applicable`, `unknown`; confidence `high|medium|low`; reasons are stable codes (`included_by_assignment`, `exclusion_overrides_include`, `user_unknown`, `inputs_stale`, `filter_unsupported`, ...). Unknown input (no User, unsupported/missing filter, missing filter input) yields `unknown`, never `not_applicable`. Inputs older than 48 h lower confidence to `low` and add `inputs_stale`.
- Filters: only a deterministic subset of the Intune rule language (`device.platform`, `ownership`, `manufacturer`, `model`, `osVersion` with `-eq`, `-ne`, `-startsWith`, `-contains`, `-in`, combined with `and`/`or` and parentheses); anything else is `filter_unsupported` and the applicability is unknown.
- Views (`endpoint.management.view` or `endpoints.manage`; Device data additionally `endpoints.view`/`endpoints.manage`; group and User views additionally `organization.directory.view`; Devices of Users additionally `assets.view`): `GET /devices/{id}/management` (items keep `assigned`/`assignments`, `expected` and `observed` as separate fields plus a `mismatch` of `assigned_not_observed|expected_not_applied|observed_not_expected`; filters `kind`, `state` (observed state or `none`), `expected`, `mismatch`; keyset by artifact id, `truncated` when the scan limit of 2000 ended a page early), `GET /devices/{id}/management/{artifactId}/path` (Assignment Path steps), `GET /directory-groups/{id}/management`, `GET /users/{id}/management`, `GET /management-artifacts/{id}/targets`.
- Redaction: without `organization.directory.view` a group reference is `{redacted: true}` (no external id, no name); the Assignment Path keeps User origin but anonymizes the User. Without device access the counts/examples of the reverse lookup are not shown (`evaluation.shown=false`). Unknown or malformed ids answer 404.
- Bounds: at most 500 candidate Devices are evaluated per request (`evaluation.truncated`, `candidatesTruncated`), 10 examples, 50 artifacts per Directory Group/User page, 20 Devices per User artifact. Truncation is always reported, never silent.
- Not done: finding kind `assignment_ineffective`, history/diff views, frontend, caching of evaluations.
