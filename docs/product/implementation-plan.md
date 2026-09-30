# Implementation Plan

**Status:** Sequencing baseline for Claude Code. This is not a date commitment.

The rule for every step is: implement a vertical slice with tests/docs instead of creating empty framework code for future phases.

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

Do not implement Entra by changing the User model; add an identity provider adapter later.

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
5. remediation work/tasks and progress,
6. system/integration health briefing items.

## F9 — Endpoint Management

Only after Endpoint Intelligence is proven:

1. Endpoint Agent enrollment/device identity,
2. inventory telemetry,
3. typed command transport,
4. Desired Software State,
5. WinGet provider,
6. Deployment/Target/Attempt execution,
7. pilot rings/maintenance windows,
8. remediation and diagnostics.

Arbitrary remote shell is explicitly out of scope.

## F10 — Remote Support

Requires a separate threat model and accepted ADR. Treat control plane, relay/media path, consent, unattended policy, MFA/elevation, session audit and tenant isolation as a separate high-trust subsystem.
