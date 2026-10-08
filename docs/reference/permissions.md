# Permission Reference

> Generated from code. Do not edit manually.

| Permission | Risk | Description |
|---|---|---|
| `ai.settings.manage` | high | Configure AI Providers, allowed data classes, caps, conversation retention and the runtime switch of the assistant. Decides which business data may leave the installation for an external provider. |
| `ai.settings.view` | elevated | View the AI Provider configuration (never secrets), the data classes allowed per provider, caps and retention. |
| `ai.usage.view` | elevated | View aggregated AI usage and estimated cost (counts only, no prompts). |
| `ai.use` | normal | Use the Turaco assistant. It never grants more than the User already has: every tool call additionally needs the permission of the module it reads and is limited to the records the User named in the conversation. |
| `assets.manage` | elevated | Create and update assets within authorized scope. |
| `assets.view` | normal | View assets and device context within authorized scope. |
| `briefing.manage` | elevated | Create, edit, publish and withdraw IT Briefing items and see drafts and withdrawn items. |
| `briefing.view` | normal | View published IT Briefing items. |
| `catalog.manage` | elevated | Create and change Catalog Items: form definitions, approval steps and fulfillment task templates; activate and deactivate them. |
| `changes.approve` | high | Approve infrastructure/service changes according to policy. |
| `changes.execute` | elevated | Run Changes: start, complete and fail them and add execution tasks. The owner of a Change may do the same for that Change without this permission. |
| `changes.manage` | elevated | Create and change Changes, edit their affected resources, submit, assess, schedule, review, close and cancel them, approve emergency changes on a justification and add execution tasks. Scheduling and emergency approval affect other people's maintenance planning. |
| `changes.view` | normal | View all Changes (risk, rollback plan, maintenance window, affected resources, approvals, execution tasks, history) and the Change impact view. Requesters and owners see their own Changes without it; affected Services need services.view, Virtual Machines and Locations infrastructure.view, Assets assets.view to be shown by name. |
| `deployments.approve` | elevated | Approve or reject high-impact Deployment plans as their named approver (or approver Team member). Required when the plan is submitted and verified again when the decision is applied; owners, creators, editors, the submitter and the authors and last editors of the plan's Target Sets never approve. Approvers read the plan and its Target Sets' definitions and evaluation counts. |
| `deployments.execute` | elevated | Start, pause, resume, halt, cancel and promote the rings of scheduled Deployments within their approved plan and gates (F9 G3); starting needs the write capability SOFTWARE_DEPLOY_WRITE. Promoting a high-impact plan additionally needs deployments.high_impact; the person who planned a high-impact plan cannot start it. Includes deployments.view. |
| `deployments.high_impact` | elevated | Plan high-impact Deployments: uninstall, supersede, rings on Target Sets selecting all Devices or a root group with its nested groups, and plans targeting at least 200 Devices or 25% of the live Devices; submit such plans for their plan Approval (the planner never approves). Beyond deployments.manage. |
| `deployments.manage` | elevated | Create and change Target Sets and Deployment plans (draft rings, gates, Change windows), validate, schedule and cancel plans. Plans that are high impact additionally need deployments.high_impact. Includes deployments.view. |
| `deployments.view` | normal | View all Deployment plans (version, intent, rings with gates, approvals, history, validation) and Target Sets with their evaluation and explanation. Example Device names additionally need endpoints.view. Owners and creators see their own plans without it. |
| `endpoint.management.view` | normal | View normalized endpoint-management artifacts, assignments and filters, and (together with endpoints.view) the provider-reported observations of a Device; the Expected Applicability views (Device, Assignment Path, reverse lookup of an artifact) need no further permission for assignments and counts but show Device data only with endpoints.view, Directory Group and User views need organization.directory.view, and the Device of a User or the User of a Device needs assets.view (without it the User is unknown, never guessed). An assignment's target group id is shown only to callers who also hold organization.directory.view. endpoints.manage includes this read access. |
| `endpoints.manage` | elevated | Link and unlink Devices to Assets by hand (also needs assets.view), register normalized software products and run the endpoint provider synchronization (devices and management data) and snapshot ingestion (the sync is covered by this permission; no separate sync permission exists). |
| `endpoints.view` | normal | View provider-observed Devices, their installed software and endpoint data-quality findings. |
| `infrastructure.manage` | elevated | Manage Buildings, Rooms and Racks; place, move and remove Assets in Racks; create, change, assign a hypervisor to and decommission Virtual Machines. |
| `infrastructure.view` | normal | View Buildings, Rooms, Racks, rack placements (rack elevation), Virtual Machines and the infrastructure site tree; with assets.view also where an Asset is placed. |
| `integrations.intune.manage` | high | Administer Intune integration configuration, credentials and synchronization controls. |
| `inventory.manage` | elevated | Manage warehouses and storage locations; issue, return, transfer, correct and dispose stock; reserve, release and fulfill reservations; post goods receipts. |
| `inventory.view` | normal | View warehouses, stock balances, the inventory ledger and reservations. |
| `knowledge.manage` | elevated | Write, publish and retire knowledge articles and read drafts and retired articles. |
| `knowledge.view` | normal | Read published internal knowledge articles (published employee articles are readable by every signed-in user). |
| `majorincidents.manage` | elevated | Declare Major Incidents, post public status updates, move them through their lifecycle and link tickets. |
| `modules.manage` | elevated | See the module overview and switch optional modules on and off at runtime (ADR-0032). A switched-off module disappears from the API and UI and its background jobs pause; no data is deleted. Module preconditions such as privacy records or providers are never bypassed. |
| `organization.directory.sync` | elevated | Request an immediate directory synchronization run. |
| `organization.directory.view` | normal | View observed Directory Groups and their memberships. |
| `organization.teams.manage` | elevated | Create, rename and deactivate Teams and manage their members. Team membership determines which tasks a user with tasks.work can see and which notifications they receive, so this permission indirectly controls task access. |
| `organization.view` | normal | View organization users, teams and locations. |
| `planning.manage` | elevated | Create and change Initiatives, their milestones and included records, and drive their lifecycle (start planning, propose for approval, activate, hold, resume, complete, cancel). Proposing chooses the approver; the owner, creator, proposer and editors can never approve. |
| `planning.view` | normal | View all Initiatives (goal, owner, status, target date, milestones, included Changes, Tasks, Procurement Requests and Services, progress, approvals, history) and the maintenance calendar. Owners see their own Initiatives without it; included records appear by name only with the matching view permission of their module. |
| `platform.admin` | high | Administer platform-wide configuration. |
| `platform.audit.view` | elevated | Query the audit log. |
| `platform.roles.manage` | high | Create, change and delete roles and assign or revoke them; equivalent to administrator access. |
| `platform.roles.view` | normal | View roles, permissions and role assignments. |
| `presence.admin` | high | Administer Workforce Presence: the runtime switch, the recorded data protection impact assessment and works-council dates, retention, and immediate purge of all entries. |
| `presence.manage_entries` | elevated | Create, change and cancel Workforce Presence entries of other Users in one's Teams. Every change is audited per entry. |
| `presence.manage_own` | normal | Create, change and cancel one's own Workforce Presence entries (work location, remote, travelling, unavailable) and read them. Entries carry no reason or note. |
| `presence.manage_teams` | elevated | Set the minimum Team Coverage (and the on-site minimum) of Teams. |
| `presence.view_availability` | elevated | See Operational Availability (available, limited, unavailable, unknown) and Team Coverage counts of the Users in one's own Teams. No entry details. |
| `presence.view_entries` | elevated | See the entry details of other Users in one's Teams that their owners allowed to be seen, and the names behind Team Coverage counts. Every read is audited (viewer, scope, count; never content). |
| `problems.manage` | elevated | Open Problems, record cause, workaround and resolution, mark Known Errors and link tickets. Reading problems needs tickets.view or tickets.manage. |
| `procurement.manage` | elevated | Manage suppliers and procurement requests; create, submit for approval, send, cancel and close purchase orders. |
| `procurement.view` | normal | View suppliers, procurement requests and purchase orders. |
| `products.manage` | elevated | Create and change products, manufacturers and product categories. |
| `products.view` | normal | View the product catalog: products, manufacturers and product categories. |
| `remote_access.admin` | high | Map and unmap Devices to provider peer ids, see unmasked peer ids, cancel or close other technicians' sessions and approve sessions that need a second approver. Provider credentials and unattended-access policy records stay planned. |
| `remote_access.file_transfer` | high | Planned (reserved, checked by no route; unattended access, terminal and file transfer are not offered in F10): transfer files inside an authorized remote-access session. |
| `remote_access.start_attended` | high | Request and launch an attended remote-access session for a Device from an open Ticket whose affected User is the Device's holder (or with a reason code), through an enabled Remote Access Provider; the user consents in the provider client. Policy may require a second approver. Rate limited; see the own sessions. |
| `remote_access.start_unattended` | high | Planned (reserved, checked by no route; unattended access, terminal and file transfer are not offered in F10): start an unattended remote-access session on a Device named by an unattended-access policy record, with a linked Ticket. |
| `remote_access.terminal` | high | Planned (reserved, checked by no route; unattended access, terminal and file transfer are not offered in F10): use the provider's terminal channel inside an authorized remote-access session. |
| `remote_access.view` | normal | See whether attended remote access is available for a Device: enabled providers, mapped peers (peer ids masked), freshness of the last observation and why a session is blocked. Does not allow starting a session. |
| `remote_access.view_sessions` | elevated | View all Remote Access Session records and their transitions, including other technicians' sessions. Provider launch links are never stored. |
| `requests.manage` | elevated | Cancel, put on hold, resume and complete any service request. |
| `requests.view` | elevated | View all service requests, their answers, approvals and fulfillment tasks. |
| `runbooks.execute` | elevated | Start and cancel runbook executions, which create tracked tasks. Reading runbooks needs knowledge.view or this permission. |
| `security.accept_risk` | elevated | Accept vulnerability finding risk with a reason code and review date. |
| `security.manage` | elevated | Create, import, analyze and manage security advisories and vulnerability findings, excluding risk acceptance. |
| `security.view` | normal | View security advisories and vulnerability findings. Device names additionally require endpoints.view. |
| `services.manage` | elevated | Create and change Services, change their status, retire them, and add or remove their dependencies on Services, Virtual Machines, Assets and Locations. |
| `services.view` | normal | View IT Services (owner, support team, criticality, status), their dependencies and dependents, and the impact view; Virtual Machine and Location names in them also need infrastructure.view, Asset references assets.view. |
| `software.approve` | elevated | Approve, reject and revoke Software Version approvals (never for a version the same person registered or requested) and change a Software Product's approval status (approve, deprecate, retire, block, unblock). Includes software.view. |
| `software.package` | elevated | Register Software Versions, request their approval, package approved versions through the Software Management Provider, publish packages whose reported installer hash equals the approved hash and run the package synchronization. Includes software.view. |
| `software.view` | normal | View Software Products with their Software Approval Status, Software Versions with their approval history, Software Packages and the provider catalog search. |
| `tasks.manage` | normal | Create, edit, assign, cancel and reopen any task and work on any task. |
| `tasks.recurrence.manage` | normal | Create, change, pause and delete Recurring Task Definitions that generate tasks on a schedule. |
| `tasks.view` | normal | View all tasks. Callers with only tasks.work see just the tasks assigned to them or their Teams. |
| `tasks.work` | normal | See and work (start, block, unblock, complete) tasks assigned to oneself or to one of one's Teams. |
| `tickets.manage` | elevated | Work tickets: also reads all tickets and internal comments; assign, set priority, comment internally, resolve, close, reopen and cancel any ticket. |
| `tickets.view` | normal | View all tickets and their internal comments. Every signed-in user can raise tickets and read their own. |
