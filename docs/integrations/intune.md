# Intune Integration

**Status:** Internal side of device and software ingestion implemented (F6 slice 1): `integrations/intune` defines the normalized `Provider` contract with an in-memory Fake and a `NotConfigured` placeholder; the `endpoints` module ingests snapshots idempotently. The normalized management model is implemented as well (F6 slice 2): the `Provider` contract has `Management(ctx)` returning `ManagementSnapshot` (filters, artifacts with their assignments, observations, device group memberships; the Fake supports it, `NotConfigured` returns `ErrNotConfigured`), and `endpoints` ingests it idempotently with assignment interval history, observation history, retirement of unreported observations and tombstones. The evaluator and the read views (slice 3) and the history, diff, `assignment_ineffective` finding and device list filters (slice 4) are implemented as backend. Published Software Packages (F9 G1) are ordinary application artifacts of this sync; the Software Management Provider that publishes them is described in [IntuneGet](intuneget.md). Not implemented: the Microsoft Graph client (authentication, paging, delta, throttling), the `turaco-admin endpoints import` command and the Assignment Intelligence frontend. Permissions: `endpoints.view`/`endpoints.manage` cover devices and software; `endpoint.management.view` (or `endpoints.manage`) reads artifacts, assignments, filters and, with device access, device observations; `integrations.intune.manage` is reserved for integration administration.

Intune is a Management Provider/Data Source, not the Turaco Asset database. Turaco keeps one canonical Device/Asset identity and synchronizes the Intune context required for operations, search, history and troubleshooting.

## Normalize locally

Store local normalized data needed for search/history/correlation while preserving source/freshness:

- managed Device identity and check-in/compliance/OS data;
- discovered/observed applications;
- assignable management artifacts such as applications, configuration profiles, compliance policies, endpoint-security policies and supported scripts/remediations;
- provider assignments, include/exclude targets and assignment filters;
- provider observations/status for concrete Users/Devices where available;
- provider external IDs and revisions needed for reconciliation.

Vendor Graph DTOs stay inside the Integration boundary.

## Assignment intelligence

Turaco must make Intune targeting understandable on **Directory Group, User and Device** pages rather than exposing a raw provider-object list.

The canonical view separates:

1. **Assigned** — provider assignment intent exists;
2. **Expected Applicable** — Turaco evaluates that it should apply using known target membership, exclusions and supported filter/provider semantics;
3. **Observed** — Intune reports a concrete status/result.

Expected applicability is explicitly labeled as a Turaco evaluation with confidence/reason and must never be presented as authoritative Intune execution state.

Detailed requirements, data concepts, Assignment Paths, reverse lookup, history and diff behavior are defined in [Intune Assignment Intelligence](intune-assignment-intelligence.md) and ADR-0020.

## Important distinctions

- Software Installation (observed) != Software Assignment/Management Assignment (intent).
- Assignment != applicability.
- Expected applicability != observed application state.
- Provider observation without a recent timestamp may be stale.
- User-targeted and Device-targeted paths remain distinguishable.

## Device page

A canonical Device can show Asset lifecycle/owner alongside latest Intune management state, OS, compliance, software and management assignments. Management views should expose Apps, Configuration, Compliance, Endpoint Security and supported Scripts/Remediations with source, freshness, Assignment Path and observed result.

## Directory Group page

A Directory Group can show all normalized Intune assignments targeting it, including include/exclude mode, app intent, filters, source/freshness and explorable expected Device/User counts where Turaco can evaluate them.

## User page

A User can show assignments inherited through Directory Groups/All Users/provider-supported user targeting while preserving the distinction between User targeting and per-Device observed results.

## Reverse lookup and comparison

Turaco should support:

- Management Artifact -> assigned Directory Groups/Users/Devices and expected affected set;
- Device vs Device assignment/effective-state diff;
- Directory Group vs Directory Group assignment diff;
- "Why is this assigned?" explainability paths.

## Actions

Future Intune actions are typed provider capabilities behind domain/application operations and explicit permissions. Read-only assignment intelligence is architecturally separate from privileged management/deployment actions. Write actions (for example ring assignments) will use a separate, disabled-by-default app registration from read-only sync.

## Apps published by a Software Management Provider (planned)

Software lifecycle orchestration ([ADR-0027](../decisions/ADR-0027-software-management-providers.md)) uses IntuneGet to package WinGet software and upload it to Intune. The resulting Intune app is an ordinary Management Artifact ingested by this sync; Turaco links it to the Software Package and Software Version but never treats publication as assignment or installation. Intune remains the Management Provider that assigns, executes and reports.

## Management Assignment Writer (F9 G3)

`integrations/intune` also defines the typed, idempotent write port Deployments use ([ADR-0027](../decisions/ADR-0027-software-management-providers.md), [design](../product/f9-software-lifecycle-design.md#g3-implementation-notes)): `SetRingAssignment(RingAssignmentOp{OperationID, ManagementArtifactExternalID, RingKey, TargetGroupExternalID, Intent, DeviceExternalIDs})` and `ClearRingAssignment(operationID, artifact, ringKey)`. Each Deployment Ring maps to one Turaco-owned provider group (`turaco-ring-<ringId>`) that holds the ring's target Devices and one assignment of the published application to it; Turaco never edits other assignments. Errors wrap `ErrTransient` (retry with a new attempt) or `ErrPermanent`. The write counts as Assigned only after the normal management synchronization reads the assignment and the group membership back.

### Real writer contract

A real (Graph) writer must satisfy this contract; the Fake implements it and the tests rely on it.

- **Content-idempotent.** The same request must leave the provider in the same state however often it is replayed, also under a new operation id (the engine sends a new id for every attempt and may replay one after a crash). The Turaco-owned group id is deterministic (`turaco-ring-<ringId>`), the assignment id is deterministic (`turaco-<ringId>`) and the write has PUT semantics: it replaces the group's membership with exactly `DeviceExternalIDs` and replaces the ring's assignment of the artifact (it never appends, never touches other groups or assignments).
- **Provider-filtered devices.** `DeviceExternalIDs` are the provider ids of Devices of the configured provider only (Turaco filters; the writer must still reject ids it does not know instead of ignoring them silently).
- **Clear** removes only the ring's own assignment and group membership (deterministic ids) and succeeds when they are already gone.
- **Timeouts.** The engine bounds each call to 30 s; the writer must honor `ctx`, never block longer, and report a timeout or HTTP 429/5xx as `ErrTransient` and a rejected request (4xx other than 429, unknown artifact or device) as `ErrPermanent`. A call whose outcome is unknown (timeout after sending) is `ErrTransient`; the next attempt is safe because of content idempotency.
- **No reads, no commands.** The writer returns only success or error; whether the assignment exists is read back by the normal management synchronization. It carries no tenant data in errors beyond codes (errors are stored as outcome codes only).
- **Secrets.** The write registration has its own app registration and secret, separate from the read-only sync.

The writer is disabled by default (`SOFTWARE_DEPLOY_WRITE`) and needs its own app registration/secret once a Graph client exists. **Not implemented:** the Graph writer; the production wiring uses `NotConfiguredWriter`, whose writes fail permanently (a started ring halts with `assignment_failed`). `FakeWriter` applies assignments and memberships to the Fake provider snapshot, records every call and can inject failures and latency; it is used by tests only.
