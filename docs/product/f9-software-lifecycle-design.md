# F9 Software Lifecycle and Patch Orchestration — Feature Design

**Status:** Draft 2026-10-06; decisions P1–P6 adopted by default. Slice G1 is implemented (backend, OpenAPI and UI; see [G1 implementation notes](#g1-implementation-notes)); G2–G4 are open (autonomous progress; revisit when a real Intune tenant and an IntuneGet instance exist). Target design; [current status](current-status.md) is authoritative for what is implemented. Related: [ADR-0027](../decisions/ADR-0027-software-management-providers.md), [ADR-0020](../decisions/ADR-0020-management-assignment-intelligence.md), [state machines](../domain/state-machines.md) (Software Approval Status, Deployment), [F6 design](f6-endpoint-intelligence-design.md), [F7 design](f7-infrastructure-change-design.md), [F8 design](f8-security-briefing-design.md), [workflow](../workflows/security-remediation.md).

## Decisions

- **P1 No tenant, no IntuneGet yet.** As with Intune (F6), Autotask (F5) and advisories (F8), F9 builds the internal side behind ports with in-memory fakes: a **Software Management Provider** port (`integrations/softwaremgmt`: search catalog, package a version, publish/update a package into a Management Provider, report package status) with a Fake and a `NotConfigured` placeholder, and a typed **Management Assignment Writer** port (`integrations/intune` extension: set/clear a ring's assignment for a Management Artifact, idempotent per operation id) with a Fake that updates the fake provider snapshot so the normal ingestion reads it back. The real IntuneGet connector and the Graph write client are documented stubs.
- **P2 Everything stays in the Endpoint module** (`endpoints`), as ADR-0027 decides: sub-packages for approvals, packages, deployments and rings. Cross-module needs go through public contracts.
- **P3 Maintenance windows reuse Change windows.** There is no scheduling subsystem (glossary: a Maintenance Window is the window of a Change). A Ring's promotion/execution gate may reference an **approved or scheduled Change**; the gate is open only while now is inside that Change's window (read through `changes/public`). A Deployment may also run without a window when the policy of its ring says `no_window_required` (pilot only).
- **P4 Dynamic Groups become Target Sets.** A **Target Set** is a saved, bounded device query (platform, OS version prefix, ownership, compliance, model, Directory/Device Group membership, linked Asset's location) plus optional explicit includes/excludes; it is evaluated on demand when a Ring resolves its targets (no continuous evaluation, no new platform subsystem). A Ring's targets are snapshotted at resolution (`resolving_targets → ready`) so results stay explainable.
- **P5 Turaco is the only writer of the assignments it orchestrates** (ADR-0027 open decision resolved): each Ring maps to one provider assignment (a group target or a filter) that only Turaco creates; reads still come back through the normal sync, and a write counts as Assigned only after read-back.
- **P6 Slicing.** Four slices, each reviewed and merged separately.

## Slices

1. **G1 Software approvals and packages:** Software Product approval status (`candidate → approved → deprecated → retired`, `blocked`), **version approvals** bound to installer SHA-256 (and publisher where reported): `pending → approved | rejected`, `approved → revoked`; a changed hash/URL/install command/detection rule creates a new version; approval of a version and executing a deployment are separate permissions; Software Package references (provider reference via `platform/externalrefs`, Software Version, installer hash, publish status, resulting Management Artifact id); the Software Management Provider port with Fake; catalog search/package/publish operations (typed, audited, idempotent); package status observations with source/freshness; UI for the approved software list and packages.
2. **G2 Target sets and ring planning:** Target Sets (CRUD, evaluation preview with counts and bounded examples, explain why a device is in/out), Desired State record per Software Version and Target Set, Deployment and Deployment Ring definitions (ordered rings, gate configuration: approval required, success threshold on fresh evidence, soak duration, optional Change window), validation rules (non-pilot ring needs an approved version; All Devices/All Users targeting and uninstall/supersede need the high-impact permission plus an Approval), draft/scheduled lifecycle, UI wizard.
3. **G3 Execution:** ring execution state machine (`pending → active → awaiting_promotion → promoted | halted`), target resolution snapshot, Management Assignment Writer port + Fake, Deployment Attempts (immutable history, idempotent per operation id), DeploymentTarget states (`pending → assignment_requested → awaiting_observation → successful | failed | expired`, `already_satisfied`, `not_applicable`, `cancelled`) derived only from observations newer than the ring's assignment read-back (installation corroborates, contradictions raise Endpoint Findings), promotion gates evaluated by a job, pause/resume/halt, audit and events, separate disabled-by-default write capability flag (`SOFTWARE_DEPLOY_WRITE`), UI with ring progress.
4. **G4 Correlation and reporting:** failure correlation findings by error/model/OS/ring (Turaco-derived, distinct from provider-reported errors), Tasks/Tickets for failures through the existing task contract, F8 context (security advisories/findings on affected devices and versions; a remediation Task may link a Deployment), IT Briefing feed entries (rollouts in progress, halted rings), explorable rollout reporting (per ring, per target, history), docs.

## G1 implementation notes

Backend implemented (migration `000050`); [current status](current-status.md) lists routes, permissions and events, [state machines](../domain/state-machines.md#software-approval-status) the operations and reason codes. Decisions made during implementation:

- A version's approval state lives on the immutable version row (`registered` is the state before a request); the decisions are append-only in `software_version_approvals`, each recording the installer hash and binding hash it was made for. The binding identity is a SHA-256 over version string, installer hash, installer URL, publisher, install command hash and detection rule hash (unique per product), so a changed URL, command or rule with the same installer hash is also a new version.
- Registering versions and requesting approval need `software.package`; approving, rejecting, revoking and product status changes need `software.approve`. The person who registered or requested a version cannot approve it.
- Package statuses: `requested` (Turaco's request, before the provider answered) and the provider-reported `building | packaged | published | failed`. One package per provider and version; packaging calls the provider with operation key `versionId:installerSha256`, publishing with `packageId:publish:installerSha256`.
- Packaging and publishing additionally require the product to be `approved`; registering and requesting are refused while it is `blocked` or `retired`.
- `package_hash_mismatch` is an Endpoint Finding with a package subject (`endpoints.findings.software_package_id`); it is exposed as `hashMismatch` on packages, not in the device findings list.
- The publish target is the Management Provider the views evaluate (`intune`); the Management Artifact is linked by external id during the package synchronization once the management sync has ingested it.

## Reused concepts

Endpoint Device/Software Product/Installation, Management Artifact/Assignment/Observation (F6), Approvals, Tasks, Notifications, Changes (windows), Security advisories (F8), Relationships, externalrefs, outbox/jobs, audit, permissions, i18n.

## New concepts

Software Version Approval, Software Package, Software Management Provider (port), Management Assignment Writer (port), Target Set, Desired Software State, Deployment, Deployment Ring, Deployment Target, Deployment Attempt, Promotion Gate.

## Permissions (planned)

`software.view`, `software.approve` (elevated: approve/revoke version approvals, set approval status), `software.package` (elevated: package/publish), `deployments.view`, `deployments.manage` (create/edit plans), `deployments.execute` (elevated: start/pause/promote within approved plans), `deployments.high_impact` (elevated: All Devices/All Users, uninstall/supersede, promotion beyond pilot without window). Separation: approving a version, planning and executing are different permissions; the person who plans cannot approve the ring promotion Approval.

## Security

Approval binds to version plus installer hash; the hash is read from the provider's package observation and compared at publish and at ring start; any difference halts execution. Write credentials are a separate, disabled-by-default capability (flag and secret) and enabling is audited. All provider data is untrusted (safetext, bounds). No free text in audit. Idempotent operations everywhere; a double click or retry cannot double-deploy. Impact is bounded: a ring's target count is capped (default 5000) and must be explicitly confirmed above a threshold. Kill switch: a halt operation stops further assignment writes immediately.

## Not in F9

Native packaging/WinGet engine, endpoint-agent execution, real IntuneGet/Graph write clients (documented stubs until tenant exists), scheduling subsystem, continuous dynamic groups, OS patch (Windows Update) rings beyond what Intune assignments express, rollback automation beyond halt + uninstall deployment.
