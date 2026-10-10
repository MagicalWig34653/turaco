# Architecture Decision Records

ADRs are immutable decision history. If a decision changes, add a new ADR that supersedes the old one. The status column is the lifecycle state of the decision; what is implemented is recorded in [current status](../product/current-status.md), which is authoritative.

| ADR | Decision | Status |
| --- | --- | --- |
| [ADR-0001](ADR-0001-modular-monolith.md) | Modular Monolith | Accepted |
| [ADR-0002](ADR-0002-go-backend.md) | Go Backend | Accepted |
| [ADR-0003](ADR-0003-postgresql-18.md) | PostgreSQL 18 | Accepted |
| [ADR-0004](ADR-0004-react-typescript-vite.md) | React/TypeScript/Vite Frontend | Accepted |
| [ADR-0005](ADR-0005-s3-object-storage.md) | S3-Compatible Object Storage | Accepted |
| [ADR-0006](ADR-0006-postgres-outbox-jobs.md) | PostgreSQL Outbox/Jobs Before External Broker | Accepted |
| [ADR-0007](ADR-0007-isolated-customer-data-planes.md) | Isolated Customer Data Planes | Accepted (direction) |
| [ADR-0008](ADR-0008-separate-agents.md) | Separate Connector and Endpoint Agents | Accepted |
| [ADR-0009](ADR-0009-language-localization.md) | English Technical Core and UI Localization | Accepted |
| [ADR-0010](ADR-0010-local-dev-colima.md) | Native macOS Toolchain + Colima Infrastructure | Accepted |
| [ADR-0011](ADR-0011-explicit-sql-pgx.md) | Explicit SQL with pgx | Accepted |
| [ADR-0012](ADR-0012-uuidv7.md) | UUIDv7 Internal Identifiers | Accepted |
| [ADR-0013](ADR-0013-authentication-abstraction.md) | Authentication Provider Abstraction | Accepted; follow-up note: Entra for every deployment variant (ADR-0035) |
| [ADR-0014](ADR-0014-application-level-secret-and-file-encryption.md) | Application-Level Secret and File Encryption | Accepted |
| [ADR-0015](ADR-0015-github-actions-ghcr.md) | GitHub Actions and GHCR | Accepted |
| [ADR-0016](ADR-0016-adobe-s3mock-local-development.md) | Adobe S3Mock for Local Development | Accepted |
| [ADR-0017](ADR-0017-turaco-project-name.md) | Turaco Project Name | Accepted; public brand clearance pending |
| [ADR-0018](ADR-0018-ai-model-routing.md) | Claude Code Model Routing | Accepted |
| [ADR-0019](ADR-0019-open-source-first.md) | Open-Source-First Product Direction | Proposed |
| [ADR-0020](ADR-0020-management-assignment-intelligence.md) | Provider-Agnostic Management Assignment Intelligence | Accepted; extended by ADR-0027 |
| [ADR-0021](ADR-0021-claude-code-cloud-environment.md) | Claude Code Cloud Sessions Reuse the Local Development Environment | Accepted |
| [ADR-0022](ADR-0022-ldap-client-library.md) | go-ldap as LDAP Client Library | Accepted |
| [ADR-0023](ADR-0023-gokrb5-kerberos.md) | gokrb5 for Kerberos/SPNEGO Authentication | Accepted |
| [ADR-0024](ADR-0024-outbox-dispatch-and-notifications.md) | Outbox Dispatch and Platform Notification Service | Accepted |
| [ADR-0025](ADR-0025-catalog-form-definitions.md) | Catalog Forms Are a Bounded Typed Schema, Not a Meta-Platform | Accepted |
| [ADR-0026](ADR-0026-remote-access-providers.md) | Remote Access Through Remote Access Providers | Accepted; partly implemented (R-A backend, R-B UI; attended launch-link connectors only) |
| [ADR-0027](ADR-0027-software-management-providers.md) | Software Lifecycle and Patch Orchestration Through Software Management Providers | Accepted; partly implemented (G1 to G4 against fake provider adapters) |
| [ADR-0028](ADR-0028-workforce-presence.md) | Workforce Presence for Operational Availability | Accepted; partly implemented (P-A backend, P-B UI; external sources not) |
| [ADR-0029](ADR-0029-turaco-ai.md) | Turaco AI — Provider-Independent, Tool-Based and User-Delegated | Accepted; partly implemented (read-only backend and UI; no proposals, write tools or MCP server) |
| [ADR-0030](ADR-0030-network-ipam-integrate-not-rebuild.md) | Network/IPAM is integrated, not rebuilt | Accepted by default; revisit when a NetBox instance or concrete IPAM need exists |
| [ADR-0031](ADR-0031-public-website-static-site.md) | Public Website as a Separate Static Site | Accepted for the project preview |
| [ADR-0032](ADR-0032-module-switches.md) | Optional Modules Are Switched at Runtime Through a Platform Module Registry | Accepted; implemented (backend and `/admin/modules`) |
| [ADR-0033](ADR-0033-workbench-views-query-engine.md) | Lists Are Queried Through a Platform Query Engine With Module-Declared Field Catalogs; Saved Views Are a Platform Concept | Accepted; implemented for Tickets, Devices and Tasks (Q-A to Q-D, backend and UI); further catalogs (Q-E) not started |
| [ADR-0034](ADR-0034-local-accounts-and-external-parties.md) | Local Accounts With Invitation Tokens and Restricted External Accounts | Accepted; local accounts implemented (backend and People UI); external accounts (A-G) not implemented |
| [ADR-0035](ADR-0035-entra-oidc-login.md) | Microsoft Entra ID Sign-in Through OpenID Connect for Every Deployment Variant | Proposed; not implemented |
| [ADR-0036](ADR-0036-microsoft-teams-integration.md) | Microsoft Teams as a Notification and Interaction Channel | Proposed; not implemented |
