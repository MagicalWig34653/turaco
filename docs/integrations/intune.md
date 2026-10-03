# Intune Integration

**Status:** Internal side of device and software ingestion implemented (F6 slice 1): `integrations/intune` defines the normalized `Provider` contract with an in-memory Fake and a `NotConfigured` placeholder; the `endpoints` module ingests snapshots idempotently. Not implemented: the Microsoft Graph client (authentication, paging, delta, throttling), the `turaco-admin endpoints import` command, management artifacts, assignments and assignment intelligence (F6 slices 2–4). Permissions: `endpoints.view`/`endpoints.manage` cover devices and software; `endpoint.management.view` and `integrations.intune.manage` are reserved for management artifacts and integration administration.

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
