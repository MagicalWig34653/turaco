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
- Platform `authorization` package: request `Principal`, `Authenticator` interface and `Require(permission)` middleware; default-deny `DenyAll` remains the fallback. There is deliberately no development bypass.
- Platform `authentication` package (F1 slice 2): server-side sessions (`platform.sessions`, only a SHA-256 hash of the opaque token is stored), idle and absolute expiry, explicit `Create`/`Authenticate`/`Revoke` operations (creation and revocation are audited in the same transaction), `turaco_session` cookie (HttpOnly, SameSite=Lax, Secure by configuration), same-origin CSRF guard for unsafe methods, `GET /api/v1/auth/session` and `POST /api/v1/auth/logout`. A session is valid only while the Organization User is `active`.
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
- Login: no identity provider can create a session yet (LDAP/AD bind in F1 slice 3, Kerberos/SPNEGO in slice 4, OIDC later). `Service.Create` exists for them; without a provider no session can be established outside tests.
- Session cleanup job for expired/revoked sessions (needs an index on `absolute_expires_at`), revoking sessions when a user leaves `active`, and session listing/administration.
- Scope evaluation, role administration and permission assignment (permission checks exist; sessions currently carry **no permissions**, so authenticated calls to permission-protected routes return `403` until F1 slice 5).
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
