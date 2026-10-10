# Intune Integration

**Status:** Internal side of device and software ingestion implemented (F6 slice 1): `integrations/intune` defines the normalized `Provider` contract with an in-memory Fake and a `NotConfigured` placeholder; the `endpoints` module ingests snapshots idempotently. The normalized management model is implemented as well (F6 slice 2): the `Provider` contract has `Management(ctx)` returning `ManagementSnapshot` (filters, artifacts with their assignments, observations, device group memberships; the Fake supports it, `NotConfigured` returns `ErrNotConfigured`), and `endpoints` ingests it idempotently with assignment interval history, observation history, retirement of unreported observations and tombstones. The evaluator and the read views (slice 3) and the history, diff, `assignment_ineffective` finding and device list filters (slice 4) are implemented as backend. Published Software Packages (F9 G1) are ordinary application artifacts of this sync; the Software Management Provider that publishes them is described in [IntuneGet](intuneget.md). The Microsoft Graph clients (read and assignment write) are implemented per the vendor documentation and **not verified against a live tenant** (see [Microsoft Graph clients](#microsoft-graph-clients-implemented-per-documentation-unverified)). Not implemented: Graph delta queries, the `turaco-admin endpoints import` command and the Assignment Intelligence frontend. Permissions: `endpoints.view`/`endpoints.manage` cover devices and software; `endpoint.management.view` (or `endpoints.manage`) reads artifacts, assignments, filters and, with device access, device observations; `integrations.intune.manage` is reserved for integration administration.

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

The writer is disabled by default (`SOFTWARE_DEPLOY_WRITE`) and needs its own app registration/secret (`MICROSOFT_GRAPH_WRITE_*`). Without that registration the production wiring uses `NotConfiguredWriter`, whose writes fail permanently (a started ring halts with `assignment_failed`). `GraphWriter` implements the contract per documentation (unverified, see below). `FakeWriter` applies assignments and memberships to the Fake provider snapshot, records every call and can inject failures and latency; it is used by tests only.

## Microsoft Graph clients (implemented per documentation, unverified)

**Honest status.** `GraphProvider` (read) and `GraphWriter` (write) in `backend/internal/integrations/intune` were written strictly from the Microsoft Learn pages named in their source comments (each endpoint carries the page URL and the date it was read, 2026-10-10) on top of `integrations/microsoft` (shared HTTP client, token source, Graph client). They have been tested only against local HTTP servers that reproduce the documented payload shapes. They have **never** talked to a real tenant. Each client reports mode `real` with status `unverified` (health error code `unverified`) until a call succeeded in the process; then `ok`, or `failing` with a short machine code (`http_403`, `auth_invalid_client`, ...) after a failure. The status is per process: the API process owns the Intune read client (manual `POST /api/v1/endpoint-sync`), the worker owns the writer (deployment engine), and only the API's health page shows the read client. Without configuration the placeholders remain and health says `client_not_configured`.

### Behavior

- **Authentication.** Client credentials against `https://login.microsoftonline.com/<tenant>/oauth2/v2.0/token` with scope `https://graph.microsoft.com/.default`; secret file or certificate (client assertion), cached until five minutes before expiry; one refresh and retry after a 401. Credentials come from deployment files, never the database (ADR-0014). Read registration: `MICROSOFT_GRAPH_TENANT_ID`, `MICROSOFT_GRAPH_CLIENT_ID` and `MICROSOFT_GRAPH_CLIENT_SECRET_FILE` or `MICROSOFT_GRAPH_CLIENT_CERTIFICATE_FILE` with `MICROSOFT_GRAPH_CLIENT_PRIVATE_KEY_FILE`; write registration: the same with `MICROSOFT_GRAPH_WRITE_`. They are separate app registrations; the write one is mounted only into the worker.
- **Throttling and errors.** 429 and 5xx are retried (up to four attempts) after the `Retry-After` value (an hour at most; a wait longer than 60 seconds ends the call with a transient error so the job runner retries later); other 4xx answers surface as the HTTP status, the Graph error code and the `request-id`, never as message or body text (they can echo tenant data). Responses are capped at 8 MiB per page, 2000 pages and 200,000 items per list.
- **Paging.** `@odata.nextLink` is followed only when scheme and host equal the configured Graph host; anything else aborts the run.
- **Host allow-list.** Only `graph.microsoft.com` and `login.microsoftonline.com` (global cloud) are reachable; https only, no redirects, optional `MICROSOFT_HTTP_PROXY` and `MICROSOFT_CA_FILE`.
- **Unknown fields** in answers are ignored; unknown enum values map to `other`/`unknown`.
- **Read is all-or-nothing.** A failing call fails the whole management run, so the ingestion never retires data because of a partial read.

### Read client: endpoints and permissions

| Purpose | Request | Application permission | Documentation |
| --- | --- | --- | --- |
| Managed devices | `GET /v1.0/deviceManagement/managedDevices` | `DeviceManagementManagedDevices.Read.All` | intune-devices-manageddevice-list |
| Detected apps per device | `GET /v1.0/deviceManagement/managedDevices/{id}/detectedApps` | `DeviceManagementManagedDevices.Read.All` | intune-devices-detectedapp (the per-device path is documented as a relationship on the beta managedDevice page only: least verified call) |
| Applications and assignments | `GET /v1.0/deviceAppManagement/mobileApps?$expand=assignments` | `DeviceManagementApps.Read.All` | intune-apps-mobileapp-list, intune-apps-mobileappassignment |
| Configuration profiles | `GET /v1.0/deviceManagement/deviceConfigurations?$expand=assignments` | `DeviceManagementConfiguration.Read.All` | intune-deviceconfig-deviceconfiguration-list |
| Compliance policies | `GET /v1.0/deviceManagement/deviceCompliancePolicies?$expand=assignments` | `DeviceManagementConfiguration.Read.All` | intune-deviceconfig-devicecompliancepolicy-list |
| Turaco ring groups (found by name) | `GET /v1.0/groups?$filter=startswith(displayName,'turaco-ring-')` | `GroupMember.Read.All` | group-list |
| Group device members (nested included) | `GET /v1.0/groups/{id}/transitiveMembers/microsoft.graph.device` | `GroupMember.Read.All`, `Device.Read.All` | group-list-members (transitive variant not on the page: unverified) |
| Assignment filters (beta, `INTUNE_GRAPH_BETA`) | `GET /beta/deviceManagement/assignmentFilters` | `DeviceManagementConfiguration.Read.All` | intune-policyset-deviceandappmanagementassignmentfilter-list (beta only) |
| Settings catalog policies (beta) | `GET /beta/deviceManagement/configurationPolicies?$expand=assignments` | `DeviceManagementConfiguration.Read.All` | intune-deviceconfigv2-devicemanagementconfigurationpolicy (beta only) |
| App install statuses as observations (beta) | `GET /beta/deviceAppManagement/mobileApps/{id}/deviceStatuses` | `DeviceManagementApps.Read.All` | intune-apps-mobileappinstallstatus-list (beta only) |

Mapping: owner `company` becomes `corporate`; compliance `inGracePeriod` becomes `in_grace_period`; `groupAssignmentTarget` and `exclusionGroupAssignmentTarget` become group include or exclude, `allDevicesAssignmentTarget` and `allLicensedUsersAssignmentTarget` become `all_devices` and `all_users`; install intents `available` and `availableWithoutEnrollment` become `available`. An artifact with a target type Turaco cannot represent keeps its previous assignments (`AssignmentsKnown` false). Graph chooses the ids of groups, so Turaco-owned ring groups (display name `turaco-ring-<ringId>`) are reported under the deterministic id `turaco-ring-<ringId>` that the Deployment engine expects.

**Not read yet** (the adapter reports nothing for them): endpoint security intents, scripts and remediations, configuration and compliance device statuses (the v1.0 status objects carry only a device display name, no device id), per-user targeting memberships. Observations come only from application install statuses and only with beta enabled; whether `deviceId` of that object is the managed device id is not stated by the documentation.

### Write client: endpoints and permissions

| Step | Request | Application permission |
| --- | --- | --- |
| Check the artifact exists, read assignments | `GET /v1.0/deviceAppManagement/mobileApps/{id}/assignments` | `DeviceManagementApps.Read.All` (covered by ReadWrite) |
| Find or create the ring group | `GET /v1.0/groups?$filter=displayName eq 'turaco-ring-<ringId>'`, `POST /v1.0/groups` (security group, assigned) | `Group.ReadWrite.All` (`Group.Create` cannot read the group back) |
| Resolve managed device to Entra device object | `GET /v1.0/deviceManagement/managedDevices/{id}`, `GET /v1.0/devices?$filter=deviceId eq '<guid>'` | `DeviceManagementManagedDevices.Read.All`, `Device.Read.All` |
| Replace group membership | `GET /v1.0/groups/{id}/members/microsoft.graph.device`, `POST /v1.0/groups/{id}/members/$ref`, `DELETE /v1.0/groups/{id}/members/{deviceObjectId}/$ref` | `GroupMember.ReadWrite.All` and `Device.ReadWrite.All` |
| Set or clear the ring assignment | `POST /v1.0/deviceAppManagement/mobileApps/{id}/assignments`, `DELETE /v1.0/deviceAppManagement/mobileApps/{id}/assignments/{assignmentId}` | `DeviceManagementApps.ReadWrite.All` |

Ownership and idempotency: the writer only touches the group named `turaco-ring-<ringId>` (validated before the name reaches a filter) and assignments targeting exactly that group; it refuses any other target group, never edits other assignments of the artifact, and an unknown artifact or device is a permanent error before anything is created. A replay leaves the same state (membership replaced by exactly the requested devices, one include assignment with the requested intent, an assignment with a different intent is replaced); clear removes only the ring's assignment and empties the group (the empty group stays) and succeeds when both are gone. 429, 5xx, timeouts and a group that is not yet replicated are `ErrTransient`; other 4xx answers are `ErrPermanent`. A write counts as Assigned only after the normal management sync reads it back. Dynamic or nested membership of the ring group is not supported (the group is created with assigned membership). Write access is wide (`Group.ReadWrite.All`, `Device.ReadWrite.All`); Microsoft documents no narrower application permission for these calls.

### Unverified and to check with a real tenant

Whether `$expand=assignments` is honoured on every collection above (if the `assignments` key is missing the assignments are reported as unknown and the previous ones stay), the per-device `detectedApps` path, `transitiveMembers/microsoft.graph.device` with `ConsistencyLevel: eventual`, the meaning of `deviceId` in application install statuses, the page sizes and throttling limits for Intune endpoints (the Graph throttling page names none), `Group.ReadWrite.All` being sufficient to add devices to a group, the replication delay after creating a group, and the beta shapes. Findings from a real run belong into this section.


## Ring group ownership (review 2026-10-10)

The writer treats a group as Turaco-owned only when its display name, mail nickname, description marker (`Owned by Turaco: deployment ring group <name>...`) and shape (security group, no mail, no group types) all match what Turaco creates; a group that only carries the ring name is refused with a permanent error and never changed. The marker is not tamper-proof against a Group administrator, so restrict the write registration with a restricted-management administrative unit that contains only the ring groups, and keep the write permissions off the sign-in and read registrations.
