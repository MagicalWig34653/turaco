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

Read: `endpoints.view` (Devices, software, artifacts, assignment views). Administration of the integration and imports: `endpoints.integration.manage` (elevated). No management actions in F6. All endpoints are backend-authorized; an Assignment Path never reveals a User, Device or Group the caller cannot read (Devices need `endpoints.view`; Users and Groups are visible to holders of that permission only as names already visible in Organization). Employees see no endpoint data except their own Assets (F4).

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
