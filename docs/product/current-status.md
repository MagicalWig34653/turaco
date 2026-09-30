# Current Implementation Status

**Status date:** 2026-09-30

This file distinguishes implemented repository/runtime foundation from planned product behavior. Architecture and workflow documents describe the target design unless they explicitly say otherwise.

## Implemented in the bootstrap repository

- Go module and Turaco binaries (`turaco-api`, `turaco-worker`, `turaco-migrate`, `turaco-docgen`) plus Connector Agent and Endpoint Agent stubs.
- React/TypeScript/Vite application shell with English/German i18n foundation.
- PostgreSQL migration runner.
- Initial PostgreSQL schemas plus foundation tables for platform audit/outbox/jobs, organization, products and tasks.
- Health endpoints and `/api/v1/meta`.
- Organization read APIs (F1 slice 1): paginated, read-only `GET` endpoints for Users, Teams (with current members), Locations and observed Directory Groups (with observed User memberships). Data is only present if written by other means; no synchronization or write API exists yet.
- Platform `authorization` package: request `Principal`, `Authenticator` interface and `Require(permission)` middleware. The API is wired with a default-deny authenticator, so every Organization endpoint currently returns `401` until authentication sessions (F1 slice 2) exist. There is deliberately no development bypass.
- Permission, event and configuration registries with generated reference documentation.
- Architecture boundary checker and Markdown-link checker.
- Local Colima/Docker Compose dependencies: PostgreSQL and S3Mock.
- Production-oriented Dockerfiles for API, worker and web shell.
- GitHub Actions scaffolding for CI, security analysis and GHCR container publication.
- Claude Code project instructions, model-routed Opus/Sonnet skills/subagents, reviewer agents, context policy and deterministic hooks.
- Product/architecture/domain/security/workflow/operations documentation.

## Explicit stubs / not implemented yet

- Connector Agent transport and LDAP/AD operations.
- Endpoint Agent enrollment/transport/inventory/deployment operations beyond capability placeholder.
- Authentication/session/Kerberos/OIDC behavior (only the default-deny `Authenticator` seam exists).
- Scope evaluation and role administration (permission checks exist; scopes do not).
- Real object-store client and envelope encryption implementation.
- Background outbox/job processing beyond worker process/DB health bootstrap.
- Organization write APIs, LDAP/AD synchronization, Departments/Cost Centers APIs, Directory Group *Device* memberships and membership history.
- Frontend for Organization data.

## Known limitations of the Organization read slice

- Lists are ordered by UUIDv7 id (roughly creation order), not by name.
- Directory Group membership has no first-observed/removed marker; the directory-sync slice must decide between soft-removal and history before writing data, and add non-empty/`deleted_observed_at` CHECK constraints then.
- The migrator runs each file in one transaction, so indexes on large existing tables cannot be built `CONCURRENTLY`; revisit before sync writes large volumes.
- No per-query timeout beyond the HTTP server write timeout.
- My Work/Task application APIs and UI.
- Service Catalog, Service Requests and Approvals.
- Inventory, procurement and Asset application behavior.
- Service Desk/Tickets/Major Incidents/Problems.
- Knowledge Base/Runbooks.
- Intune, Autotask, Teams and SMTP integrations.
- Intune Assignment Intelligence (Directory Group/User/Device views, normalized artifacts/assignments/filters, effective-applicability evaluation, Assignment Paths, reverse lookup/history/diff).
- Endpoint intelligence/software normalization.
- Infrastructure/CMDB/IPAM.
- Change/Initiative implementation.
- Security advisory ingestion/correlation and IT Briefing behavior.
- Patch/deployment management.
- Remote Support.

## Maintenance rule

Any pull request that moves a capability from planned/stub to implemented (or removes implemented behavior) must update this file. Do not mark a capability implemented merely because interfaces, migrations or placeholders exist.
