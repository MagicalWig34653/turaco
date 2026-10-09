# F14 Administration — Feature Design

**Status:** Design proposed 2026-10-09; nothing in this document is implemented. [Current status](current-status.md) is authoritative for what exists. Source: the administrator and persona findings of the hospital simulation ([simulation](../development/simulation-hospital.md)). Related: [ADR-0034](../decisions/ADR-0034-local-accounts-and-external-parties.md) (proposed), [ADR-0033](../decisions/ADR-0033-workbench-views-query-engine.md) (query engine, Views), [ADR-0032](../decisions/ADR-0032-module-switches.md) (module registry), [ADR-0028](../decisions/ADR-0028-workforce-presence.md) (presence privacy), [identity and access design](../security/identity-access-design.md) (F1: roles, login, audit), [module boundaries](../architecture/module-boundaries.md), [F13 design](f13-workbench-views-design.md) (format reference), [UI design system](../development/ui-design-system.md).

## Starting point (verified in the repository)

- The sidebar group Administration has only Roles, Role Assignments, Directory Sync, Modules, Audit and the AI and Presence settings. `/admin/users`, `/admin/organization`, `/settings` and `/search` do not exist (admin finding 1). Users, Teams and Locations are readable through `GET /users`, `/teams`, `/locations` (permission `organization.view`) and have no screen.
- Organization writes exist only for Teams (`POST/PATCH /teams`, activate/deactivate, add/remove member; `organization.teams.manage`) and for user status (`user_status.go`, used by directory sync). Locations and Departments have no write operation, and Users have no profile write: the simulation seed wrote them with SQL ([simulation](../development/simulation-hospital.md)). Locations are flat (`name`, `external_key`, `active`); Infrastructure owns Buildings and Rooms below a Location (Site).
- Directory synchronization writes `display_name`, `given_name`, `family_name`, `primary_email`, `employee_number`, the manager link, and `status` for Users it created or deactivated (`status_source` `directory`/`platform`); it never writes department, primary location or Team membership. Freshness is `external_identities.last_seen_at`; the Directory Sync screen exists, but "Request run" fails with a raw reference when `LDAP_URL` is empty (finding 7).
- Local Users exist only as the break-glass emergency account (`turaco-admin emergency create`, `platform.local_credentials`, login only at `/auth/emergency-login` when `AUTH_EMERGENCY_LOGIN_ENABLED=true`, 1 hour sessions). A Turaco-created person without a directory account cannot sign in. The simulation therefore logs every persona in through the emergency endpoint and the UI shows "Notfallkonto" under every name.
- Roles: one built-in immutable `platform-administrator` plus custom roles (`platform.roles`, `role_permissions`, `role_assignments` to a User or a Directory Group, global scope only, no expiry). Operations and the last-administrator guard (revoking the last active administrator assignment of an active User) exist; permissions are evaluated per request. The role editor is a flat checklist of 75 permissions (finding 4). `platform.roles.manage` is documented as equivalent to administrator access; nothing stops a holder from granting a permission they do not hold themselves or from assigning a role to their own account.
- Audit: `GET /audit-events` with exact filters on ids, `platform.audit.view`, no export, no purge. The UI shows raw UUIDs and the unexplained actor `cli` (finding 5).
- Health: `/health/live`, `/health/ready` (database ping) and `/api/v1/meta` (name, version, environment). `organization/public.SyncHealth` returns unhealthy directory providers for the Briefing feed. Integration modes are code-level: `integrations/intune` has `Fake` and `NotConfigured`, `integrations/remoteaccess` and `integrations/softwaremgmt` have fakes, Autotask has a placeholder behind `AUTOTASK_SYNC`; none is shown anywhere (finding 2). There is no worker heartbeat; the API cannot tell whether `turaco-worker` runs.
- Cmd+K (`CommandPalette`, `paletteCommands.ts`) searches navigation entries and commands only (finding 6). F13 already provides catalog-declared searchable fields and trigram indexes per list.
- The module registry (`platform/modules`, `GET /modules/status`, `/admin/modules`) hides navigation of disabled modules; the AI and Presence admin pages still showed errors against an older API (finding 3).

## Decisions

- **A1 One Administration area, one sidebar group, role-gated per page.** Pages: Setup, People (Users, Teams, Departments, Locations, Import), Access (Roles, Role Templates, Assignments, External Parties), System (Integrations, Health, Directory Sync, Modules, AI, Presence), Audit. A page is visible only with its permission; the backend is the authority. The group appears when any page is visible.
- **A2 Organization owns people data and its write operations.** Users, Teams, Departments, Locations and Import Batches are Organization concepts. Other modules read them through `organization/public` contracts; Infrastructure keeps Buildings, Rooms and Racks. No second people directory is created for the admin UI.
- **A3 Writes are explicit domain operations**, not a generic `UpdateUser`: `CreateLocalUser`, `UpdateProfile` (named fields), `SetDepartment`, `SetPrimaryLocation`, `SetManager`, `Deactivate`, `Reactivate`, `MarkDeparted`, `ExpireAccess`, and so on. Every operation takes `expectedVersion`, locks the row, authorizes inside the transaction and audits in the same transaction.
- **A4 Field ownership is explicit and enforced by the backend.** Every User attribute has an owner, `directory` or `platform`. A directory-linked User's directory-owned attributes are read-only (409 `organization.field_directory_owned`); the UI shows source and freshness next to them. Platform-owned attributes are never overwritten by synchronization (existing behaviour, now a documented contract with a test).
- **A5 Lists use the F13 query engine.** Users, Teams, Locations, Role Assignments and Audit Events get Field Catalogs and `POST /{resource}/query`; the admin lists have filter builder, Saved Views and server-side sort for free. No new filter parser.
- **A6 Bulk work is two-step.** Every CSV import and bulk edit creates a stored dry-run preview first; only `apply` with the preview's hash writes. The preview contains personal data and expires after one hour.
- **A7 Role templates are code, roles are data.** Templates ship in the binary as plain data, are copied into ordinary editable roles, record `template_key` and `template_version`, and are never auto-updated. The built-in `platform-administrator` stays the only immutable role.
- **A8 Privilege-escalation guards belong to the role service**, not the UI: a caller grants only permissions they hold, high-risk permissions only a platform administrator may grant, nobody changes their own assignments, the last-admin guard also covers deactivation, and every role change is audited with a permission diff. Separation-of-duties rules are advisory warnings that need acknowledgement, not hard blocks.
- **A9 Effective permissions are explained by the evaluator itself.** `roles.Evaluator` gets an `Explain` method that returns the same set plus the grant path; a test asserts both agree. No second evaluation implementation.
- **A10 Health is a platform read model fed by registered checks.** `platform/health` defines the check contract and the result vocabulary (`ok`, `stale`, `fake`, `not_configured`, `disabled`, `failing`, `unknown`); modules and adapters register checks in `internal/wiring`. It imports no module. Checks read stored facts; outbound probes only happen on an explicit, audited admin action.
- **A11 "Done" in the Setup checklist is derived, never ticked.** Only `skipped` (with reason) and `confirmed` (for review items such as modules) are stored.
- **A12 Audit becomes readable by resolution at read time.** Ids are resolved to labels through registered resolvers (name, reference or tag only, never titles or free text). Labels are not snapshotted into the immutable record, so personal data erasure keeps working.
- **A13 Export and retention are explicit permissions with limits.** `platform.audit.export` (high), 10,000 rows and 92 days per export, formula-neutralized CSV, every export audited. Retention is off by default; when configured, a worker-only function purges and audits the purge.
- **A14 Search is a federation of existing catalogs.** `GET /search` fans out to providers registered by modules, each applying the module's own scope and redaction (V3/V4 of F13). No search engine, no cross-module SQL.
- **A15 External parties are restricted principals** (ADR-0034): an account kind `external` with a sponsor, an expiry date, a closed permission ceiling enforced in the evaluator, an HTTP allow-list, and visibility limited to Tasks and Tickets explicitly shared with them. Local sign-in with invitation and reset tokens is the account mechanism for people outside the directory (ADR-0034).
- **A16 No Teams as role subjects.** Role assignments stay User and Directory Group. Team management (`organization.teams.manage`, held by Team leads) must not become an indirect route to permissions.

## Scope

In: the six areas of the findings (People, Roles, Setup and Health, Audit UX, global search, external parties), the permissions, audit actions, migrations and slices needed for them, and the review risks.

Not in F14: per-site scoping of roles (still global; recorded as the next authorization step), multi-factor authentication and SSO for external parties (ADR-0034 leaves the hook), self-service profile editing by employees, HR master data beyond the fields listed, an organization chart, mail template editing and SMTP settings written through the UI (mail stays deployment configuration; the UI shows status and a test action), service-desk setup beyond a link to Queues (F13 Q-C owns it), a configuration editor for the secrets of any integration (secrets stay deployment secrets, ADR-0014), user-defined fields, calendar or meeting features.

## 1. People

### 1.1 Users

**Ownership:** Organization. **Directory-owned (read-only for directory-linked Users):** display name, given name, family name, primary email, employee number, manager. **Platform-owned (always editable):** department, primary location, account kind, Team membership, role assignments, external-party fields. **Status:** `active`, `inactive`, `departed` (the legacy values `external` and `unknown` stay valid but are not set by F14). `status_source` decides who may reactivate: the directory re-enables only Users it disabled; a User an administrator deactivated stays inactive until an administrator reactivates (existing rule, unchanged).

**List:** `GET /users/fields`, `POST /users/query` (permission `organization.view`; catalog fields below), the legacy `GET /users` is kept. Catalog (field: type, filterable/sortable/searchable, permission):

| Field | Notes |
| --- | --- |
| `display_name`, `primary_email` | text, searchable (trigram) |
| `status`, `account_kind`, `source` | enum; `source` = `directory`, `local`, `emergency` (derived) |
| `department`, `location`, `team` | references; `team` = current membership |
| `manager` | reference, needs `organization.users.view_details` |
| `employee_number`, `last_sign_in_at`, `username`, `directory_last_seen_at`, `access_expires_at` | needs `organization.users.view_details` |
| `role` | reference, needs `platform.roles.view` |
| `has_credentials`, `directory_disabled` | boolean, needs `organization.users.view_details` |

Emergency accounts are listed with the badge "Emergency account" and offer no edit actions (CLI-only lifecycle, [identity design §7](../security/identity-access-design.md)).

**Operations (permission `organization.users.manage`, elevated, unless noted):**

| Operation | Rule |
| --- | --- |
| `POST /users` create local User | display name required; email unique (existing index); `status_source` platform; creates no credential; optional `accountKind` `employee` (default) or `external` (needs `organization.external_parties.manage`) |
| `PATCH /users/{id}/profile` | only named fields: `givenName`, `familyName`, `displayName`, `primaryEmail`, `employeeNumber` for local Users; rejected per field for directory-linked Users |
| `PUT /users/{id}/department`, `/primary-location`, `/manager` | platform-owned (manager only for Users without directory manager); cycle check on manager; target must be active |
| `POST /users/{id}/deactivate` (reason code), `/reactivate`, `/mark-departed` | sets `status_source` platform; revokes the User's sessions in the same transaction (`authentication.RevokeUserSessions`); refused for the last active platform administrator and for emergency accounts; reactivation refused while the directory identity is disabled |
| `POST /users/{id}/send-invitation`, `/reset-password` | local accounts only, see 1.5 |
| `GET /users/{id}` | adds `fields[]`: per attribute `{key, owner, source, observedAt}` so the UI renders badges without guessing |

**Directory conflicts:** if a directory run changes a field the administrator also edited, the platform-owned value wins by construction (they are different fields). A new directory User whose email matches an existing local User is not merged silently; the existing sync conflict list gets the entry `local_user_email_match`, and an explicit operation `POST /users/{local}/link-directory-identity` (needs `organization.users.manage` and `organization.directory.view`, shows both records, audited) links them. Until linked, the directory User keeps the sync's own email handling (existing "desired email" rule).

### 1.2 Teams

Existing write API stays. Added: `description` (500 characters), `PUT /teams/{id}/members/{userId}/role` with `lead|member` using the existing `team_memberships.role`, `GET /teams/{id}` returns `leads[]`. A lead may be 0..n; leads are a marker used by the UI, approvals and Queue defaults, not a permission. `organization.teams.manage` stays the one permission; the simulation role `it-site-lead` holds it. Team names stay unique among active Teams. Team deactivation lists blockers (open Queue default, routing, recurring definitions) from public contracts and requires `confirmImpact=true` when they exist.

### 1.3 Locations, Sites, Buildings (hierarchy and write operations)

**Decision:** Organization Locations become a tree with two kinds, `site` (root) and `area` (any level below, maximum depth 4: site, area, sub-area, sub-sub-area). Buildings, Rooms and Racks remain Infrastructure-owned and are shown as read-only children of their Site in the tree through `infrastructure/public` when the module is on (no duplicated concept). An area may be a floor, a ward, a department zone.

- Fields: `kind`, `parent_location_id`, `code` (unique among active Locations, replaces `external_key` for display; `external_key` remains for imports), `description`, `version`.
- Operations (`organization.locations.manage`, new, normal): `POST /locations`, `PATCH /locations/{id}` (name, code, description), `POST /locations/{id}/move` (new parent; cycle and depth check; a Site has no parent), `POST /locations/{id}/deactivate|activate`. No hard delete. Deactivation hides the Location from pickers; references stay valid; the response lists counts of Users, Assets and Buildings that still point to it (via public contracts) and requires `confirmImpact` when any exist.
- Reads: `GET /locations?tree=true`, catalog fields `name`, `code`, `kind`, `parent`, `active`.
- Per-site scoping of permissions stays a non-goal; a site is a data attribute (filter, Team Queue routing, reports), not a permission boundary.
- Privacy: Locations hold no address or coordinates in F14. A User's primary location is HR-adjacent and appears in lists only with `organization.users.view_details`; presence and work-location entries (F11) are never shown in Administration.

### 1.4 Departments

Organization-owned, tree already modelled (`parent_department_id`). Operations (`organization.departments.manage`, new, normal): create, rename, move (cycle check), deactivate/activate. A department has a `code`; cost centers remain a separate planned concept and are not part of F14. Catalog: `name`, `code`, `parent`, `active`, `member_count` (capped count). Deactivation lists Users still assigned and requires `confirmImpact`.

### 1.5 Local accounts, invitation and password reset

Required because "create a user" is useless if the person cannot sign in (see ADR-0034 for the authentication decision).

- Local sign-in is enabled by `AUTH_LOCAL_LOGIN_ENABLED` (default `false`). It is a separate endpoint (`POST /auth/local-login`) from the emergency login, with its own throttling and audit; `GET /auth/methods` gains `local`.
- A local User gets credentials only by an **invitation or reset token**: 256-bit random, only the SHA-256 hash stored, single use, expires after 24 hours (invitation: 7 days), bound to the User. The administrator never sees or sets a password. Delivery: email through the existing SMTP channel when configured; otherwise the one-time link is shown once to the administrator for out-of-band hand-over ("copy link"), with the risk stated in the dialog.
- Password policy: minimum 12 characters, argon2id (same parameters as the emergency account), no composition rules, checked against a bundled list of the 10,000 most common passwords; setting a password revokes all sessions of the User.
- Directory-linked Users cannot get a local password (their sign-in is the directory); the endpoint answers 409 `organization.directory_user`.
- Self-service "forgot password" is not built in A; the administrator triggers resets (open decision 4).

### 1.6 CSV import and bulk edit (dry-run first)

**Ownership:** Organization (`organization.import_batches`, `import_rows`). **Kinds:** `users`, `team_members`, `locations`, `departments`.

1. `POST /import-batches` (multipart CSV or JSON rows; permission per kind: `organization.users.manage`, `organization.teams.manage`, `organization.locations.manage`, `organization.departments.manage`; plus `organization.import` (elevated) for CSV upload) with `kind`, `matchKey` (users: `primary_email` or `employee_number`; others: `code`) and `mode` (`create_only`, `update_only`, `upsert`). Limits: 2 MB, 5,000 rows, UTF-8 (BOM tolerated), comma or semicolon, header row, 20 known columns; unknown columns are reported, never ignored silently.
2. The server parses, validates every row with the same domain rules as the single operations (field ownership, uniqueness, references by code/email, cycle checks) and stores the **preview**: per row `action` (`create`, `update`, `unchanged`, `reject`), field-level diff, errors and warnings. Nothing else is written. Response: counts, first page of rows, `previewHash`.
3. `GET /import-batches/{id}/rows?action=reject` pages through rows (a stored result, not a query engine resource).
4. `POST /import-batches/{id}/apply` with `previewHash` and `expectedRejects=n`. Rows are re-validated against current versions inside one transaction (all-or-nothing, at most 5,000 rows); any drift answers 409 `organization.import_stale` and the preview must be repeated. Rejected rows are never applied; `applyRejected=false` is the only mode.
5. Attributes owned by the directory are rejected for directory-linked Users (row-level error, not silent skip). Roles are never assigned by import.
6. Purge: previews expire after 1 hour and are deleted by the job `organization.import.purge`; applied batches keep counts and the file hash only.

**Bulk edit** from the Users list uses the same preview/apply mechanism: `POST /users/bulk-operations` with `operation` in `set_department`, `set_primary_location`, `add_to_team`, `remove_from_team`, `deactivate`, `assign_role` (needs `platform.roles.manage` and all role guards), `userIds[]` (max 500; "all matching the filter" is resolved server-side at preview and the id list is fixed by the hash) and `dryRun`. The dry run lists per User `would_change | unchanged | skipped(reason)`; skip reasons include directory-owned field, emergency account, last administrator, self.

**CSV injection and PII:** imported cells are stored as text and never interpreted; control characters are rejected; leading `= + - @ \t \r` produce a warning (the value is kept, because names such as "-Meier" exist) and are neutralized on every export (section 4.4). Previews, row data and error messages are never written to logs or audit; audit carries counts, kind, file hash and correlation id.

### 1.7 Audit, events, permissions of People

Audit actions: `organization.user.created_local|profile_changed|department_set|location_set|manager_set|deactivated|reactivated|departed|access_expired|directory_linked`, `organization.team.description_changed|lead_set|lead_removed`, `organization.location.created|updated|moved|deactivated|activated`, `organization.department.created|renamed|moved|deactivated|activated`, `organization.import.previewed|applied`, `organization.bulk.previewed|applied`, `auth.local_credential.invited|reset_requested|password_set|invitation_expired`, `auth.local_login.succeeded|failed`. Existing `organization.user.created_local` and `organization.user.status_changed` are kept and mapped. Payloads carry ids, changed property names and before/after only for non-personal enumerations (status, kind, department id); names and email addresses appear in audit only where the existing actions already store them. Events: `UserDeactivated` (sessions revoked, assignments on open Tasks to be reviewed by Tasks), `UserDeparted`, `LocationChanged` (Infrastructure, Presence and Services consumers re-read names), `AccessExpiring`.

## 2. Roles

### 2.1 Role templates

Shipped in `backend/internal/platform/authorization/roles/templates.go` as plain data (key, version, name and description i18n keys, permission list, intended audience, optional required modules). Not stored in a table, so a new release can add templates without a migration. `GET /role-templates` (`platform.roles.view`) lists them with the permission diff against existing roles. `POST /roles` accepts `templateKey` to prefill; the result is a normal role with `template_key` and `template_version` for drift display ("The template changed in 1.5: 2 new permissions. Review diff") and never updates itself.

| Template key | For | Principal contents (final lists are fixed in A-B and security-reviewed) |
| --- | --- | --- |
| `first-level-support` | triage | `tickets.manage`, `knowledge.view`, `assets.view`, `endpoints.view`, `requests.view`, `tasks.work`, `remote_access.view`, `remote_access.start_attended` |
| `it-specialist` | WLAN, ORBIS, server | first level plus `assets.manage`, `knowledge.manage`, `problems.manage`, `tasks.manage`, `changes.view`, `infrastructure.view`, `security.view` |
| `team-lead` | site or team lead | specialist plus `briefing.manage`, `majorincidents.manage`, `requests.manage`, `organization.teams.manage`, `presence.view_availability`, `changes.manage` (not `changes.approve`) |
| `security-analyst` | security | `security.view`, `security.manage`, `tickets.view`, `endpoints.view`, `organization.directory.view`, `platform.audit.view` (not `security.accept_risk`; listed as optional add-on shown with a warning) |
| `infrastructure-engineer` | infrastructure | `infrastructure.view|manage`, `services.view|manage`, `assets.manage`, `changes.view|manage|execute`, `tickets.view` |
| `vendor-restricted` | external vendor | `tasks.work` only; assignable to external accounts only (ceiling) |
| `employee-plus` | everyone with extras | `tasks.work`, `knowledge.view`, `briefing.view`; documents what the implicit baseline already covers |
| `read-only-auditor` | audit, compliance | all `*.view` permissions of enabled modules except `organization.users.view_details`; `platform.audit.view`, no `export` |

Rules: no template contains a `high`-risk permission; `platform.roles.manage`, `platform.admin`, `platform.audit.export` and `modules.manage` are never in a template. A registry test fails when a template names an unknown permission, and the existing simulation seed roles are replaced by these templates (the seed keeps its role keys through `templateKey`).

### 2.2 Permission picker

`GET /permissions` gains `module` (from the module registry catalog by permission prefix), `group` (view, manage, execute, approve, admin), `needs[]` (required companion permissions such as `assets.view` for `remote_access.start_attended`; stated in registry data, not parsed from prose) and `introducedIn`. The UI picker: search over name and description, collapsible module groups with selected counts, risk filter, "only selected", per-group select view/manage, "Copy from role", "Apply template", and a **save diff** dialog (added and removed permissions with risk, SoD warnings, `needs` that are missing). Disabled modules' permissions are shown dimmed with "module off", not hidden.

### 2.3 Effective permissions

- `GET /users/{id}/effective-permissions` (permissions `platform.roles.view` and `organization.view`): roles in force with their source (`direct` with assignment id and expiry, `directory_group` with the group path and the observed-since time, `built_in_admin`), and per permission `grantedBy[]`, risk and module. Also the User's SoD warnings and the list of ceilings applied (external accounts).
- `GET /access/holders?permission=` (same permissions plus `organization.users.view_details` for names beyond ids): who holds a permission, direct or via groups, paged. Answers "who can manage roles?".
- `GET /roles/{id}/members` (effective members including group expansion, capped at 500 with a count).
- Implementation: `roles.Evaluator.Explain(ctx, userID)` shares the query with `Evaluate`; a test compares both for randomized fixtures. Both ignore expired assignments (2.5).

### 2.4 Separation of duties

Rules live in `roles/sod.go` as data: `key`, `permissions[]` (a set that should not be held together), severity (`warn`), i18n message. Initial rules: `changes.manage` with `changes.approve`; `software.package` with `software.approve`; `deployments.manage|high_impact` with `deployments.approve`; `security.manage` with `security.accept_risk`; `procurement.manage` with `inventory.manage`; `platform.roles.manage` with any `*.manage` of operations (administrators should not also work tickets daily: warn once per user); `platform.audit.view` with `platform.roles.manage` (custody of the trail). Evaluated in three places: role save (within the role), assignment (combined with the target's existing roles and group-derived roles), and the effective viewer. A warning does not block; the caller sends `acknowledgedRules: [key...]` plus a reason, audited (`authorization.role.sod_acknowledged`). Existing runtime checks (never approve what you registered) remain the enforcement; this is hygiene.

### 2.5 Escalation guards, expiry and audit

- **Grant ceiling.** `SetRolePermissions`, `CreateRole(permissions)` and `AssignRole` check that the actor holds every permission being added (or every permission of the assigned role), unless the actor is a platform administrator. Violations answer 403 `access.grant_exceeds_holder`.
- **High-risk permissions** (`risk: high`) can be added to a role or assigned only by a platform administrator.
- **No self-assignment.** `AssignRole` with the actor's own User id, or with a Directory Group the actor currently belongs to, answers 409 `access.self_assignment`; the CLI is exempt (existing bootstrap path). Role edits that add permissions to a role the actor holds answer the same.
- **Last administrator.** The guard extends to deactivation, departure and expiry of the last active direct platform administrator, and to removing the User from the only administrator-mapped group when that can be detected; the health check "At least two active platform administrators" warns (section 3).
- **Assignment expiry.** `role_assignments.expires_at` (nullable, maximum 366 days for roles holding high-risk permissions). The evaluator ignores expired rows immediately; the job `access.expire_assignments` revokes them with actor `system:access-expiry` and audit so history shows the end. Required for roles assigned to external accounts.
- **Audit.** `authorization.role.permissions_changed` carries `added[]`, `removed[]` (permission names, with risk class counts), the `templateKey`, and the actor; assignments carry subject type/id, role id, expiry and acknowledged rules. A role change notifies the other platform administrators (notification category `access`, high-risk changes cannot be switched off), event `RoleChanged`.
- **Role history:** the role detail page shows the audit trail of the role (query by target) in the same component as the Audit screen.

## 3. Setup checklist and system health

### 3.1 Platform health contract

`backend/internal/platform/health` (no module import): `Check{Key, Category, Run(ctx) Result}`, `Result{Status, Mode, ObservedAt, LastSuccessAt, LastAttemptAt, ErrorCode, Counts, NextStep}`. Status vocabulary and meaning:

| Status | Meaning | Label (UI) |
| --- | --- | --- |
| `ok` | configured, real, recent success | OK |
| `stale` | configured, last success older than 3 times the interval | STALE |
| `fake` | a fake or in-memory adapter is wired (development or demo) | FAKE |
| `not_configured` | required configuration missing | NOT CONFIGURED |
| `disabled` | switched off on purpose (module switch, `*_SYNC=false`) | DISABLED |
| `failing` | last attempt failed (error code, never raw text without permission) | FAILING |
| `unknown` | no observation yet | UNKNOWN |

`NextStep` is `{kind: route|config|docs, route?, configKeys[], docsPath}` and always renders a link or the exact configuration key names (never values). Provider ports gain an optional `Mode() Mode` (`real|fake|not_configured`) implemented by every Fake and NotConfigured adapter (Intune, Autotask, Software Management, Remote Access, AI); a check reports `fake` whenever the adapter says so, even when it "succeeds". Production environments (`APP_ENV=production`) with a `fake` adapter raise the setup item to attention.

Checks are registered in `internal/wiring/health.go` and read stored facts only (database, job tables, sync runs, notification deliveries, module state); results are cached for 5 seconds. Outbound probes (LDAP bind, SMTP handshake, Graph call) are a separate explicit action `POST /admin/health/checks/{key}/probe` (permission `platform.health.probe`, elevated, 1 per minute per check, timeout 5 seconds, audited, no secret in the response); they are optional in A-C (open decision 6).

Initial checks: `database` (ping, version), `migrations` (applied version vs embedded latest from `platform.schema_migrations`; mismatch is `failing` with the message "database schema behind application"), `worker` (heartbeat), `jobs` (oldest pending age, failed jobs in 24 h, scheduled jobs without success in 3 times their interval), `outbox` (due events, oldest age, failed), `object_storage`, `smtp` (configured, `EMAIL_BASE_URL` set, last delivery outcome), `directory` (configured, last run outcome and age, withheld sweeps, conflicts), `kerberos`, `emergency_login` (enabled is a warning), `intune`/`endpoints` (mode, `INTUNE_SYNC`, last endpoint sync), `software_provider`, `remote_access_providers`, `advisory_feeds`, `autotask` (placeholder: always `not_configured` until a REST client exists), `ai`, `presence`, `modules` (switched-off modules with unmet preconditions).

### 3.2 Worker heartbeat and job health

Migration adds `platform.worker_heartbeats(instance_id, version, started_at, last_seen_at)`; the worker upserts every 15 seconds, rows older than a day are deleted by the worker itself. Worker health is `ok` when a heartbeat is younger than 60 seconds, `failing` otherwise ("no worker is running: background jobs, email and sync are stopped"). Job health reads `platform.jobs` aggregates by `job_type` (pending, processing, failed, oldest pending age, last success), no payloads.

### 3.3 Setup checklist

`GET /admin/setup` (permission `platform.health.view`) returns ordered items from checks plus count probes that modules register through the same contract (counts only, via public contracts):

| # | Item | Done when (derived) | Link |
| --- | --- | --- | --- |
| 1 | Administrators | at least 2 active direct platform administrators and the emergency account is disabled or documented | Access |
| 2 | Directory | directory configured and last run succeeded, or `skipped` (local-only installation) | Directory Sync |
| 3 | Mail | SMTP configured, base URL set, at least one delivered email | Health |
| 4 | Teams | at least one active Team with a member | Teams |
| 5 | Locations | at least one Site | Locations |
| 6 | Queues | at least one active Ticket Queue with a default Team | Queues (F13) |
| 7 | Roles | at least one non-built-in role assigned to someone | Roles/Templates |
| 8 | Catalog | at least one active Catalog Item (when the module is on) | Catalog |
| 9 | Integrations | each integration is `ok`, `disabled` or `skipped`; any `fake` in production is attention | Integrations |
| 10 | Modules | `confirmed` once by an administrator (defaults are meaningful) | Modules |

`PUT /admin/setup/items/{key}` with `{state: skipped|confirmed|cleared, reason}` is the only write (stored in `platform.setup_items`, audited `platform.setup.item_skipped|confirmed|cleared`). The Overview of a user with `platform.health.view` shows "Setup n/10" with a link instead of "You're all caught up" while items are open. Disabled modules remove their items.

### 3.4 Integrations page and system information

`GET /admin/integrations` lists integrations as cards: name, mode label (FAKE / NOT CONFIGURED / OK / STALE / DISABLED / FAILING), what it does for the product, last success and last attempt with freshness, last error code, the configuration key names to set (never values), "Open guide" (docs path) and a link to the related screen (for example Devices > "Sync now" is disabled with the reason when the provider is not configured, fixing the "Sync now next to No devices found" finding). `GET /admin/system` returns application version, build commit, environment, applied and latest migration, process start, database version and the module summary. The Devices, Security and Directory pages read the same check to show their own banner.

### 3.5 Version mismatch and broken settings pages

Finding 3: when a settings route answers 404 `platform.not_found` or `platform.module_disabled`, the UI shows "This server does not provide this feature (version mismatch or module off)" with the server version from `/meta`; navigation entries are hidden when `/modules/status` or the new `GET /meta` field `features[]` does not list them. Empty-state headings never say "None".

## 4. Audit log UX

### 4.1 Reading

- `GET /audit-events/fields`, `POST /audit-events/query` (permission `platform.audit.view`) over the query engine: fields `occurred_at` (always-visible date range, default last 7 days, quick ranges), `actor` (User picker), `actor_kind` (`user`, `system`), `system_actor`, `action` (prefix and `in`), `module` (derived from the action prefix), `target_type`, `target_id`, `correlation_id`, `via`. The legacy `GET /audit-events` stays. Saved Views work on the resource `audit` (for example "Role changes last 30 days").
- **Resolution:** `platform/audit` defines `Resolver` (`TargetType`, `Resolve(ctx, ids) map[id]Label{Text, Route, Gone}`); modules register resolvers in the composition root for users, teams, roles, locations, departments, tickets (reference only), assets (tag), devices (name), modules (key), queues. At most 200 ids per page, batched per type, missing entities render "Removed (id suffix)". Actors: users through the Organization contract, system actors through i18n (`cli` = "Operator command line (turaco-admin)", `directory-sync`, `login`, `access-expiry`, `worker`). `metadata.osUser` is shown only on the detail drawer. Resolution runs after the authorization of the page and returns labels, never titles, comments or addresses.
- **Detail drawer:** formatted before/after diff (role permissions as added/removed chips with risk), correlation id with "show everything from this request", copy link.
- Action labels come from `audit.action.*` i18n keys with the raw key as fallback and grouping by module.

### 4.2 Export

`GET /audit-events/export.csv?{same filter}` (permission `platform.audit.export`, high; also requires `platform.audit.view`): date range required, at most 92 days and 10,000 rows (more answers 413 `audit.export_too_large` with the count and a hint to narrow), UTF-8 with BOM, columns `time, actor, actor_id, action, target_type, target, target_id, correlation_id, via`. Before/after/metadata only with `includeDetails=true` (open decision 8). Cells starting with `= + - @ \t \r` are prefixed with `'`; quoting per RFC 4180. One audit event `platform.audit.exported` (filter hash, range, row count, actor) is written before streaming; the rate limit is 5 exports per hour per user. Large or recurring exports are not built (open decision 7).

### 4.3 Retention

Audit is append-only by convention and has no purge today. Configuration `AUDIT_RETENTION_DAYS` (default `0` = keep, minimum 365 when set). A worker job `platform.audit.purge` calls a database function `platform.purge_audit_before(cutoff)` that only the worker role may execute; it deletes in batches of 5,000, never within the last 365 days, and writes one `platform.audit.purged` event with cutoff, count and a hash of the deleted id range. The Audit page header states the policy and the oldest event date. Legal retention is the operator's decision; the design provides the mechanism and a safe default.

### 4.4 Privacy

Audit payloads keep ids; names are resolved at read time; no free text from tickets or comments is resolved; export is the only bulk path and is itself audited; `platform.audit.view` is elevated, `platform.audit.export` high.

## 5. Global search and Cmd+K

- **Rename and scope:** the palette placeholder becomes "Search pages, people, tickets and devices". Two result groups: **Pages and commands** (client side, with keyword synonyms from i18n: "user" finds Users, "mail" finds Health > Mail, "integration" finds Integrations; commands such as "Create user", "Create Team", "Invite external user", permission-gated) and **Records** (server side). "No results" is explicit text.
- **Backend:** `GET /search?q=&types=&limit=` (any signed-in User); `platform/search` holds the provider contract `Provider{Type, Visible(principal), Search(ctx, principal, q, limit)}`; providers are registered in `internal/wiring/search.go` and call the owning module's application service using the same catalog search fields, scope and redaction as the F13 list queries, so the result set equals what the list would show. Minimum 2 characters, 5 results per type, 25 total, per-provider timeout 300 ms (a slow provider returns `unavailable: [type]`, not an error), per-principal rate limit, disabled modules skipped.
- **Providers:** `users` (needs `organization.view`; shows name, email, department only; no HR fields), `teams`, `locations`, `roles` (`platform.roles.view`), `tickets` (reference, alias and title within the caller's scope; a query matching a reference or alias jumps directly), `assets`/`devices`, `knowledge` (published, caller's audience). Further providers follow F13 Q-E catalogs.
- **Audit:** searches are not audited; queries never go into logs; results are not cached across principals.

## 6. Vendors and external parties

Authentication and authorization mechanics are decided in [ADR-0034](../decisions/ADR-0034-local-accounts-and-external-parties.md); the product model:

- **External Party** (Organization): company or person group, `kind` (`vendor`, `contractor`, `partner`), name, `sponsor_user_id` (an internal responsible person, required), `active`, notes (500 characters, no personal data). External Users reference it. A Team may hold external members; the vendor Team receives Tasks exactly like an internal Team.
- **External account:** `account_kind = external`, `external_party_id`, `access_expires_at` (required, maximum 365 days from creation or extension). Creation needs `organization.external_parties.manage` (elevated). The sponsor receives notifications 14 and 3 days before expiry; an explicit `POST /users/{id}/extend-access` (reason, sponsor or administrator, new date) is the only way to extend; extension is audited.
- **Enforcement at expiry:** `ExpireAccess` (job `organization.expire_external_access`, every 5 minutes) sets the User `inactive` with actor `system:access-expiry`, revokes sessions and role assignments' effect; lazily, `UserAccess` treats a User past `access_expires_at` as inactive immediately so the job is only the audit and cleanup path.
- **Ceiling:** a closed list `ExternalCeiling` (initially `tasks.work`, `knowledge.view` limited to external-audience articles, `briefing.view` off, nothing else). The evaluator intersects an external principal's permissions with the ceiling; assigning a role with permissions outside the ceiling to an external account answers 409 `access.external_ceiling` (the role editor marks such roles "not for external accounts").
- **HTTP allow-list:** for an external principal one middleware allows only registered route patterns (`/me`, `/auth/*`, `/tasks` operations, `/my-work`, shared ticket read and comment, `/notifications`, `/modules/status`, `/meta`); everything else answers 404 `access.not_available`. Module registry entries declare `ExternalPaths`; a test fails when a route family does not state its external behaviour. This also removes the console 403 from the Briefing feed seen in the simulation, because the UI no longer calls it for external accounts.
- **Visibility:** Tasks assigned to the account or its Team (existing `tasks.work` rule). Tickets only when explicitly shared: Service Desk gets `ticket_participants` (ticket, user, added by, added at, optional expiry, level `comment`/`read`) with operations `AddParticipant`/`RemoveParticipant` (needs `work` in the Ticket's Queue); external participants see the Ticket and its **public** comments only, never internal comments, Queue names, routing, assignee emails or other tickets. People pickers, directory, devices, assets, Problems and the whole Administration area are not reachable. Ticket-by-id answers 404 for everything not shared.
- **Task completion note:** the vendor task finding (result note on completion) is a Tasks follow-up and not part of F14; the external model does not depend on it.
- **Offboarding:** `MarkDeparted` and `ExpireAccess` raise the event `UserDeparted`/`AccessExpired`; Tasks lists open assignments of the account for reassignment (workflow in the [offboarding workflow](../workflows/offboarding.md)).
- Privacy: the vendor sees names of Task participants only as the module already shows; the party record carries no address or contract data.

## Data model sketch (migrations reserved in order)

`000063` and `000064` exist and `000062` belongs to F13 Q-D; F14 reserves **000065 to 000070**. Numbers are claimed in this order at merge; re-check the directory before writing a migration.

- **000065 `organization_people`:** `organization.locations` + `kind text check in ('site','area') default 'site'`, `parent_location_id uuid references locations`, `code text`, `description text`, `version integer default 1`, check (`kind='site'` implies `parent_location_id IS NULL`), unique `(lower(code)) WHERE active AND code IS NOT NULL`, depth enforced by the operation and a deferred trigger; `organization.departments` + `code`, `version`; `organization.teams` + `description`, `version`; `team_memberships.role` check in (`lead`,`member`) (existing nulls become `member`); `organization.users` + `version`, `account_kind text not null default 'employee' check in ('employee','external')`, `external_party_id`, `access_expires_at`; `organization.import_batches(id, kind, status, created_by, file_sha256, row_count, summary jsonb, preview_hash, expires_at, applied_at)` and `import_rows(batch_id, row_no, action, target_id, diff jsonb, errors jsonb)` (primary key `(batch_id, row_no)`); trigram indexes on `users(display_name, primary_email)` through the F13 catalog rule; indexes for the catalog sorts.
- **000066 `local_accounts`:** `platform.local_credentials` + `kind text check in ('emergency','local') default 'emergency'`, `enabled` semantics unchanged; `platform.credential_tokens(id, user_id, purpose check in ('invitation','reset'), token_hash unique, expires_at, used_at, created_by, created_at)`; throttle keys `local:` allowed by the existing check pattern (extended).
- **000067 `access_roles`:** `platform.roles` + `template_key`, `template_version`; `platform.role_assignments` + `expires_at` and an index for the expiry job; `platform.setup_items` is in 000068.
- **000068 `health_setup`:** `platform.worker_heartbeats`, `platform.setup_items(key, state, reason, updated_by, updated_at)`.
- **000069 `audit_search_export`:** `platform.purge_audit_before(timestamptz)` (security definer, worker role), indexes for `module` prefix and `via` filters if the EXPLAIN tests need them, trigram on `action` is not added.
- **000070 `external_parties`:** `organization.external_parties(id, kind, name, sponsor_user_id, active, notes, version, timestamps)` and the FK from users; `servicedesk.ticket_participants` (owned by Service Desk).

All forward-only; no released migration is edited. `account_kind`, `access_expires_at` and expiry are read by the authentication path (`UserAccess`, `LockActiveUser`) so they must be in the same release as the enforcement code.

## Permissions

New in `backend/internal/platform/permissions` (generated reference `docs/reference/permissions.md` updates with them):

| Permission | Risk | Meaning |
| --- | --- | --- |
| `organization.users.manage` | elevated | create local Users, edit platform-owned and local profile fields, deactivate, reactivate, mark departed, invitations and resets, link directory identity, bulk operations |
| `organization.users.view_details` | elevated | see manager, employee number, department, primary location, last sign-in, username and credential state in lists, catalogs and exports |
| `organization.locations.manage` | normal | create, rename, move, deactivate Locations |
| `organization.departments.manage` | normal | same for Departments |
| `organization.import` | elevated | upload CSV for preview and apply it (also needs the per-kind manage permission) |
| `organization.users.export` | high | export Users as CSV (not built in A; reserved so that no route is added without it) |
| `organization.external_parties.manage` | elevated | create External Parties and external accounts, extend access |
| `platform.health.view` | elevated | Setup checklist, Integrations, Health and System pages including error codes and configuration key names |
| `platform.health.probe` | elevated | run an explicit connectivity probe (A-C optional) |
| `platform.audit.export` | high | CSV export of audit events |
| `platform.roles.view` / `.manage` | unchanged | `roles.manage` keeps its meaning; the guards of 2.5 apply to every holder who is not a platform administrator |

Existing `organization.teams.manage`, `organization.directory.view|sync`, `platform.audit.view`, `modules.manage` are unchanged. The simulation role `it-site-lead` keeps `organization.teams.manage` only; no operational template contains `organization.users.manage`.

## Audit (complete list of new actions)

People: section 1.7. Access: `authorization.role.created|updated|permissions_changed|deleted|assigned|assignment_revoked|assignment_expired|sod_acknowledged|created_from_template`. Setup and health: `platform.setup.item_skipped|confirmed|cleared`, `platform.health.probe_run`. Audit: `platform.audit.exported|purged`. External: `organization.external_party.created|updated|deactivated`, `organization.user.access_extended|access_expiring_notified`, `servicedesk.ticket.participant_added|removed`. Payloads: ids, version, changed property names, counts, enumerations; never filter values, CSV rows, tokens or email addresses.

## HTTP API sketch (summary)

All under `/api/v1`; error codes `organization.field_directory_owned`, `organization.directory_user`, `organization.import_stale`, `organization.version_conflict`, `organization.last_administrator`, `access.grant_exceeds_holder`, `access.self_assignment`, `access.external_ceiling`, `access.not_available`, `audit.export_too_large`, `health.probe_rate_limited`.

- People: `GET /users/fields`, `POST /users/query`, `POST /users`, `PATCH /users/{id}/profile`, `PUT /users/{id}/department|primary-location|manager`, `POST /users/{id}/deactivate|reactivate|mark-departed|send-invitation|reset-password|extend-access|link-directory-identity`, `POST /users/bulk-operations`, `GET /teams/fields`, `POST /teams/query`, `PUT /teams/{id}/members/{userId}/role`, `GET/POST /locations`, `PATCH /locations/{id}`, `POST /locations/{id}/move|deactivate|activate`, `GET/POST /departments`, `PATCH /departments/{id}`, `POST /departments/{id}/move|deactivate|activate`, `POST /import-batches`, `GET /import-batches/{id}[/rows]`, `POST /import-batches/{id}/apply`, `GET/POST /external-parties`, `PATCH /external-parties/{id}`.
- Access: `GET /role-templates`, `POST /roles` (`templateKey`), `GET /users/{id}/effective-permissions`, `GET /access/holders`, `GET /roles/{id}/members`, `GET /permissions` (extended).
- Auth: `POST /auth/local-login`, `POST /auth/credential-tokens/redeem` (token + new password), `GET /auth/methods` (+ `local`).
- Health: `GET /admin/setup`, `PUT /admin/setup/items/{key}`, `GET /admin/integrations`, `GET /admin/health`, `GET /admin/system`, `POST /admin/health/checks/{key}/probe`.
- Audit and search: `GET /audit-events/fields`, `POST /audit-events/query`, `GET /audit-events/export.csv`, `GET /search`.
- Module registry: the new route families are core (`organization`, `access`, `audit`, `platform`); `search` is core; `ticket_participants` belongs to `servicedesk`.
- OpenAPI additions in `api/openapi/openapi.yaml`; `make docs-check` regenerates the permission and event references.

## UI sketch

Functional first (visual design postponed), semantic tokens, i18n for every string, German and English.

- **Sidebar:** Administration group with the sections above; Setup shows a progress badge while incomplete.
- **Setup (`/admin/setup`):** ten items with status icon, one-line evidence ("2 of 2 administrators", "Last sync 3 h ago"), primary action link, "Skip with reason". Brand-new installs open here from the Overview.
- **Users (`/admin/users`):** list with the F13 filter builder, view bar and Saved Views; columns chooser; row menu (open, deactivate, reset password, assign role); bulk bar with the operations of 1.6; "New user" and "Import CSV" buttons. **User detail:** profile card where every attribute shows an owner chip ("Directory ad · seen 2 h ago" or "Turaco"); tabs Teams, Roles (with the effective-permissions panel and warnings), Sign-in (method, credentials state, send invitation/reset, sessions), Activity (audit of the User). Directory-owned inputs are disabled with an explanation.
- **Teams, Locations (tree with add/move/drag alternative via "Move to…" dialog and keyboard), Departments:** list/detail pages with member lists and lead toggle.
- **Import:** upload, mapping preview, dry-run result table (create/update/unchanged/reject counts, row filter, error export without personal data), "Apply" disabled until the user confirmed the counts.
- **Roles:** list with template badge and drift indicator; create dialog "From template / Copy role / Empty"; editor with the grouped searchable picker, risk filters, save-diff and SoD warnings; **Effective permissions** page (choose a User, see roles and the grant path per permission, warnings); **Holders** search by permission; assignments dialog with expiry date and group warning.
- **Integrations and Health:** cards and a table with the labelled badges, next-step links, last success freshness; Health tabs Jobs, Worker, Outbox, System (version, migration).
- **Audit:** always-visible date range and actor/action/target pickers, results as names with raw id on hover, detail drawer with diff, Export button (disabled with reason without permission), policy line for retention.
- **Cmd+K:** two groups as in section 5; keyboard navigation unchanged.

## Slices

1. **A-A Backend people and locations:** migration 000065, Organization operations (users, teams description/lead, locations tree, departments), field ownership contract and `fields[]`, catalogs and query endpoints, import batches and bulk operations with dry run and purge job, `organization.view_details`, audit actions, events, search providers for users/teams/locations, tests (ownership, version conflicts, directory conflicts, last-admin, cycles, import stale, CSV edge cases, authorization matrix, audit completeness). Sub-step **A-A2** (after ADR-0034 is accepted): migration 000066, local credentials, invitation/reset tokens, `/auth/local-login`, throttling, session revocation.
2. **A-B Backend roles and effective permissions:** migration 000067, templates, `needs`/`module` registry fields, `Evaluator.Explain`, holders and members reads, SoD rules, escalation guards, assignment expiry and job, audit diffs and notifications, tests (escalation matrix, self-assignment, expiry, explain equals evaluate, template registry test).
3. **A-C Backend health, checklist, audit and search:** C1 `platform/health`, worker heartbeat, checks and adapter `Mode()`, setup items (migration 000068), `/admin/*` endpoints; C2 audit catalog, resolvers, export, retention function and job (migration 000069); C3 `platform/search` and remaining providers. Tests: status vocabulary mapping, fake in production, stale detection, no secret in any response, export limits and CSV neutralization, resolver authorization, retention floor.
4. **A-D UI people:** Users, User detail, Teams, Locations, Departments, Import, bulk bar, invitation/reset dialogs, directory badges, i18n, tests.
5. **A-E UI roles:** templates, picker, diff and SoD dialogs, effective permissions, holders, assignment expiry.
6. **A-F UI health, audit, search:** Setup, Integrations, Health, System, Audit screen with names and export, Cmd+K records, version-mismatch handling.
7. **A-G External parties (follows A-B):** migration 000070, party and account model, ceiling in the evaluator, HTTP allow-list and route declarations, expiry job and notifications, ticket participants in Service Desk, UI for External Parties. Needs Opus design review and a security review before merge.

Order: A-A (with A2) and A-B in parallel (disjoint files), then A-C; A-D after A-A, A-E after A-B, A-F after A-C; A-G after A-B. Maximum three concurrent implementers.

## Reused concepts

Organization Users, Teams, Locations, Departments, Directory Groups and `WorkDirectory`; platform roles, assignments and evaluator; audit and outbox; notifications; module registry (`ExternalPaths`, route declarations); query engine, Saved Views and `DataTable`/`FilterBar`; `safetext`; `ReasonDialog`; `authentication.RevokeUserSessions` and the throttle; argon2id parameters of the emergency account; job runner and schedules; SMTP channel; F13 trigram index rule; Infrastructure public reads for Buildings.

## New concepts

Import Batch (preview and apply), Field Owner (directory/platform) on User attributes, Location kind and tree, Role Template, Separation-of-Duties rule, Effective Permissions (Explain), Assignment Expiry, Health Check and Result vocabulary, Setup Item, Worker Heartbeat, Audit Resolver, Search Provider, Local Account and Credential Token (ADR-0034), External Party, External Account with ceiling and expiry, Ticket Participant. Glossary entries are added with each slice.

## Open decisions (proposed defaults)

1. Local sign-in for non-directory Users (ADR-0034). Default: yes, off unless `AUTH_LOCAL_LOGIN_ENABLED=true`, invitation/reset tokens only.
2. May a platform administrator assign themselves a role? Default: no, a second administrator or the CLI does it.
3. SoD rules: block or warn? Default: warn with acknowledgement and reason.
4. Self-service forgot-password. Default: not in A; administrator-triggered only.
5. Manager for directory-linked Users when the directory has no manager. Default: read-only (directory-owned); a platform override is a later decision.
6. Outbound connectivity probes in Health. Default: A-C ships stored-facts checks only; probes are a follow-up with their own review.
7. Asynchronous or scheduled audit exports. Default: no; synchronous with the limits above.
8. Before/after details in CSV export. Default: excluded unless `includeDetails=true`.
9. Audit retention default. Default: keep forever; minimum 365 days when configured.
10. Do externals get MFA? Default: not in F14; the external allow-list and expiry are the compensating controls; TOTP is a follow-up ADR.
11. Narrow `organization.view` itself (manager, department, location, employee number are in today's `userDTO`)? Default: not in F14 to avoid a silent permission change; the new catalog and exports use `view_details`; revisit with a release note.
12. Location kinds beyond `site` and `area`. Default: two kinds; Buildings stay Infrastructure.
13. Role assignment to Teams. Default: no (A16).
14. Max length of external access. Default: 365 days.
15. German UI wording of Users/People vs the findings: Default: "Personen" for the section, "Benutzer" for accounts; final wording in i18n review.

## Review-risk list

- **Privilege escalation via role editing:** granting what you do not hold, high-risk permissions by non-admin role managers, self-assignment, assignment through a Directory Group the actor controls, editing a role you hold, template drift adding permissions silently (templates never update), bulk `assign_role`, role created from template with extra permissions in the same request, import creating administrators (roles are never importable), invitation tokens granting access to a previously deactivated User.
- **Last-admin lockout:** deactivation, departure, expiry, group removal, sync deactivation of directory-linked administrators, deletion races between two administrators (lock order: Organization user row, then credentials and assignments), bulk deactivation, the health warning for fewer than two administrators.
- **Directory conflicts:** local User with the same email as a directory User, directory deactivating a User an administrator kept active, reactivating a User the directory disabled, `status_source` flips, editing a directory-owned field through bulk or import, manager cycles from mixed sources, sync running while an import applies (row versions and the transaction), `LDAP_PROVIDER_KEY` change.
- **CSV injection and parsing:** formula prefixes in names on export, delimiter and encoding confusion, BOM, quoted newlines, oversized files and rows, duplicate match keys within one file, partial application (all-or-nothing), preview hash replay, stale previews, PII retained in previews beyond one hour.
- **PII exports and displays:** audit export and any future user export (permission, limits, audit, neutralization), HR fields (manager, employee number, location) only with `view_details`, search results leaking fields, resolvers resolving names the viewer may not see, presence data (excluded), audit labels resolved after erasure ("Removed"), logs containing preview rows or tokens.
- **Tokens and passwords:** token entropy, hash-only storage, single use, expiry, timing-safe comparison, invitation link exposure to the administrator, session fixation on redemption, throttling of redeem and local login, enumeration through reset (uniform answers), reuse of emergency hashing parameters.
- **External parties:** ceiling bypass through a new route family (the route declaration test), ticket participant leakage (internal comments, Queue names, aliases of other Queues), people pickers, expiry boundary races (lazy check at every authentication), sponsor leaving, shared accounts, extension without reason, pinned Views and notifications referencing internal data, Views and search for external principals.
- **Health and setup:** secrets or hostnames in responses or logs, probes as SSRF or credential oracle, cache serving stale `ok`, heartbeat spoofing (worker writes with its own database role only), `fake` hidden by a "successful" call, false "done" when counts come from a disabled module, skip abuse.
- **Audit UX:** resolver N+1 and cost, resolver exposing ticket content, query-engine cost on `audit_events` (indexes, 92-day bound), purge function misuse (worker role only, 365-day floor, audited), clock skew in range filters.
- **Concurrency and consistency:** `expectedVersion` on every operation, location moves and cycle checks under concurrent moves (lock order by id), deactivation of a Team with live references, import batches applied twice (status check under lock), bulk operations partially failing (single transaction).
- **Module boundaries:** `platform/health`, `platform/search` and audit resolvers import no module; counts come through public contracts; Organization does not read Service Desk or Infrastructure tables; `make archcheck` rules are added with A-C; the new permissions and audit actions are registered so the generated references stay current.
- **UI vs authorization:** hidden pages are not protection; every endpoint is checked by a table-driven authorization test (no permission, wrong permission, external principal, disabled module).
