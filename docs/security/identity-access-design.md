# F1 Completion — Login, Roles, Audit and Admin UI: Feature Design

**Status:** Accepted 2026-10-01 (decisions E1–E4). Covers F1 slices 3b (LDAP login), 4 (Kerberos/SPNEGO), 5 (roles/permissions/scope), 6 (audit of privileged changes) and the F1 web UI. [Current status](../product/current-status.md) is authoritative for what exists.

Context: [security architecture](security-architecture.md), [LDAP/AD](../integrations/ldap-ad.md), [directory sync design](../integrations/ldap-ad-sync-design.md), ADR-0013 (authentication abstraction), ADR-0014 (secret encryption), [state machines](../domain/state-machines.md#user-status).

## Decisions

- **E1 Administrator bootstrap:** the first administrator is granted with the audited `turaco-admin` CLI run against the database. An optional local **emergency account** (break-glass) with an argon2id password hash exists for directory outages; it is disabled unless explicitly enabled, every use is audited and logged at error level.
- **E2 Kerberos:** `turaco-api` validates SPNEGO/Negotiate tickets natively (gokrb5, ADR-0023) with a keytab deployment secret.
- **E3 Roles:** custom roles (named permission sets) managed through the API, plus one immutable built-in `platform-administrator` role. Roles are assigned to Users and to Directory Groups. Scope is `global` only; scoped assignments arrive with the first scoped module.
- **E4 UI:** F1 ships a login page (Kerberos attempt, password form, emergency login when enabled), logout, a current-user view and admin screens for roles, role assignments, directory sync runs and audit events (EN/DE).

## 1. Outcome

People log in with their directory password or transparently with Windows SSO and receive server-side sessions whose permissions come from roles. Administrators manage roles and see what changed in the audit log. When the directory is unavailable, an operator can still get in through the CLI or the emergency account.

## 2. Reused concepts

User, External Identity, Directory Group (incl. nesting), session service (`platform/authentication`), `authorization.Principal`/`Require`, permission registry, Audit Event, job/outbox conventions, organization read APIs (user/group pickers), i18n.

## 3. New concepts

- **Role** — a named set of registered permissions; custom or the built-in `platform-administrator` (all permissions, immutable). Platform-owned (`platform/authorization`), because authorization is a platform service (module-boundaries).
- **Role Assignment** — Role → User or Directory Group, scope `global`; history kept (revoked, never deleted).
- **Local Credential** — password hash for an emergency account's User; platform-owned (`platform/authentication`). Not a second identity model: the account is an ordinary Organization User without a directory identity.
- **Login throttle** — per-identifier and per-client counters protecting the directory against brute force and AD lockout.

Glossary additions: Role, Role Assignment, Emergency Account.

## 4. Ownership and boundaries

| Area | Owner | Notes |
| --- | --- | --- |
| request contract (`Principal`, `Require`) | `platform/authorization` | small package every transport imports |
| roles, role_permissions, role_assignments | `platform/authorization/roles` (+ `roles/transport`), schema `platform` | no FKs into business schemas (same rule as sessions) |
| permission evaluation | `platform/authorization/roles.Evaluator` | needs a User's Directory Groups → injected `GroupResolver`/`SubjectDirectory` implemented by `organization/public.AuthorizationSubjects` (platform must not import modules) |
| login, local credentials, throttle, Kerberos | `platform/authentication` | account lookup and user locking through injected interfaces implemented by `organization/public` |
| LDAP password verification | `integrations/ldap` (`PasswordVerifier`) | no service account needed |
| Kerberos ticket validation | `integrations/kerberos` (gokrb5) | returns principal name only |
| emergency User creation | `organization/application` operation exposed via `organization/public` | |
| audit writing and query | `platform/audit` (`Record`, `Actor`; query + `audit/transport`) | one actor format for all audited changes |
| CLI | `backend/cmd/turaco-admin` | composition root like the other binaries |

## 5. Slice 5 — Roles, permissions, scope

**Data (migration `000010_authorization`):**
- `platform.roles(id uuid pk, key text CHECK ^[a-z0-9][a-z0-9-]{1,62}$, name text, description text, built_in bool, created_at, updated_at, deleted_at)`; partial unique index on `key` among non-deleted roles; the built-in role can never be deleted; seed `platform-administrator` (built_in).
- `platform.role_permissions(role_id → roles, permission text, PK(role_id, permission))`; never rows for the built-in role.
- `platform.role_assignments(id uuid pk, role_id → roles, subject_type CHECK in ('user','directory_group'), subject_id uuid, scope text CHECK = 'global', created_at, created_by jsonb, revoked_at, revoked_by jsonb)`; partial unique `(role_id, subject_type, subject_id, scope) WHERE revoked_at IS NULL`; index `(subject_type, subject_id) WHERE revoked_at IS NULL`.

**Evaluation** (`authorization.PermissionLoader` used by `SessionAuthenticator`, per request, no cache):
1. Groups of the User = currently observed Directory Group memberships of non-deleted groups, expanded transitively over currently observed nesting edges (cycle-safe recursive CTE) — `organization/public.AuthorizationSubjects.GroupIDsOfUser`.
2. Active assignments for the User and for those groups.
3. `platform-administrator` → every registered permission; other roles → their stored permissions ∩ registry (unknown stored permissions are ignored).
Permissions therefore change immediately when roles, assignments or directory memberships change.

**Operations** (all in one transaction with their audit entry; `platform.roles.manage` unless noted):
- `CreateRole(key, name, description, permissions)`; `UpdateRole(name, description)`; `SetRolePermissions(permissions)` (full replacement, validated against the registry); `DeleteRole` only when it has no active assignments — a soft delete that keeps the role and its revoked assignments as history and frees the key. The built-in role cannot be changed or deleted. Each operation locks the role row in its own statement before reading it, so concurrent assignment and deletion serialize correctly.
- `AssignRole(role, subject)` — subject must exist (User via organization, Directory Group not deleted); duplicate active assignment → 409. `RevokeAssignment(id)`.
- Last-administrator guard: revoking the last active `platform-administrator` assignment of an **active** User is refused (409) unless done through the CLI. Group assignments and inactive Users are not counted (group membership can change outside Turaco; inactive Users cannot sign in).

**Audit actions:** `authorization.role.created|updated|permissions_changed|deleted`, `authorization.role.assigned|assignment_revoked` (before/after; actor = session user or CLI, see §9).

**Permissions (registry):** `platform.roles.view` (normal), `platform.roles.manage` (high), `platform.audit.view` (elevated). Existing `platform.admin` stays for future platform configuration.

**Group-to-role mapping risk:** whoever controls membership of a mapped directory group controls the Turaco role. The admin UI shows this warning when assigning to a group.

## 6. Slice 3b — LDAP password login

`POST /api/v1/auth/login {identifier, password}`:
1. Validate input (identifier ≤ 256 bytes, password 1–1024 bytes; an **empty password is rejected before any LDAP call** — an empty simple bind is an unauthenticated bind and would succeed).
2. Reserve a client attempt (throttle, below) → `429 auth.too_many_attempts` with `Retry-After`.
3. Resolve the account via `organization/public.LoginAccounts.FindDirectoryAccount(providerKey, identifier)`: identifier `DOMAIN\user` → `user`; containing `@` → match `primary_email` (lower()); otherwise `username` (lower()). The identity must be enabled, not deleted, have a non-empty DN, and its User must be `active`; exactly one match. Then reserve an account attempt (or, for an unknown account, an attempt on the normalized identifier) → 429.
4. Verify the password by binding **as the stored DN** over LDAPS/StartTLS (`integrations/ldap.PasswordVerifier`): no service account, same TLS policy as sync. Result code 49 → invalid credentials; connection/TLS failure → `503 auth.provider_unavailable`.
5. On success create the session in one transaction (`lock_timeout` 5 s → `503 auth.temporarily_unavailable`) that first locks the User row (`FOR SHARE` through `authentication.UserLocker.LockActiveUser`, implemented by `organization/public.LoginAccounts`), so creation serializes with a concurrent status change (directory sync locks the row `FOR UPDATE`); revoke the previous session if the request carried one (actor `login`, metadata `replacedByUserId`); set the cookie; `204`. Release the reserved attempts (account key cleared, client key decremented).
6. Every failure answers `401 auth.invalid_credentials` with the same body after a minimum response time of 400 ms (unknown account, disabled, wrong password are indistinguishable); audit `auth.login.failed` with target User (or `login`/`unknown`) and `{method, reason, clientIp}` — never the typed identifier, which may contain a mistyped password; success is audited by session creation (`auth.session.created`, authMethod `ldap`). Known limitation: a directory bind slower than 400 ms, or a directory outage (503 only for known accounts), can reveal that an account exists.

The API therefore needs the directory **connection** settings (`LDAP_URL`, StartTLS, CA, directory type, provider key) but still never the bind secret: `config.LoadLDAPConnection` for the API, `config.LoadLDAP` (adds bind/search settings) for the worker.

**Throttle** (`platform.auth_throttle(key text pk, failures int, window_started_at, locked_until, updated_at)`, database clock): attempts are **reserved atomically before** any directory or hash work (one upsert that increments and returns the state), so concurrent requests cannot exceed the budget. Keys: client `ip:<address>` (IPv4 address, IPv6 /64) limit 30; account `acct:<user id>` limit 5, or `id:<sha256 of the normalized identifier>` for unknown accounts, so `alice`, `DOMAIN\alice` and `ALICE` share one budget. Exceeding a limit locks the key for 15 minutes (attempts during a lock do not extend it). Locked requests never reach LDAP, which keeps Turaco from triggering AD lockout policies. Old rows are pruned with bounded work. Client address = `RemoteAddr`, or, when `RemoteAddr` is a trusted proxy (`HTTP_TRUSTED_PROXIES`), the right-most untrusted entry among the last 32 `X-Forwarded-For` entries. Without real client addresses the client limit becomes global (see deployment docs).

## 7. Emergency account (break-glass)

- `platform.local_credentials(user_id uuid pk, login_name text unique CHECK pattern, password_hash text, enabled bool, created_at, updated_at, password_changed_at, last_used_at)`.
- Hash: argon2id (`golang.org/x/crypto/argon2`, already in the module graph), m=64 MiB, t=3, p=2, 16-byte salt, PHC string; verification in constant time; minimum password length 16.
- Created only by CLI: `turaco-admin emergency create --login <name> --display-name <text>` creates an Organization User (no directory identity, `status_source = platform`) via `organization/public`, stores the hash, prints a generated password once (or reads `--password-stdin`). Also `emergency set-password|disable|enable`. The CLI can then grant it a role.
- Lifecycle operations live in `platform/authentication.EmergencyAccounts` (the CLI only parses arguments): create (Organization User + disabled credential in one transaction), change password and disable (both revoke the account's sessions), enable. Audit `auth.emergency_account.created|password_changed|enabled|disabled` and `organization.user.created_local`.
- Login: `POST /api/v1/auth/emergency-login {login, password}` exists only when `AUTH_EMERGENCY_LOGIN_ENABLED=true` (otherwise 404). Only the client throttle applies (an account lock would let strangers block the break-glass path; the generated password has about 140 bits). Hash verification waits at most 1 s for one of four slots, else `429` with a short `Retry-After`. Session `auth_method = emergency` with absolute lifetime capped at 1 hour; the session is only created if the credential is still enabled with the verified hash at commit time, so a concurrent disable or password change wins. Audit `auth.emergency_login.succeeded|failed`; success also logs at ERROR level ("emergency account used") for alerting.

## 8. Slice 4 — Kerberos/SPNEGO

- `GET /api/v1/auth/kerberos`: `404` when `KERBEROS_KEYTAB_FILE` is not configured (Kerberos also requires the LDAP connection settings, because principals map to synced accounts). Without `Authorization: Negotiate …`, or with an NTLM token (browsers fall back to NTLM when they get no Kerberos ticket) → `401` with `WWW-Authenticate: Negotiate`, not throttled or audited. With a Kerberos token: reserve the client attempt, validate it with the keytab (`integrations/kerberos`, gokrb5, ADR-0023) for the configured `HTTP/<host>` service principal only, then map `user` → `FindDirectoryAccount(providerKey, user)` → session (`auth_method = kerberos`) → `204` with cookie.
- Validation: only AES keys of the service principal in `KERBEROS_REALM` are loaded from the keytab (keys of other principals, realms or RC4/DES types are ignored); the ticket realm, the KDC-encrypted client realm and the authenticator's client realm must all equal `KERBEROS_REALM` (identity is read from the encrypted ticket part, never from the client-encrypted authenticator); the principal must have a single component with a plain username. The synced directory username must equal the principal name exactly or ASCII-case-insensitively, so Unicode case folding in the directory lookup cannot map a different principal onto an account.
- Clock skew `KERBEROS_MAX_CLOCK_SKEW` (default 5 minutes, at most 15) and gokrb5's per-process replay cache apply; verification is serialized per process so one captured token is accepted at most once per instance. PAC data is not used, so a user disabled in AD keeps Kerberos access until the next directory sync marks the User inactive. Tickets with client address restrictions are refused.
- Kerberos attempts use their own client throttle budget (`ip:krb/<client>`), so failed SSO attempts never lock users out of password login and vice versa.
- Because this GET creates a session and browsers attach Negotiate credentials to cross-site requests, requests with `Sec-Fetch-Site` other than `same-origin`/`none`, or with an `Origin` whose host differs from the request host, are rejected (login-CSRF protection).
- Failures answer the uniform 401 (+ `WWW-Authenticate: Negotiate`) after the password-login minimum time and are audited as `auth.login.failed` (method `kerberos`, reason `invalid_ticket|unknown_account|user_inactive`) without ticket contents or principal names.
- The UI calls it first when `GET /auth/methods` reports Kerberos; any non-204 shows the password form; after an explicit logout the attempt is skipped in that tab. Browsers send Negotiate only to sites in their intranet/trusted zone.
- Verified end to end against an MIT KDC with curl's GSS-API client (manual; see current status).

## 9. Slice 6 — Audit of privileged changes

- Audited privileged identity/config changes (complete list, enforced by tests per operation): role create/update/permissions/delete, role assign/revoke, emergency account create/password/enable/disable/use (`auth.emergency_account.*`, `auth.emergency_login.*`, `organization.user.created_local`), login failures, session create/revoke, user created/status/email changed by directory, directory sync requested/sweep withheld.
- Every audited change is written with `audit.Record` in the same transaction. Actor: a signed-in User (`actor_id`), or a system actor (`actor_id` NULL, metadata `{"actor": "cli"|"directory-sync"|"login", "osUser": …}` — `osUser` only for the CLI). Correlation ID = the server-generated request ID (client `X-Request-ID` values are only logged), the sync run ID, or a CLI-generated ID.
- `GET /api/v1/audit-events?targetType=&targetId=&action=&actionPrefix=&actorId=&correlationId=&from=&to=&limit=&cursor=` newest first by `(occurred_at, id)` with an opaque keyset cursor, permission `platform.audit.view`; `actionPrefix` is a literal prefix evaluated as an index range; `targetId` requires `targetType`. Indexes `(occurred_at DESC, id DESC)`, `(actor_id, occurred_at DESC, id DESC)`, `(action text_pattern_ops, occurred_at DESC, id DESC)`. Broad prefixes without a time range sort all matches.

## 10. API summary

| Method/path | Permission |
| --- | --- |
| `POST /auth/login`, `POST /auth/emergency-login`, `GET /auth/kerberos`, `GET /auth/methods` | none (CSRF same-origin guard applies to POST) |
| `GET /permissions` | `platform.roles.view` |
| `GET /roles`, `GET /roles/{id}` | `platform.roles.view` |
| `POST /roles`, `PATCH /roles/{id}`, `PUT /roles/{id}/permissions`, `DELETE /roles/{id}` | `platform.roles.manage` |
| `GET /role-assignments?roleId=&subjectType=&subjectId=&includeRevoked=` | `platform.roles.view` |
| `POST /role-assignments`, `POST /role-assignments/{id}/revoke` | `platform.roles.manage` |
| `GET /audit-events` | `platform.audit.view` |

`GET /auth/methods` returns `{password: bool, kerberos: bool, emergency: bool}` so the UI shows only configured methods. Exact schemas: `api/openapi/openapi.yaml`.

## 11. UI (frontend)

No new runtime dependencies (no router/query/component libraries): a small history-API router and fetch helpers in `src/platform`. Screens: login, app shell with user menu/logout, current user (`/me`), admin: roles (list, create, edit name/description/permissions, delete), role assignments (list, assign to user/group via search pickers, revoke; group-assignment warning), directory sync runs (list, detail, request run), audit events (filters, paging, JSON detail). Navigation entries are shown by permission (UI hiding is convenience; the backend enforces). All strings in EN/DE. Keyboard accessible, labelled form controls, errors announced.

## 12. Security and privacy

- No password is logged, stored (except argon2id emergency hashes) or returned; request bodies are never logged.
- Generic login errors, minimum response time, throttling before LDAP, empty-password guard.
- Session fixation: fresh token, previous session revoked on login; session creation serialized with status changes.
- Least privilege: API holds no directory bind secret; keytab only for the API when Kerberos is enabled.
- Privilege escalation: `platform.roles.manage` is high risk and equivalent to admin; audited; last-admin guard.
- Emergency account disabled by default, short sessions, loud logging, CLI-only lifecycle.

## 13. Tests and documentation

Automated: unit + PostgreSQL tests per operation (incl. audit rows), concurrency regressions (parallel login attempts vs. throttle, emergency disable vs. login, delete vs. assign role, last-admin guard), timing behaviour, empty-password guard, group-transitive evaluation, CLI commands, frontend unit tests (router, api client, i18n parity). Manual before each release (not automated yet, recorded in current status): OpenLDAP end-to-end login and sync, MIT KDC end-to-end Kerberos, and a Playwright smoke run through login and all admin screens against a running stack. Docs: this design, security architecture, LDAP/AD operation, configuration/permissions/events references, OpenAPI, state machines (Role Assignment), glossary, current status, deployment examples.

## 14. Delivery

- **PR A:** migrations 000010 (authorization + audit indexes) and 000011 (local credentials + throttle), slices 3b, 5, 6, emergency account, CLI, UI.
- **PR B:** slice 4 Kerberos (ADR-0023) and its UI hook-up.

## Update 2026-10-09: local accounts and role guards (F14 A-A, A-A2, A-B)

Implemented in the backend: a second credential kind (`local`) in `platform.local_credentials` for Users created in Turaco, set up through single-use invitation or reset tokens and signed in at `POST /auth/local-login` (switch `AUTH_LOCAL_LOGIN_ENABLED`, default off; separate throttling keys, account lock, hash slot pool and password policy; the emergency endpoint finds only emergency credentials). The role service enforces the escalation guards (grant and removal ceiling, role-holding rule, no self-assignment, no expiry on the administrator role, no high-risk permission for local accounts, assignment expiry) and `organization.users.manage` operations follow the dominance rule. Details and API: [F14 design](../product/f14-administration-design.md), [ADR-0034](../decisions/ADR-0034-local-accounts-and-external-parties.md), [current status](../product/current-status.md).
