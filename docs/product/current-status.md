# Current Implementation Status

**Status date:** 2026-10-01

This file distinguishes implemented repository/runtime foundation from planned product behavior. Architecture and workflow documents describe the target design unless they explicitly say otherwise.

## Implemented in the bootstrap repository

- Go module and Turaco binaries (`turaco-api`, `turaco-worker`, `turaco-migrate`, `turaco-docgen`) plus Connector Agent and Endpoint Agent stubs.
- React/TypeScript/Vite application shell with English/German i18n foundation.
- PostgreSQL migration runner.
- Initial PostgreSQL schemas plus foundation tables for platform audit/outbox/jobs, organization, products and tasks.
- Health endpoints and `/api/v1/meta`.
- Organization read APIs (F1 slice 1): paginated, read-only `GET` endpoints for Users, Teams (with current members), Locations and observed Directory Groups (with observed User memberships). Data is written by directory synchronization (below); there is no write API.
- Platform `authorization` package: request `Principal`, `Authenticator` interface and `Require(permission)` middleware; default-deny `DenyAll` remains the fallback. There is deliberately no development bypass.
- Platform `authentication` package (F1 slice 2): server-side sessions (`platform.sessions`, only a SHA-256 hash of the opaque token is stored), idle and absolute expiry, explicit `Create`/`Authenticate`/`Revoke` operations (creation and revocation are audited in the same transaction), `turaco_session` cookie (HttpOnly, SameSite=Lax, Secure by configuration), same-origin CSRF guard for unsafe methods, `GET /api/v1/auth/session` and `POST /api/v1/auth/logout`. A session is valid only while the Organization User is `active`.
- LDAP/AD directory synchronization (F1 slice 3; [design](../integrations/ldap-ad-sync-design.md), [operation](../integrations/ldap-ad.md)): one directory per deployment configured with `LDAP_*` (bind password from a deployment secret file, LDAPS/StartTLS with verification). `turaco-worker` syncs every `LDAP_SYNC_INTERVAL` and on manual request: Users and External Identities (matched only by objectGUID/entryUUID, never linked by email), directory-owned User fields, Directory Groups, membership and direct group nesting as interval history, the User status rule with `status_source`, session revocation when a User leaves `active`, mass-removal safeguard (`sweep_withheld`), per-entry handling of malformed data, run records with counts and conflicts, audit and `UserSynchronized` outbox events (written to the outbox only; nothing dispatches them yet).
- Directory sync API: `GET /api/v1/directory-sync-runs[/{id}]`, `POST /api/v1/directory-sync-runs` (permission `organization.directory.sync`; returns 403 for everyone until slice 5 grants permissions, 409 when sync is not configured); `GET /api/v1/users/{id}` returns `externalIdentities`; group members list only currently observed members with `observedFrom`.
- Platform job runner (`backend/internal/platform/jobs`): PostgreSQL queue with dedupe keys, retry with backoff, permanent failure, stale-lock reclaim and interval schedules (ADR-0006). Outbox dispatch is not implemented.
- Claude Code cloud sessions bootstrap automatically and run the full `make check`/`make build` (ADR-0021); database tests are required in CI and cloud (`TURACO_REQUIRE_DB_TESTS`).
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
- Login: no identity provider can create a session yet (LDAP bind in F1 slice 3b, Kerberos/SPNEGO in slice 4, OIDC later). `Service.Create` exists for them; without a provider no session can be established outside tests. The future login must serialize session creation with User status changes, otherwise a session created while sync deactivates the User could revive on reactivation.
- Session cleanup job for expired/revoked sessions (needs an index on `absolute_expires_at`) and session listing/administration.
- Scope evaluation, role administration and permission assignment (permission checks exist; sessions currently carry **no permissions**, so authenticated calls to permission-protected routes return `403` until F1 slice 5).
- Real object-store client and envelope encryption implementation.
- Outbox dispatch/consumers (events are written but not delivered).
- Organization write APIs, Departments/Cost Centers APIs and directory mapping of department/location/cost center, Directory Group *Device* memberships, group-to-role mapping (slice 5).
- Connector Agent `ldap.*` capabilities (hosted deployments), DB-managed/multi-directory configuration (waits for ADR-0014 key management).
- Automated tests against a real directory: the adapter is tested with fakes; Active Directory specifics (objectGUID, userAccountControl, range retrieval) must be verified against a real AD before production use. OpenLDAP was verified manually end to end.
- Frontend for Organization data.

## Known limitations of the Organization read slice

- Lists are ordered by UUIDv7 id (roughly creation order), not by name.
- The migrator runs each file in one transaction, so indexes on large existing tables cannot be built `CONCURRENTLY`; revisit before tables grow large.
- Directory sync applies a whole snapshot in one transaction; it is designed for directories of about 50k users but tested only up to 3,000, so larger directories need measurement.
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
