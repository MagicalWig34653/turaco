# ADR-0032: Optional modules are switched at runtime through a platform module registry

- Status: Accepted (2026-10-08). Backend and administration UI (`/admin/modules`) implemented.

## Context

Turaco is a modular monolith whose modules were always compiled in and mounted. Installations differ: a team that only needs the service desk should not see Procurement, Remote Access or Security, and an administrator must be able to turn a module on or off without a redeploy. Two modules already have their own gates (Workforce Presence: `PRESENCE_ENABLED` plus a recorded data protection impact assessment; Turaco AI: `AI_ENABLED`, a runtime setting and an enabled provider), and Remote Access needs configured providers. A module switch must not become a way around those.

## Decision

1. `backend/internal/platform/modules` is the **module registry**. Its catalog (plain data, no module imports) lists every module with a stable key, category, `core` flag, default state, dependencies (`requires`), the API route prefixes it owns, exact status-probe paths that stay reachable, retention jobs that keep running, and the startup gates that apply to it.
2. **Core modules** (`platform`, `access`, `organization`, `audit`, `tasks` with My Work and recurrence, `approvals`, `notifications`) are always on and cannot be switched. Approvals is core because Changes, Requests, Procurement, Planning and Deployments all depend on it; it is a platform workflow primitive.
3. Every other module is **optional** and has one switch, stored in `platform.module_switches` (key, enabled, `version`, reason code, `updated_by`, `updated_at`). A module without a row has the catalog default and version 0, so adding a module needs no migration. The upgrade migration seeds Workforce Presence and Turaco AI from their existing runtime settings; everything else stays on, so an upgrade changes nothing observable. Presence and AI default to off on new installations.
4. The lifecycle is two explicit operations, `Enable` and `Disable`, each with `expectedVersion` and a mandatory reason code, authorized by the new elevated permission `modules.manage`, and audited (`platform.module.enabled|disabled`, before/after and reason code). All changes are serialized by one advisory lock, so dependency checks cannot race. Enabling is refused while a required module is off; disabling is refused while an enabled module requires it; both errors list the blocking modules. No generic status update exists.
5. **Preconditions are never bypassed.** A module may register a precondition in the composition root (`internal/wiring/modules.go`): Presence needs `PRESENCE_ENABLED` and the recorded DPIA date, AI needs `AI_ENABLED` and an enabled provider, Remote Access needs configured providers. Enabling a module whose precondition is unmet is refused (`platform.modules.blocked`). A switch that is on while a precondition fails (later, for example) reports the state `blocked` and behaves as off. The overview shows the reason code. Environment startup gates remain read-only information.
6. **A disabled module is hidden, not deleted.**
   - HTTP: one gate in front of the router maps the first path segment below `/api/v1/` to the owning module and answers `404 platform.module_disabled` for signed-in callers (anonymous callers still get 401, so the state is not revealed). Core paths and unknown paths pass through; `/ai/status`, `/presence/status` and the administration routes needed to configure Presence and AI (`/presence/settings*`, `/ai/settings*`, `/ai/providers*`, `/ai/usage*`) stay reachable while the switch is off, still behind their own admin permissions, so a module can be configured before its switch can be enabled; functional routes stay hidden.
   - Background jobs: the job runner asks a gate after claiming a job. Per job type the catalog says: always run (retention, session expiry and the Deployment kill-switch sweep, which does only its safe part while Endpoints is off), drop (disposable recurring ticks) or defer (durable work stays pending until the module is enabled; nothing is lost).
   - Outbox consumers `<module key>.*` of a disabled module hand their event to a durable per-consumer job that is delivered after re-enable; other consumers of the event are not blocked.
   - Other modules: a module's hard dependencies are enforced by `requires`, so an enabled module never calls a disabled one. For the few optional reads, the Briefing feed drops the sources of disabled modules and AI tools of a disabled module answer "not found". `Service.Require` returns `ErrModuleDisabled` for further adapters.
   - Data: tables, audit and history are untouched; enabling again restores everything.
7. State is read through a short cache (2 seconds) per process, so a change made in another API instance or in the worker takes effect within seconds without a restart; a change made through a process is visible to it immediately. A read error fails closed (the gate answers 500, the job is retried later).
8. `GET /api/v1/modules/status` (any signed-in User) returns only key and effective enabled flag, so the UI can hide navigation. The overview and the switches are under `/api/v1/admin/modules`.

## Consequences

- One platform concept, no per-module toggle system. Presence and AI keep their own runtime settings; the module switch is an additional layer, and both must allow use.
- The dependency graph is coarse and follows the synchronous public contracts wired in `internal/wiring`. It can be relaxed later by an ADR when a dependency becomes optional.
- Deployment planning still reads the Security advisory summary of a disabled Security module (an unavoidable cycle in the graph, read-only counts); this is a documented limitation.
- Every new route family needs a catalog entry; a test scans the transport packages and fails otherwise.

See the [design](../product/module-switches-design.md).
