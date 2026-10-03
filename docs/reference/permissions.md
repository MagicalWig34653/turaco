# Permission Reference

> Generated from code. Do not edit manually.

| Permission | Risk | Description |
|---|---|---|
| `assets.manage` | elevated | Create and update assets within authorized scope. |
| `assets.view` | normal | View assets and device context within authorized scope. |
| `briefing.manage` | elevated | Create, edit, publish and withdraw IT Briefing items and see drafts and withdrawn items. |
| `briefing.view` | normal | View published IT Briefing items. |
| `catalog.manage` | elevated | Create and change Catalog Items: form definitions, approval steps and fulfillment task templates; activate and deactivate them. |
| `changes.approve` | high | Approve infrastructure/service changes according to policy. |
| `deployments.execute` | high | Start endpoint software/remediation deployments. |
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
| `organization.directory.sync` | elevated | Request an immediate directory synchronization run. |
| `organization.directory.view` | normal | View observed Directory Groups and their memberships. |
| `organization.teams.manage` | elevated | Create, rename and deactivate Teams and manage their members. Team membership determines which tasks a user with tasks.work can see and which notifications they receive, so this permission indirectly controls task access. |
| `organization.view` | normal | View organization users, teams and locations. |
| `platform.admin` | high | Administer platform-wide configuration. |
| `platform.audit.view` | elevated | Query the audit log. |
| `platform.roles.manage` | high | Create, change and delete roles and assign or revoke them; equivalent to administrator access. |
| `platform.roles.view` | normal | View roles, permissions and role assignments. |
| `problems.manage` | elevated | Open Problems, record cause, workaround and resolution, mark Known Errors and link tickets. Reading problems needs tickets.view or tickets.manage. |
| `procurement.manage` | elevated | Manage suppliers and procurement requests; create, submit for approval, send, cancel and close purchase orders. |
| `procurement.view` | normal | View suppliers, procurement requests and purchase orders. |
| `products.manage` | elevated | Create and change products, manufacturers and product categories. |
| `products.view` | normal | View the product catalog: products, manufacturers and product categories. |
| `remote_access.admin` | high | Planned: configure Remote Access Providers, their credentials and unattended-access policy records. |
| `remote_access.file_transfer` | high | Planned: transfer files inside an authorized remote-access session. |
| `remote_access.start_attended` | high | Planned: start an attended remote-access session (user consent required) through a Remote Access Provider when policy allows it. |
| `remote_access.start_unattended` | high | Planned: start an unattended remote-access session on a Device named by an unattended-access policy record, with a linked Ticket. |
| `remote_access.terminal` | high | Planned: use the provider's terminal channel inside an authorized remote-access session. |
| `remote_access.view` | normal | Planned: see whether remote access is available for a Device or Ticket (provider mapping, supported modes); does not allow starting a session. |
| `remote_access.view_sessions` | elevated | Planned: view all Remote Access Session records and their audit trail, including other technicians' sessions. |
| `requests.manage` | elevated | Cancel, put on hold, resume and complete any service request. |
| `requests.view` | elevated | View all service requests, their answers, approvals and fulfillment tasks. |
| `runbooks.execute` | elevated | Start and cancel runbook executions, which create tracked tasks. Reading runbooks needs knowledge.view or this permission. |
| `tasks.manage` | normal | Create, edit, assign, cancel and reopen any task and work on any task. |
| `tasks.recurrence.manage` | normal | Create, change, pause and delete Recurring Task Definitions that generate tasks on a schedule. |
| `tasks.view` | normal | View all tasks. Callers with only tasks.work see just the tasks assigned to them or their Teams. |
| `tasks.work` | normal | See and work (start, block, unblock, complete) tasks assigned to oneself or to one of one's Teams. |
| `tickets.manage` | elevated | Work tickets: also reads all tickets and internal comments; assign, set priority, comment internally, resolve, close, reopen and cancel any ticket. |
| `tickets.view` | normal | View all tickets and their internal comments. Every signed-in user can raise tickets and read their own. |
