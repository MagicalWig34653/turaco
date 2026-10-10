# Implementation Plan

**Status:** Sequencing baseline for Claude Code. This is not a date commitment.

The rule for every step is: implement a vertical slice with tests/docs instead of creating empty framework code for future phases.

**Plan revision 2026-10-03 (after F6 slice 1):** F9 and F10 were re-scoped to integrate specialist providers instead of building a patch engine and a remote-desktop transport ([ADR-0026](../decisions/ADR-0026-remote-access-providers.md), [ADR-0027](../decisions/ADR-0027-software-management-providers.md)); F11 Workforce Presence ([ADR-0028](../decisions/ADR-0028-workforce-presence.md)) and F12 Turaco AI ([ADR-0029](../decisions/ADR-0029-turaco-ai.md)) were added; Endpoint Agent management moved to "Later / optional". F0–F8 are unchanged. Earlier sessions planned F9 as "Endpoint Management" (Endpoint Agent command transport and a native WinGet provider) and F10 as a Turaco-built "Remote Support" subsystem; that plan is superseded. The F-number order is not a commitment; F9–F12 sequencing is an open product decision.

## Delivery status (2026-10-10)

[Current status](current-status.md) is authoritative; this table only orders the phases. "Fake" means the internal side is complete behind a domain port and runs against a fake or placeholder adapter because no real vendor client exists.

| Phase | State | Open gaps |
| --- | --- | --- |
| F0 Repository foundation | Done | |
| F1 Identity and Organization | Done for on-prem with one directory | Real Active Directory and Windows client verification; Entra sign-in moved to F15 |
| F2 Work Foundation | Done | Email bounce handling, digests, per-recipient language |
| F3 Products, Catalog, Requests | Done (backend, OpenAPI, UI) | |
| F4 Inventory, Procurement, Assets | Done (backend, OpenAPI, UI) | |
| F5 Service Desk and Knowledge | Done (tickets, Major Incidents, Problems, Runbooks, Autotask internal side) | Autotask REST client and webhook (fake), routing rules, SLA |
| F6 Endpoint Intelligence | Done against the fake provider (devices, management model, assignment intelligence, UI) | Microsoft Graph client, `assignment_stale` |
| F7 Infrastructure and Change | Done (F7a to F7d, OpenAPI, UI) | Runbook link, native IPAM (ADR-0030: integrate) |
| F8 Security and IT Briefing | Done (advisories, findings, remediation, feed, UI) | OSV and MSRC feeds |
| F9 Software Lifecycle | G1 to G4 done (backend, OpenAPI, UI) against fake adapters | Real IntuneGet client and Graph write client |
| F10 Remote Access | R-A and R-B done (attended, launch-link connectors) | Provider API connectors, unattended access, R-C threat models and lab test |
| F11 Workforce Presence | P-A and the P-B UI done (manual entries, availability hints) | Change window and Briefing integration, coverage notifications, external sources (P-C) |
| F12 Turaco AI | A-A (read-only backend) and A-B (UI) done | AI Proposals and write tools (A-C), MCP server (A-D), non-local providers |
| F13 Workbench Views | Q-A to Q-D done (query engine, Saved Views, Queues, System Views, My Work sources, Boards; backend and UI) | Q-E catalogs of further resources, routing rules, archived-View restore screen |
| F14 Administration | A-A, A-A2, A-B (backend) and A-D, A-E (UI) done | A-C/A-F health, setup checklist, audit UX, search; CSV import and bulk edit; A-G External Parties |
| Module switches (ADR-0032) | Done (backend and `/admin/modules`) | |
| F15 Microsoft integration | Planned (design only) | Everything: M-0, E-A to E-E (Entra sign-in), T-A to T-E (Teams) |

The UI design pass (themes, shell, workbench screens) ran between F8 and F9; visual design beyond functional UI is postponed.

## F0 — Repository Foundation

Already scaffolded:

- architecture/domain governance,
- Go/React/PostgreSQL baseline,
- migrations,
- API/worker/agent entry points,
- generated permission/event/config references,
- architecture/doc checks,
- Colima development dependencies,
- GitHub CI/security/container workflows.

Exit criteria: connected bootstrap produces lockfiles and `make check && make build` pass on the developer machine and CI.

## F1 — Identity and Organization

Vertical slices:

1. canonical User/Team/Location/Directory Group repositories and read APIs,
2. authentication session foundation,
3. LDAP/AD directory sync with source/freshness metadata, including observed Directory Group/user memberships where available,
4. Kerberos/SPNEGO transparent employee SSO design/implementation for on-prem,
5. role/permission/scope evaluation,
6. audit of privileged identity/config changes.

Do not implement Entra by changing the User model; add an identity provider adapter later. Entra sign-in is planned in F15 ([ADR-0035](../decisions/ADR-0035-entra-oidc-login.md)).

## F2 — Work Foundation

1. Task lifecycle and assignment,
2. My Work queries,
3. in-app notification model,
4. HTML email channel,
5. recurring task definitions,
6. IT Briefing manual publications.

## F3 — Products, Catalog and Requests

1. Products/manufacturers/categories,
2. Catalog Item schema/form definition,
3. Service Request lifecycle,
4. Approval model,
5. Hardware Request workflow,
6. New Workplace workflow,
7. Software Request workflow,
8. Access Request workflow,
9. employee-facing request status UI.

## F4 — Inventory, Procurement and Assets

1. Warehouse/Storage Location,
2. immutable Inventory Transactions,
3. reservations with concurrency tests,
4. Procurement Request/Purchase Order,
5. Goods Receipt,
6. serialized Asset creation,
7. Asset Assignment/return/lifecycle,
8. provisioning state,
9. barcode/QR workflows.

## F5 — Service Desk and Knowledge

1. employee Incident creation,
2. transparent user detection,
3. likely/current device context selection,
4. queues/assignment/waiting reason,
5. device-context snapshot,
6. Major Incident/known outage,
7. Knowledge Articles and contextual suggestions,
8. Problems/Known Errors,
9. Runbook definition/execution,
10. Autotask adapter.

## F6 — Endpoint Intelligence

1. Intune device identity mapping,
2. OS/hardware observations with freshness,
3. installed-software observations,
4. software normalization/aliases,
5. normalized Management Artifacts for apps/configuration/compliance/endpoint security and supported scripts/remediations,
6. Management Assignments with Directory Group/All Users/All Devices targets, include/exclude semantics, app intent and assignment filters, plus observed Device group memberships required for explainability,
7. Group/User/Device views separating Assigned / Expected Applicable / Observed,
8. explainable Assignment Paths ("Why is this assigned?") with User-vs-Device source and explicit `unknown` when Turaco cannot evaluate safely,
9. Management Artifact reverse lookup to assigned/effective Groups/Users/Devices with explorable counts,
10. meaningful assignment/applicability history with source/freshness,
11. Device-vs-Device and Group-vs-Group assignment/effective-state diff,
12. provider-reported conflicts/errors plus Turaco-derived stale/ineffective-assignment findings kept visibly distinct,
13. device reconciliation/data-quality findings,
14. query/saved-view support for endpoints and management state.

Detailed requirements are in `docs/integrations/intune-assignment-intelligence.md`. F6 does not attempt to reproduce undocumented Intune internals; expected applicability remains a Turaco-derived read model.

## F7 — Infrastructure and Change

1. Site/Building/Room/Rack/Rack Placement,
2. physical Devices reuse Asset identity,
3. VM/Hypervisor relationships,
4. decide native IPAM vs NetBox integration through ADR,
5. Service ownership/dependency graph,
6. Change lifecycle/maintenance windows,
7. Initiative/modernization planning,
8. impact views and maintenance calendar.

## F8 — Security and IT Briefing

1. Security Advisory ingestion,
2. advisory-to-software normalization,
3. potential Vulnerability Findings with confidence,
4. IT Briefing aggregation,
5. remediation work/tasks and progress (remediation deployments follow F9),
6. system/integration health briefing items.

## F9 — Software Lifecycle and Patch Orchestration (provider-based)

[ADR-0027](../decisions/ADR-0027-software-management-providers.md). Turaco orchestrates; IntuneGet (first Software Management Provider) packages and publishes into Intune; Intune assigns, executes and reports. Do not build a native WinGet/packaging engine.

Prerequisites: F6 slices 2–4, a real Intune tenant and Graph client, platform targeting/dynamic groups and maintenance windows (built as platform services, not inside `endpoints`).

1. Software Approval Status and version approvals bound to installer hash (approved software list),
2. Software Management Provider port, IntuneGet connector and Software Package references (integration surface verified first),
3. separate, disabled-by-default Intune write credential and typed assignment-write action,
4. Desired State, Deployment and Deployment Rings with promotion gates (Approval, fresh-evidence thresholds, soak time, Maintenance Window),
5. Deployment Target results derived from fresh Management Observations (Installation corroborates), keeping Desired / Assigned / Expected Applicable / Observed separate,
6. failure correlation as Turaco-derived findings, distinct from provider-reported errors,
7. security/CVE context from F8 on affected devices and versions,
8. Tickets/Tasks for failures, IT Briefing items and explorable rollout reporting.

## F10 — Remote Access (provider-based)

[ADR-0026](../decisions/ADR-0026-remote-access-providers.md). Turaco owns authorization, audit, context and session records; the provider (HopToDesk, RustDesk and AnyDesk as first providers) owns the transport. Do not build a remote-desktop transport.

1. threat model per provider (HopToDesk, RustDesk, AnyDesk) and verification of each integration surface (API, per-session credentials, device identity, session records); a provider whose surface is insufficient ships with reduced capabilities (attended only) or not at all,
2. Remote Access Provider port and connectors for the first providers (HopToDesk, RustDesk, AnyDesk), one connector per provider (device mapping, session start with one-time launch handle, session records),
3. Remote Access Session lifecycle with Ticket/Device context and audit,
4. attended sessions with user consent,
5. unattended-access policy records and unattended sessions only if the provider meets ADR-0026's constraints,
6. typed remote actions through the Endpoint public contract.

## F11 — Workforce Presence

[ADR-0028](../decisions/ADR-0028-workforce-presence.md). Operational availability, not HR. Feature design: [F11 design](f11-workforce-presence-design.md). Requires a data protection review before external sources.

1. recurrence rule moved from Tasks into a platform scheduling package,
2. Presence Entries entered in Turaco with privacy-scoped visibility and retention,
3. Operational Availability and Team Coverage read models with per-Team minimums,
4. My Work, Ticket assignment, Change and IT Briefing integration,
5. Microsoft 365 source (free/busy, work location, out-of-office only),
6. HR system source.

## F12 — Turaco AI

[ADR-0029](../decisions/ADR-0029-turaco-ai.md). Provider-independent, tool-based and user-delegated. Feature design: [F12 design](f12-turaco-ai-design.md).

1. AI runtime, first AI Provider connector, egress policy and audit,
2. read-only AI Tools contributed by modules (summaries, explanations),
3. AI Proposals with confirmed writes,
4. drafts/requests into existing approval workflows for high-impact intents,
5. read-only Turaco MCP server.

## F13 — Workbench Views

[ADR-0033](../decisions/ADR-0033-workbench-views-query-engine.md). Feature design: [F13 design](f13-workbench-views-design.md). Implemented: Q-A query engine and first catalogs, Q-B Saved Views, shares and pins, Q-C Ticket Queues with numbering, System Views, counts and My Work sources, Q-D Task Boards, and the UI of all four. Not started: Q-E catalogs for further resources (assets, software, requests, changes, approvals, knowledge, procurement, security findings), one pull request per module family.

## F14 — Administration

[ADR-0034](../decisions/ADR-0034-local-accounts-and-external-parties.md). Feature design: [F14 design](f14-administration-design.md). Slices: A-A People and Locations backend (with A-A2 local accounts), A-B roles and effective permissions backend, A-C platform health, setup checklist, audit and search backend, A-D People UI, A-E roles UI, A-F health, audit and search UI, A-G External Parties. A-A, A-A2, A-B, A-D and A-E are implemented; A-C, A-F and A-G are open. Operator walkthrough: [Getting started as an administrator](../operations/administrator-getting-started.md).

## F15 — Microsoft integration (planned)

[ADR-0035](../decisions/ADR-0035-entra-oidc-login.md) (Entra sign-in for every deployment variant) and [ADR-0036](../decisions/ADR-0036-microsoft-teams-integration.md) (Teams), both Proposed. Feature design: [F15 design](f15-microsoft-integration-design.md). Slices: M-0 shared Microsoft HTTP client and credentials; E-A sign-in core, E-B identity linking, E-C Graph reconciliation and cloud-only group sync, E-D MFA assurance and step-up, E-E restricted multi-tenant and guests (after F14 A-G); T-A channel posts, T-B personal cards, T-C approval actions, T-D meeting and channel links, T-E Microsoft 365 presence (F11 P-C). Nothing is implemented; the open product decisions are listed in the design.

## Later / optional

Each item needs its own ADR before work starts.

- Endpoint Agent enrollment, device identity, inventory telemetry and typed command transport ([ADR-0008](../decisions/ADR-0008-separate-agents.md)); arbitrary remote shell stays out of scope.
- Native Software Management Provider (for example WinGet executed by the Endpoint Agent).
- Native Remote Access Provider (requires its own threat model; control plane, relay/media path, consent, unattended policy, session audit and tenant isolation as a separate high-trust subsystem).
