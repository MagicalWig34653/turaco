# Module Switches (design and implementation notes)

Status: backend implemented (2026-10-08); the administration UI follows. Decision: [ADR-0032](../decisions/ADR-0032-module-switches.md).

## Goal

An administrator sees all modules in one overview and switches each optional module on or off at runtime. Off means hidden and paused, never deleted.

## Concepts reused

Permissions and roles (`modules.manage`, elevated; built-in administrator roles receive it automatically), the audit log, `expectedVersion` optimistic concurrency, the job runner, the API error envelope. New concepts: only the **module registry** (`platform/modules`) and its table.

## Catalog

| Category | Modules (key: requires) |
|---|---|
| core (always on) | platform, access, organization, audit, tasks, approvals, notifications |
| service_management | servicedesk: assets; knowledge: servicedesk; catalog: products; requests: catalog |
| assets_inventory | products; assets: products; procurement: products; inventory: assets, procurement, products |
| endpoints_security | endpoints: assets, changes; security: endpoints, changes; remoteaccess: endpoints, assets, servicedesk |
| infrastructure_operations | infrastructure: assets; services: assets, infrastructure; changes: assets, infrastructure, services; planning: changes, procurement, services |
| workforce | presence (default off) |
| insight | briefing; ai (default off) |

Route prefixes, retention jobs and startup gates are in `backend/internal/platform/modules/catalog.go`.

## State

`platform.module_switches (module_key PK, enabled, version >= 1, reason_code, updated_by, updated_at)`, migration `000059`. No row = catalog default, version 0. The upgrade migration seeds `presence` and `ai` from `presence.settings.enabled` and `ai.settings.enabled` with reason `upgrade_default`.

States shown in the overview: `enabled`; `disabled` (switch off); `blocked` (switch on, a precondition is unmet; behaves as off). `blockedReason` is also shown for an off module so the UI can explain why it cannot be enabled yet: `startup_gate_off` (PRESENCE_ENABLED / AI_ENABLED false), `dpia_not_recorded`, `no_enabled_provider`, `no_providers_configured`, `runtime_setting_off` (presence.settings.enabled / ai.settings.enabled is off, so `/modules/status` never claims a module is on while it refuses use), `dependency_disabled`.

Lifecycle: see [state machines](../domain/state-machines.md#module-switch).

## Jobs and outbox consumers while a module is off (review outcomes)

Each job type `<key>.*` has one policy in the catalog:
- **always run** (`AlwaysRunJobs`): safety and retention paths: `presence.purge`, `ai.sessions.expire`, `ai.retention.purge`, `remoteaccess.expire_sessions`, `endpoints.deployment_tick`. The deployment tick runs only the kill-switch sweep (pause, halt, queue clearing) while Endpoints is off and starts no new rollout work.
- **disposable** (`DisposableJobs`): recurring scheduled ticks (`endpoints.software_package_sync`, `endpoints.deployment_correlation`, `security.advisory_sync|match_all|risk_review_reminders`, `changes.reminders`, `remoteaccess.observe`): completed without running; the schedule enqueues the next one.
- **durable** (everything else, e.g. `security.match`, `servicedesk.external.push`, `services.vm_link_backfill`): kept pending (attempt refunded, offered again every minute) and run after the module is enabled. Nothing is lost.

Outbox consumers named `<key>.*` of a disabled module do not run. The dispatcher hands the event, in its own transaction, to a durable job `<key>.deferred_event` for exactly that consumer (dedupe by event and consumer), so other consumers of the same event (for example the core task notification) are not blocked and the event is delivered after the module is enabled. Core consumers always run. `AlwaysRunConsumers` can exempt a safety or cleanup consumer; none exists yet. Briefing feed results are cached per principal and per set of active sources, so a switch is visible at once.

## API (OpenAPI is authoritative)

- `GET /api/v1/modules/status` (signed in): `{items: [{key, enabled}]}`.
- `GET /api/v1/admin/modules` (`modules.manage`): `{items: [Module]}` with key, nameKey, descriptionKey, category, core, enabled, switchOn, state, blockedReason, requires, requiredBy, startupGates, version, changedAt, changedByUserId, reasonCode.
- `POST /api/v1/admin/modules/{key}/enable|disable` (`modules.manage`), body `{expectedVersion, reasonCode}`, reason codes `initial_setup|business_need|not_needed|maintenance|compliance_review|evaluation`. Returns the Module. Errors: 400 `platform.modules.invalid_request`, 404 `platform.modules.not_found`, 409 `platform.modules.version_conflict|no_change|not_switchable|blocked|requires_disabled|required_by_enabled` (the last two carry `error.blockers`).
- Routes of a module that is off: 404 `platform.module_disabled`.
- i18n keys for the UI: `modules.<key>.name`, `modules.<key>.description`, `modules.category.<category>`, `modules.blocked.<code>`, plus error codes above.

## Security and audit

Only `modules.manage` can read the overview or change a switch; the module key is not an owned resource, so there is no per-object access. Each change is one transaction with the audit record (action, actor, before/after, reason code, correlation id). A disabled module's endpoints do not reveal their existence to anonymous callers. Switching never grants access: permissions still apply once a module is on.

## Out of scope

Per-tenant or per-role module visibility, scheduled switching, deleting a module's data, and relaxing coarse dependencies.
