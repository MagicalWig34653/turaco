# F8 Security and IT Briefing — Feature Design

**Status:** F8a backend implemented 2026-10-04; F8a UI, F8b and F8c remain planned. Decisions B1–B5 are adopted. [Current status](current-status.md) is authoritative for runtime availability. Related: [security remediation workflow](../workflows/security-remediation.md), [state machines](../domain/state-machines.md) (Security Advisory / Finding), [core data model](../domain/core-data-model.md), [F6 design](f6-endpoint-intelligence-design.md), [F7 design](f7-infrastructure-change-design.md), [ADR-0027](../decisions/ADR-0027-software-management-providers.md).

## Decisions

- **B1 No live advisory feed yet.** Like Intune and Autotask, F8 builds the internal side behind a port: `integrations/advisories` defines a normalized `AdvisoryRecord` (source, external id such as a CVE or vendor bulletin id, title, summary, severity, published/modified time, affected criteria, references, source URL), a `Fake`, a `NotConfigured` placeholder and a bounded JSON import (`POST /api/v1/security/advisories/import`, also usable by `turaco-admin security import`). Real feeds (NVD/CISA KEV/vendor RSS, MSRC) are later adapters. Advisories can also be entered by hand.
- **B2 Matching is deterministic and conservative.** An advisory carries *affected criteria*: `(software_product_id | product name + publisher, version range expressed as `introduced`/`fixed` or `lt`/`le`/`eq` lists, optional OS platform)`. Matching runs against the F6 software installations (normalized Software Products, raw name/version preserved). Confidence: `confirmed` is never assigned automatically (reserved for an analyst or an observation-based verification), `probable` = normalized Software Product matches and the installed version is inside the range, `potential` = product matched only through an alias or the version could not be compared (non-semver), `unknown` = never stored. Findings are Turaco-derived and say so; "potential" is never shown as a confirmed vulnerability.
- **B3 Security is its own module `security`** (schema `security`) owning Advisories, affected criteria, Vulnerability Findings and risk acceptances. It reads installations through a new `endpoints/public` contract (batch reads by Software Product), never the endpoints tables.
- **B4 Remediation in F8 = tracking, not deployment.** A Finding or an Advisory can create Tasks (owner, due date) and link Changes (relationship `advisory REMEDIATED_BY change` via `platform/relationships`); progress is derived from fresh observations: a Finding becomes `remediated` only when a newer installation observation no longer matches (or the device was tombstoned), never because a task was closed. Deployments, rings and publishing arrive with F9.
- **B5 Briefing stays an aggregation layer.** `briefing` keeps manual Items (F2) and gains a computed feed `GET /api/v1/briefing` that merges: published manual Items, applicable Security Advisories with affected counts, upcoming maintenance and due milestones (`planning/public`), open Major Incidents and recent failures (Service Desk public read), integration health (Intune sync age/errors, Autotask failed pushes, directory sync status via their public/read contracts), pending Approvals count for the caller. Entries are computed per request from the owning modules (no copied authoritative data); each entry has a type, severity, reference link and the owning module's permission is applied (an entry the caller may not see is omitted). Optional per-user dismiss/read state is out of scope.

## Slices

1. **F8a Advisories and findings (backend implemented; UI planned):** Advisory lifecycle `new → analyzing → applicable | not_applicable → remediation_planned → remediating → resolved → archived` with explicit operations, affected criteria editor, bounded JSON import port + Fake, `security.match` per-advisory job and `security.match_all` every six hours, Vulnerability Findings (`open → investigating | accepted → remediation_planned → remediating → remediated`, `false_positive`, `risk_accepted` with reason code, actor and review date) with confidence, device/software drill-down ("which devices are affected"), counts per advisory, list/filters, audit, permissions `security.view`/`security.manage`/`security.accept_risk` (last two elevated), events `SecurityAdvisoryPublished`, `VulnerabilityFindingChanged`. Matching reads Endpoint installations through `endpoints/public`, processes at most 20,000 Devices per Advisory and reports `truncated`. A newer observation or Device tombstone is needed to mark a Finding remediated. No UI is part of this slice.
2. **F8b Remediation tracking (planned):** Tasks from advisory/finding (context type `security_advisory`), Change links, richer progress/residual risk summary per advisory (share remediated, oldest open), risk acceptance expiry reminder job (idempotent), notification `security.risk_review_due`. F8a already includes Finding state counts, fresh-observation re-verification and `security.advisory` notifications to `security.manage` holders.
3. **F8c Briefing aggregation (backend + UI):** computed feed with the sources above, `briefing/public` unchanged for manual items, per-source redaction rules and truncation flags, health entries, UI: IT Briefing page with grouped cards and clickable counts that deep-link to the filtered lists (e.g. devices affected by an advisory).

## Slice 1 status

F8a is implemented in the backend with migration `000047_security.up.sql`, the `security` module, the `integrations/advisories` port, the Endpoints inventory public contract, HTTP routes under `/api/v1/security`, audit, permissions, events, `security.advisory` notifications and the worker matching jobs. The Advisory and Finding transitions and reason codes are specified in the [state machines](../domain/state-machines.md#security-advisory--finding); the [current status](current-status.md) records the runtime surface. The slice does not include a frontend screen or a live feed adapter. Task and Change linkage and the computed Briefing feed remain F8b/F8c work.

## Reused concepts

Software Product / installation observations (F6), Device (F6), Task (F2), Change (F7), Planning contracts (F7), Notification, Approval (risk acceptance may later need approval; F8 records actor/reason/review date only), Relationship (platform), audit/outbox/jobs/permissions/i18n. No new task, notification, search or relationship system.

## New concepts

Security Advisory, Affected Criteria, Vulnerability Finding (confidence), Risk Acceptance, Briefing feed entry (computed). Terms are in the glossary or are added with the slice.

## Security and privacy

Advisory text and feed data are untrusted (safetext, length limits, escaped rendering, no HTML, URLs shown as text with `https` validation). Imports are bounded (size, record count). Findings reveal which devices are vulnerable: reads need `security.view` and, for device names, `endpoints.view`; employees see nothing. Matching never writes to endpoint data. Risk acceptance needs a reason code, an actor and a review date (maximum 12 months) and is audited; accepting is separate from `security.manage` triage (elevated `security.accept_risk`).

## Documentation obligations

Current status, glossary, state machines (Advisory/Finding implemented ops), core data model, module boundaries (Security row), workflow `security-remediation.md` status, generated references, OpenAPI, this document per slice.

## Not in F8

Live NVD/KEV/vendor feeds and credentials, CVSS/EPSS scoring beyond storing the provided severity, automatic confirmed findings, patch deployment/rings/publishing (F9), exploit intelligence, SBOM import, container/package scanners, per-user briefing dismissal.
