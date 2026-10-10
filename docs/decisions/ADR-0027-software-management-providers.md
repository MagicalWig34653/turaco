# ADR-0027: Software Lifecycle and Patch Orchestration Through Software Management Providers

- Status: Accepted (2026-10-03). Slices G1 (software approvals and packages), G2 (Target Sets and Deployment planning), G3 (Deployment execution) and G4 (failure correlation and rollout reporting) are implemented with OpenAPI and UI, against fake provider adapters; the real IntuneGet client is not built and the Graph write client is implemented per documentation and unverified against a live tenant ([current status](../product/current-status.md)).
- Related: [ADR-0020](ADR-0020-management-assignment-intelligence.md) (Assigned / Expected Applicable / Observed, still valid), [Intune](../integrations/intune.md), [Intune Assignment Intelligence](../integrations/intune-assignment-intelligence.md).

## Context

Earlier planning (implementation plan F9, roadmap phase 5) had Turaco build its own patch engine: a WinGet provider executed by the Turaco Endpoint Agent over its own command transport. Packaging Windows software for Intune (WinGet catalog lookup, installers, detection rules, `.intunewin` creation, upload, new-version publication) is specialist work that existing tools already do; **IntuneGet** does it for Intune. What those tools do not provide is Turaco's context: which software is approved, which devices are affected, which vulnerabilities matter, how a rollout progresses, why it failed and who has to act.

## Decision

1. Turaco does **not** build its own WinGet packaging or patch engine as the default path. Packaging and publishing go through a **Software Management Provider** (an external system) whose Connector implements a provider-agnostic port. **IntuneGet** is the intended first provider. It publishes into **Intune, which stays the Management Provider** that targets devices, executes installs/updates and reports results. A Software Management Provider is not a Management Provider: it manages no devices.
2. **Turaco owns** (in the Endpoint module `endpoints`, which owns all software concepts since F6 — see [module boundaries](../architecture/module-boundaries.md)): software intelligence (normalized Software Products, Versions, Installations), the approved-software lifecycle (**Software Approval Status** and version approvals), affected-device context, Desired State and Deployments with **Deployment Rings**, approvals and maintenance windows, rollout state, failure correlation, security/CVE context, Intune assignment context, Tickets and Tasks, IT Briefing, reporting and audit.
3. **The provider owns:** WinGet catalog/provider mechanics, packaging, detection rules, `.intunewin` creation, Intune upload and provider-specific update mechanics (new-version publication, supersedence). Device-side execution is the Management Provider's.
4. The port models the capabilities Turaco needs, not a generic plugin interface: search the provider catalog for a Software Product, package a Software Version, publish or update a **Software Package** into a Management Provider, and report package/publish status as provider observations with source and observed time (including installer source, hash and publisher where reported).
5. A published package is an ordinary **Management Artifact** of the Management Provider (an Intune app) and is ingested by the same sync as every other artifact. The Software Package records only the link: provider reference (a platform external reference, `platform/externalrefs`), Software Version, installer hash, publish status and the resulting Management Artifact.
6. The ADR-0020 dimensions are extended by Turaco's intent, never collapsed. **Software Assignment** stays a projection of Management Assignment (dimension 2), never Turaco rollout intent. For one Software Version and target:
   1. **Desired State** — Turaco's intended outcome (Desired Software State), reached through a **Deployment** (operation) in **Deployment Rings** (stages);
   2. **Assigned** — the Management Assignment as currently configured in the provider and read back by sync, with source and freshness. A write request counts as Assigned only once sync reads it back, regardless of who wrote it;
   3. **Expected Applicable** — Turaco's evaluation (ADR-0020);
   4. **Observed** — the provider's Management Observation; a Software Installation is separate inventory evidence.
   A Deployment Target result is derived from evidence newer than the ring's assignment read-back: the Management Observation is authoritative, a Software Installation corroborates; a contradiction between them is raised as an Endpoint Finding, not resolved silently. Requested-but-not-yet-read-back assignments are tracked on the Deployment Attempt, not as another dimension. Stale or unknown evidence never counts as success, including for ring promotion thresholds; a target that already had the version before rollout is `already_satisfied`, not `successful`. Package build or publish success never marks a target successful. States: [state machines](../domain/state-machines.md#deployment).
7. Writing ring assignments into Intune is a typed, permissioned Management Provider action, idempotent per operation id, audited, and subject to approvals, ring promotion gates and maintenance windows. Turaco is the only writer of the assignments it orchestrates unless the feature design decides otherwise (open decision).
8. Failure correlation (by error, model, OS version, ring) produces Turaco-derived findings that stay distinct from provider-reported errors.
9. A native provider (for example WinGet executed by the Endpoint Agent) remains possible as another Software Management Provider and requires its own ADR. It is not the default plan.

## Security constraints (non-negotiable for the implementation)

- Approval binds to a **Software Version plus the installer SHA-256** (and publisher signature where available). A changed hash, installer URL, install command or detection rule requires a new approval. No version reaches a non-pilot ring without explicit approval. Approving a version and executing its deployment are separate permissions.
- Promotion beyond pilot, "All devices"/"All users" targeting and uninstall/supersede are high-impact: a dedicated permission plus Approval, beyond `deployments.execute`.
- Intune write access uses a **separate app registration and secret** from read-only sync, with minimum Graph permissions, and is disabled until explicitly enabled. Enabling it is audited.
- IntuneGet acts with write access to the Intune tenant. Self-hosted IntuneGet is preferred; a hosted (third-party, multi-tenant) instance requires an explicit, documented risk acceptance by the customer. The feature design documents the Entra permissions IntuneGet needs and who controls its credentials; Turaco stores only its own credentials as secrets ([ADR-0014](ADR-0014-application-level-secret-and-file-encryption.md)).

## Consequences

- Implementation plan F9 becomes "Software Lifecycle and Patch Orchestration (provider-based)"; Endpoint Agent work moves to an optional later phase.
- Prerequisites: platform targeting/dynamic groups and maintenance windows (platform services, not built inside `endpoints`) and F6 slices 2–4 (normalized management model, evaluation, history): orchestration reads assignments and observations from that model. A real Intune tenant and Graph client are needed before an IntuneGet integration can be verified (F6 decision E1: none yet).
- IntuneGet's integration surface (API, webhooks, or shared Intune state only) must be verified before the feature design; without an API, Turaco orchestrates through Intune state it observes.
- Software Approval Status is Turaco's decision; vendor end-of-life/end-of-support is a separate observed fact.
