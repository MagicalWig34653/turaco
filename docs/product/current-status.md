# Current Implementation Status

**Status date:** 2026-10-02

This file distinguishes implemented repository/runtime foundation from planned product behavior. Architecture and workflow documents describe the target design unless they explicitly say otherwise.

**F2 (Work Foundation, [design](f2-work-foundation-design.md)) is implemented:** the outbox dispatcher (slice 1); the Teams write API, Tasks and My Work with UI (slice 2); in-app notifications with UI (slice 3); the HTML email channel (slice 4); Recurring Task Definitions with UI (slice 5); manual IT Briefing publications with UI (slice 6). A Teams admin UI does not exist (Teams are managed through the API). F2 lives on the branch `f2-work-foundation` until it is merged.

**Milestone:** F1 (Identity and Organization, [plan](implementation-plan.md)) is complete: all six slices are implemented for on-prem deployments with one directory. Its open verification items (real Active Directory and Windows clients, automated directory/browser end-to-end tests) are listed under "Explicit stubs / not implemented yet". Next milestone: F3 (Products, Catalog and Requests).

## Implemented in the bootstrap repository

- Go module and Turaco binaries (`turaco-api`, `turaco-worker`, `turaco-migrate`, `turaco-docgen`, `turaco-admin`) plus Connector Agent and Endpoint Agent stubs.
- React/TypeScript/Vite application shell with English/German i18n foundation.
- PostgreSQL migration runner.
- Initial PostgreSQL schemas plus foundation tables for platform audit/outbox/jobs, organization, products and tasks.
- Health endpoints and `/api/v1/meta`.
- Organization read APIs (F1 slice 1): paginated, read-only `GET` endpoints for Users, Teams (with current members), Locations and observed Directory Groups (with observed User memberships). Data is written by directory synchronization (below); there is no write API except for Teams (next entry).
- Team write API (F2 slice 2): `POST /api/v1/teams`, `PATCH /teams/{id}`, `POST /teams/{id}/activate|deactivate`, `POST /teams/{id}/members`, `DELETE /teams/{id}/members/{userId}` (permission `organization.teams.manage`); Teams and memberships are platform-owned (source `platform`), active team names are unique, at most one current membership per user and team, every change audited (`organization.team.*`).
- Tasks and My Work backend (F2 slice 2; [design](f2-work-foundation-design.md), [state machine](../domain/state-machines.md#task)): `GET/POST /api/v1/tasks`, `GET/PATCH /tasks/{id}`, explicit actions `assign`, `unassign`, `start`, `block`, `unblock`, `complete`, `cancel`, `reopen`, and `GET /api/v1/my-work`. New permission `tasks.work` (see and work tasks assigned to oneself or one's Teams; `tasks.view` sees all, `tasks.manage` changes all); invisible tasks are 404. Optimistic `version`, every operation audited without title/description, `TaskAssigned`/`TaskCompleted` written to the outbox in the same transaction. Organization exposes `public.WorkDirectory` for active-user/team checks, names and a user's Teams. Task context (`context_type`/`context_id`) is set only through the `tasks/public` contract (below).
- Platform `authorization` package: request `Principal`, `Authenticator` interface and `Require(permission)` middleware; default-deny `DenyAll` remains the fallback. There is deliberately no development bypass.
- Roles and permissions (F1 slice 5; [design](../security/identity-access-design.md)): custom roles (registry-validated permission sets) plus the immutable built-in `platform-administrator`, assigned to Users or Directory Groups with `global` scope; session permissions are evaluated per request from direct and transitive Directory Group assignments; last-administrator guard; soft-deleted roles keep assignment history; API `GET /permissions`, `/roles*`, `/role-assignments*`.
- Audit query (F1 slice 6): `GET /api/v1/audit-events` with filters and keyset paging (`platform.audit.view`); all privileged identity/configuration changes listed in the design are audited.
- Login (F1 slice 3b): `POST /api/v1/auth/login` verifies the password by binding to the directory as the synced account (no service account in the API), uniform failures with a 400 ms floor, attempts reserved atomically before verification per account and per client (IPv6 per /64; trusted proxies via `HTTP_TRUSTED_PROXIES`), session creation serialized with User status changes; `GET /api/v1/auth/methods`.
- Kerberos/SPNEGO login (F1 slice 4): `GET /api/v1/auth/kerberos` validates Negotiate tickets with a keytab (`KERBEROS_*`), maps the principal to the synced directory account and issues a session; the UI tries it automatically when configured.
- Emergency (break-glass) account: argon2id local credential for a dedicated User, created and enabled only with `turaco-admin`, login at `POST /api/v1/auth/emergency-login` only when `AUTH_EMERGENCY_LOGIN_ENABLED=true`, sessions capped at one hour, every use audited and logged at error level.
- `turaco-admin` operator CLI (shipped in the worker image): `role list|grant|revoke`, `emergency create|set-password|enable|disable`.
- In-app notifications backend (F2 slice 3; [ADR-0024](../decisions/ADR-0024-outbox-dispatch-and-notifications.md)): platform `notifications` service (`platform.notifications`, idempotent per recipient and dedupe key, texts localized by the client from category and params), `GET /api/v1/notifications` (newest first, `unread=true`), `GET /notifications/unread-count` (capped at 100), `POST /notifications/{id}/read`, `POST /notifications/read-all`; any signed-in user, own notifications only (`RequireAuthenticated`). The Tasks module registers outbox consumers in `turaco-worker`: `TaskAssigned` notifies the assigned User and the current members of the assigned Team (not the actor, only active Users, stale events ignored) and `TaskCompleted` notifies the creator unless they completed it. Categories: `task.assigned`, `task.completed`. Recipients must be active Users holding `tasks.work`, `tasks.view` or `tasks.manage`; an "assigned" notification for the same task is not repeated within one hour.
- Notifications UI (F2 slice 3): Notifications screen (own notifications, unread filter, mark read/all read, links to tasks) and an unread counter in the navigation that polls every 60 s and on window focus; texts are localized in English and German from category and params. No realtime push yet (SSE only when polling is insufficient).
- HTML email channel (F2 slice 4; [email channel](../integrations/teams-email.md)): `turaco-worker` sends notification emails through an SMTP relay (standard library only; STARTTLS required by default, implicit TLS or development-only clear text) when `SMTP_HOST` is configured. Deliveries (`platform.notification_deliveries`, `pending → sending → delivered | failed | cancelled`) are created with the notification and sent by retryable jobs; recipients must be active Users with a primary email address and can opt out per category (`GET /notifications/preferences`, `PUT /notifications/preferences/{category}/email`, shown on the Notifications screen). Bilingual server-side templates; at-least-once delivery. Verified against an in-process fake relay only, not against a real mail server.
- Recurring Task Definitions backend (F2 slice 5): `GET/POST /api/v1/recurring-task-definitions`, `GET/PATCH/DELETE /{id}`, `POST /{id}/pause|resume` (permission `tasks.recurrence.manage`, audited). A definition has a title/description/priority, optional assignees and due offset and a rule (daily, weekly or monthly every N, local time of day in an IANA time zone, anchored at a start date; monthly days are clamped to the last day). The scheduled job `tasks.recurrence.generate` (every minute, `turaco-worker`) creates one Task per due definition for its oldest due run, advances to the first run after now (missed runs are not caught up), is idempotent per (definition, run) and drops inactive assignees from generated tasks; generated tasks are audited as `tasks.task.created` by the system actor `recurrence` and carry `recurrenceDefinitionId`. UI: Recurring Tasks under Administration (list with pause/resume/delete, create, detail and edit with schedule, assignment and due offset; English and German).
- IT Briefing manual publications backend (F2 slice 6): `GET/POST /api/v1/briefing-items`, `GET/PATCH/DELETE /{id}`, `POST /{id}/publish|withdraw`. Permissions `briefing.view` (published, unexpired items) and `briefing.manage` (all items, create/edit/publish/withdraw/delete); plain-text title, body, severity (info, warning, critical) and optional expiry; drafts are editable and deletable, published items are immutable and can only be withdrawn; audited without content; `BriefingItemPublished` is written to the outbox (no consumer). UI: IT Briefing in the navigation (readers see published items; managers see all with a status filter, create, edit drafts, publish, withdraw and delete drafts; English and German). No aggregation of other modules' records yet (F8).
- Task and My Work UI (F2 slice 2): My Work (own unfinished tasks), task list with filters, create, detail with lifecycle actions (reason dialogs for block/cancel/reopen), edit and assign (user or Team search); navigation visible with any of `tasks.view`/`tasks.work`/`tasks.manage`; English and German.
- Web UI (F1): login (automatic Kerberos attempt when configured, password, and emergency when enabled), app shell with permission-filtered navigation, current user, roles, role assignments, directory sync runs and audit events; English and German.
- Platform `authentication` package (F1 slice 2): server-side sessions (`platform.sessions`, only a SHA-256 hash of the opaque token is stored), idle and absolute expiry, explicit `Create`/`Authenticate`/`Revoke` operations (creation and revocation are audited in the same transaction), `turaco_session` cookie (HttpOnly, SameSite=Lax, Secure by configuration), same-origin CSRF guard for unsafe methods, `GET /api/v1/auth/session` and `POST /api/v1/auth/logout`. A session is valid only while the Organization User is `active`.
- LDAP/AD directory synchronization (F1 slice 3; [design](../integrations/ldap-ad-sync-design.md), [operation](../integrations/ldap-ad.md)): one directory per deployment configured with `LDAP_*` (bind password from a deployment secret file, LDAPS/StartTLS with verification). `turaco-worker` syncs every `LDAP_SYNC_INTERVAL` and on manual request: Users and External Identities (matched only by objectGUID/entryUUID, never linked by email), directory-owned User fields, Directory Groups, membership and direct group nesting as interval history, the User status rule with `status_source`, session revocation when a User leaves `active`, mass-removal safeguard (`sweep_withheld`), per-entry handling of malformed data, run records with counts and conflicts, audit and `UserSynchronized` outbox events (written to the outbox; the dispatcher acknowledges them, no consumer exists yet).
- Directory sync API: `GET /api/v1/directory-sync-runs[/{id}]`, `POST /api/v1/directory-sync-runs` (permission `organization.directory.sync`, 409 when sync is not configured); `GET /api/v1/users/{id}` returns `externalIdentities`; group members list only currently observed members with `observedFrom`.
- Platform job runner (`backend/internal/platform/jobs`): PostgreSQL queue with dedupe keys, retry with backoff, permanent failure, stale-lock reclaim and interval schedules (ADR-0006).
- Outbox dispatcher (F2 slice 1, [ADR-0024](../decisions/ADR-0024-outbox-dispatch-and-notifications.md)): `turaco-worker` claims due `platform.outbox_events` one at a time and runs registered consumers in the claim transaction, with retry/back-off and terminal `failed`; events without a consumer are acknowledged. The Tasks module registers the `TaskAssigned` and `TaskCompleted` consumers (notifications); `UserSynchronized` and `BriefingItemPublished` have none. An empty event type filter claims every event type.
- Claude Code cloud sessions bootstrap automatically and run the full `make check`/`make build` (ADR-0021); database tests are required in CI and cloud (`TURACO_REQUIRE_DB_TESTS`) and run against a separate migrated test database locally and in the cloud (`TEST_DATABASE_URL`).
- Permission, event and configuration registries with generated reference documentation.
- Architecture boundary checker and Markdown-link checker.
- Local Colima/Docker Compose dependencies: PostgreSQL and S3Mock.
- Production-oriented Dockerfiles for API, worker and web shell.
- GitHub Actions scaffolding for CI, security analysis and GHCR container publication.
- Claude Code project instructions, model-routed Opus/Sonnet skills/subagents, reviewer agents, context policy and deterministic hooks.
- Product/architecture/domain/security/workflow/operations documentation.

- Products backend (F3 slice 1): `GET/POST /api/v1/manufacturers`, `PATCH /manufacturers/{id}`, `GET/POST /api/v1/product-categories`, `PATCH /product-categories/{id}`, `GET/POST /api/v1/products`, `GET/PATCH /products/{id}`, `POST /products/{id}/activate|deactivate`. Permissions `products.view` (read) and `products.manage` (write, audited); optimistic versions, unique manufacturer names, category names per parent and internal part numbers, soft deactivation of products; categories are a tree whose parent is fixed at creation. Products `public.Directory` serves other modules. No UI yet; no Product Variants.

- Tasks public contract (F3 slice 2, `modules/tasks/public`): other modules create Tasks that belong to one of their records (typed context) inside their own transaction (`CreateInTx`, audited, `TaskAssigned` when assigned), cancel all unfinished tasks of a context (`CancelByContextInTx`, audited, `TaskCancelled` per task) and read task state (`Tasks`, `StatusesInTx`). Cancelling a task by hand also emits `TaskCancelled`.

## Explicit stubs / not implemented yet

- Connector Agent transport and LDAP/AD operations.
- Endpoint Agent enrollment/transport/inventory/deployment operations beyond capability placeholder.
- OIDC/Entra login (hosted environments).
- Session cleanup job for expired/revoked sessions (needs an index on `absolute_expires_at`) and session listing/administration.
- Scoped role assignments (only `global` exists until the first scoped module).
- Real object-store client and envelope encryption implementation.
- Email bounce handling, unsubscribe headers, digests, per-recipient language, tenant branding, delivery history UI; outbox consumers beyond the Task notifications (`UserSynchronized` and `BriefingItemPublished` have none); retention/pruning of processed outbox events and notifications.
- Organization write APIs other than Teams, Departments/Cost Centers APIs and directory mapping of department/location/cost center, Directory Group *Device* memberships.
- Connector Agent `ldap.*` capabilities (hosted deployments), DB-managed/multi-directory configuration (waits for ADR-0014 key management).
- Automated tests against a real directory and browser: the LDAP adapter and login are tested with fakes; OpenLDAP sync and login, Kerberos login against an MIT KDC (curl GSS-API), and a Playwright run through login and all admin screens were verified manually (scripts not yet in CI). Kerberos with real Windows clients/Active Directory still needs verification. Active Directory specifics (objectGUID, userAccountControl, range retrieval) must be verified against a real AD before production use.
- Scheduled cleanup of expired login-throttle rows and sessions: throttle rows are pruned opportunistically during logins; a worker job should replace this together with session cleanup.
- Frontend screens for Organization data beyond user/group pickers.

## Known limitations of the Organization read slice

- Lists are ordered by UUIDv7 id (roughly creation order), not by name.
- The migrator runs each file in one transaction, so indexes on large existing tables cannot be built `CONCURRENTLY`; revisit before tables grow large.
- Directory sync applies a whole snapshot in one transaction; it is designed for directories of about 50k users but tested only up to 3,000, so larger directories need measurement.
- No per-query timeout beyond the HTTP server write timeout.

## Planned capabilities (not implemented)

- Service Catalog, Service Requests and Approvals.
- Inventory, procurement and Asset application behavior.
- Service Desk/Tickets/Major Incidents/Problems.
- Knowledge Base/Runbooks.
- Intune, Autotask and Teams integrations (the SMTP email channel exists, see above).
- Intune Assignment Intelligence (Directory Group/User/Device views, normalized artifacts/assignments/filters, effective-applicability evaluation, Assignment Paths, reverse lookup/history/diff).
- Endpoint intelligence/software normalization.
- Infrastructure/CMDB/IPAM.
- Change/Initiative implementation.
- Security advisory ingestion/correlation and IT Briefing aggregation of other modules' records (F8).
- Patch/deployment management.
- Remote Support.

