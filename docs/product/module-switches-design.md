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

States shown in the overview: `enabled`; `disabled` (switch off); `blocked` (switch on, a precondition is unmet; behaves as off). `blockedReason` is also shown for an off module so the UI can explain why it cannot be enabled yet: `startup_gate_off` (PRESENCE_ENABLED / AI_ENABLED false), `dpia_not_recorded`, `no_enabled_provider`, `no_providers_configured`, `dependency_disabled`.

Lifecycle: see [state machines](../domain/state-machines.md#module-switch).

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
