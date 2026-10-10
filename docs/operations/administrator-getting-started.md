# Getting started as an administrator

This page walks a new administrator through the first hour with a running Turaco installation: first sign-in, modules, people, roles, Ticket Queues, Views and Boards. It describes only what exists in the repository today. [Current status](../product/current-status.md) is authoritative; steps that are not possible yet are marked **Gap**. Installation, TLS, directory and Kerberos setup are in [Deployment](deployment.md), [LDAP/AD](../integrations/ldap-ad.md) and [Backup and Restore](backup-restore.md). Every environment variable is listed in the generated [configuration reference](../reference/configuration.md); this page names only the ones that change what an administrator sees.

## 1. Before you start

| Setting | Why an administrator cares |
| --- | --- |
| `AUTH_EMERGENCY_LOGIN_ENABLED` (default `false`) | Exposes the break-glass login. Needed for the first sign-in when no directory is connected. Every use is audited and logged at error level. |
| `AUTH_LOCAL_LOGIN_ENABLED` (default `false`) | Switches local accounts on: people without a directory account can be invited and sign in with email and password. Off answers 404 and rejects existing local sessions. Needs `EMAIL_BASE_URL`. |
| `EMAIL_BASE_URL` | Address of the web application (for example `https://turaco.example.org`); invitation and reset links are built from it. |
| `SMTP_HOST` and related `SMTP_*` | Without a mail relay Turaco cannot send invitations, resets or notifications (see section 4 for the one case where a link is shown on screen). The API sends the credential links of local accounts itself, the worker sends notifications, so both need the settings ([Deployment](deployment.md#email-notifications)). |
| `PEOPLE_LOOKUP_ENABLED` (default `true`) | Lets every employee find active colleagues by name when raising a ticket or request for someone else. |
| `PRESENCE_ENABLED`, `AI_ENABLED`, `REMOTE_ACCESS_PROVIDERS` | Startup gates of optional modules; see section 3. |
| `HTTP_TRUSTED_PROXIES` | Needed behind a reverse proxy so login throttling sees real client addresses. |

API and worker must use the same values for the settings that both read (the reference marks them).

## 2. First administrator and first sign-in

A fresh installation has no administrator. The first one is granted on the command line, not in the web UI, from the worker container (it ships `turaco-admin` and uses the worker's `DATABASE_URL`):

1. **With a directory:** configure `LDAP_*` ([LDAP/AD](../integrations/ldap-ad.md)), wait for the first directory sync (Administration > Directory Sync shows the runs), then run `turaco-admin role grant --role platform-administrator --user <username>`. Sign in with the directory password; Kerberos single sign-on is tried automatically when `KERBEROS_*` is configured.
2. **Without a directory:** create the break-glass account with `turaco-admin emergency create --login <name> --display-name <text>`, grant it the `platform-administrator` role with `turaco-admin role grant`, run `turaco-admin emergency enable --login <name>`, set `AUTH_EMERGENCY_LOGIN_ENABLED=true` on the API and sign in with the emergency option of the login screen. Emergency sessions end after one hour at the latest.

**Gap:** local accounts can never hold high-risk permissions, so a local account cannot be a platform administrator ([ADR-0034](../decisions/ADR-0034-local-accounts-and-external-parties.md)). An installation without a directory therefore has no long-lived administrator account; it depends on the emergency account. A setup checklist and a system health screen are designed ([F14](../product/f14-administration-design.md)) but not implemented, so verify the configuration yourself.

Emergency accounts are read-only in the People screens; their lifecycle stays on the command line (`emergency set-password`, `emergency disable`).

## 3. Modules

Open Administration > Modules (`/admin/modules`, permission `modules.manage`). Core modules (platform, access, organization, audit, tasks, approvals, notifications) are always on. Optional modules are on after an upgrade, except Workforce Presence and Turaco AI, which stay off until their startup gate (`PRESENCE_ENABLED`, `AI_ENABLED`) and their own preconditions (recorded data protection impact assessment, an enabled AI Provider) are met; Remote Access also needs `REMOTE_ACCESS_PROVIDERS`. An unmet precondition shows the module as blocked. A module cannot be switched off while another enabled module depends on it, and switching off hides the module's screens and routes but never deletes data ([ADR-0032](../decisions/ADR-0032-module-switches.md)). Every switch needs a reason code and is audited.

Switch off modules the organization does not use before you create roles, so the permission picker and the navigation stay small.

## 4. People

Administration > Users (`/admin/users`, permission `organization.view`; writes need `organization.users.manage`).

- **Directory users** appear after the sync. Attributes owned by the directory (names, email, employee number, manager) are read-only and carry an owner chip; Turaco owns department, primary location and Team membership.
- **Create a person** with "New user". Without `AUTH_LOCAL_LOGIN_ENABLED` the person exists but cannot sign in. With it, the new account is invited: Turaco emails a single-use link (valid 7 days) to the stored address. Only when no mail channel exists and the account was never activated is the link shown once on screen with a copy button; copy it then, it cannot be shown again.
- **Reset a password** with "Reset password" (link valid 24 hours). An administrator never sees or chooses a password. The person sets it on `/set-password`; the policy is at least 12 characters, no common passwords, nothing derived from name or email. Five failed sign-ins lock the account for 15 minutes.
- **Deactivate, reactivate, mark as departed** are explicit operations with a closed reason; they revoke sessions and open tokens. You cannot deactivate yourself, and the last active platform administrator cannot be deactivated. Resets, invitations, email changes and deactivations of someone else need either the platform-administrator role or every effective permission of the target person (dominance rule).
- **Structure:** Teams (members and leads; nobody edits their own membership), Locations (a tree of sites and areas) and Departments (a tree). Archiving an entry that is still referenced asks for explicit confirmation and shows the counts.

**Gaps:** CSV import and bulk edit, linking a directory identity to a local person, cost centers, and External Parties (vendors with restricted accounts, slice A-G) are not implemented. Local accounts are for people outside the directory; external accounts with an expiry and sponsor do not exist yet.

## 5. Roles from templates

Administration > Roles (`/admin/roles`, permission `platform.roles.view`; writes need `platform.roles.manage`).

1. Open the **Templates** tab. The built-in templates are First-level support, IT specialist, Team or site lead, Security analyst, Infrastructure engineer, Vendor (restricted), Employee plus and Remote support (attended), the last one an opt-in high-risk add-on. A template is data; it is **copied** into an ordinary editable role that records the template key and version and is never updated automatically (the editor shows drift to the current template).
2. Review the permission list in the save review: it shows added and removed permissions, their risk, companions a permission needs and separation-of-duties warnings. Adjust the permissions, or clone an existing role instead.
3. Assign the role under Role Assignments (`/admin/role-assignments`) to a person or a Directory Group. An end date is optional, at most 366 days for roles with high-risk permissions, and not allowed on the platform-administrator role.
4. Check the result under a person's effective permissions (it explains each permission by the role and the path that grants it) or under "Who has access" (`/admin/holders`, needs `organization.users.view_details` too).

Guards worth knowing: you can grant and remove only permissions you hold yourself (administrators excepted), high-risk permissions are granted only by an administrator, a role is assigned only by someone who holds it or by an administrator, and nobody assigns a role to themselves. A separation-of-duties rule is a warning that needs an acknowledgement with a reason.

Employees need no role: every signed-in person can raise and read their own tickets and requests, read published employee articles and see their own assets.

**Gap:** all assignments are global; there is no per-site or per-Team scope for roles.

## 6. Ticket Queues

A Queue is a desk; every Ticket belongs to exactly one. The default Queue `it` (prefix `TKT`) exists after the upgrade. Administration > Ticket queues (`/service-desk/queues`, permission `servicedesk.queues.manage`) creates further Queues, edits them, archives and restores them and makes one the default.

- Key, prefix and number padding are permanent; choose them deliberately. Numbers are issued per Queue and never reused once committed. Moving a Ticket to another Queue gives it a new number and keeps the old one as alias.
- The grants dialog sets who may create, view, work or manage in a Queue, per person, Team or role. People with the global `tickets.view` or `tickets.manage` permission see and work every Queue regardless.
- Each Queue appears under "Ticket queues" in the sidebar for people who may view it, with an open-ticket count.

**Gaps:** routing rules (automatic assignment) and ticket SLAs do not exist; staff set the Queue and assignee by hand.

## 7. Saved Views and pinning

Anyone who may read Tickets, Devices or Tasks can save the filter workbench of that list as a private View (filter, sort, search, columns), pin it to their own sidebar and share it with people, Teams, roles or everyone (`views.share`, `views.publish`). A share never grants data access: the viewer always sees only the rows their own permissions allow, and conditions they may not use match nothing and show a warning. Administrators with `views.pin_for_groups` pin a View for a Team or role (Pin Rule); `views.admin` lists, archives, restores and takes over any View, for example after its owner has left.

**Gap:** the UI offers no screen to restore an archived View, and Views for other resources than Tickets, Devices and Tasks do not exist.

## 8. Task Boards

Tasks > Boards (`/tasks/boards`) creates a Board over a Task filter with two to eight columns that each map to one Task status. Moving a card runs the normal Task operation with its own authorization and audit; sharing and pinning use the View mechanism. Team-owned Boards exist in the API (`tasks.boards.manage_team`) but the create dialog does not offer a Team owner yet.

## 9. Audit, AI and Presence

- Administration > Audit (`/admin/audit`, `platform.audit.view`) lists privileged changes with filters. **Gap:** the screen shows identifiers rather than names; name resolution, export and retention controls belong to the F14 audit slice, which [current status](../product/current-status.md) records once it is merged ([design](../product/f14-administration-design.md)).
- Turaco AI is read-only (summaries and knowledge search as the signed-in person) and off by default. Administration > AI (`/admin/ai`) configures AI Providers and limits; the permissions `ai.*` are granted through roles, no default role has them. See the [AI design](../product/f12-turaco-ai-design.md) before enabling an external provider.
- Workforce Presence is operational availability, not HR, and off by default; enabling needs the recorded data protection impact assessment (`/admin/presence`).

## 10. What does not work yet

- Real Microsoft Graph (Intune), Autotask REST and Teams integrations: the internal sides exist; the clients do not. Deployments halt with `assignment_failed` because the production writer is a placeholder.
- Platform health, setup checklist, integrations page, audit names and global search beyond the command palette (F14 A-C and A-F).
- CSV import and bulk edit of people, External Parties, per-site role scope.
- Development tooling that must not run in production: `turaco-admin demo seed` and `demo seed-hospital`, the simulation passwords and the load generator ([simulation](../development/simulation-hospital.md), [load testing](../development/load-testing.md)). They refuse to run unless `APP_ENV=development`.
