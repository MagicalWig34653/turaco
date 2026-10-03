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
| `endpoint.management.view` | normal | View normalized endpoint-management artifacts, assignments, applicability and observations within authorized scope. |
| `integrations.intune.manage` | high | Administer Intune integration configuration, credentials and synchronization controls. |
| `inventory.manage` | elevated | Manage warehouses and storage locations; issue, return, transfer, correct and dispose stock; reserve, release and fulfill reservations; post goods receipts. |
| `inventory.view` | normal | View warehouses, stock balances, the inventory ledger and reservations. |
| `organization.directory.sync` | elevated | Request an immediate directory synchronization run. |
| `organization.directory.view` | normal | View observed Directory Groups and their memberships. |
| `organization.teams.manage` | elevated | Create, rename and deactivate Teams and manage their members. Team membership determines which tasks a user with tasks.work can see and which notifications they receive, so this permission indirectly controls task access. |
| `organization.view` | normal | View organization users, teams and locations. |
| `platform.admin` | high | Administer platform-wide configuration. |
| `platform.audit.view` | elevated | Query the audit log. |
| `platform.roles.manage` | high | Create, change and delete roles and assign or revoke them; equivalent to administrator access. |
| `platform.roles.view` | normal | View roles, permissions and role assignments. |
| `procurement.manage` | elevated | Manage suppliers and procurement requests; create, submit for approval, send, cancel and close purchase orders. |
| `procurement.view` | normal | View suppliers, procurement requests and purchase orders. |
| `products.manage` | elevated | Create and change products, manufacturers and product categories. |
| `products.view` | normal | View the product catalog: products, manufacturers and product categories. |
| `remote_support.start` | high | Start a future remote-support session when enabled by policy. |
| `requests.manage` | elevated | Cancel, put on hold, resume and complete any service request. |
| `requests.view` | elevated | View all service requests, their answers, approvals and fulfillment tasks. |
| `tasks.manage` | normal | Create, edit, assign, cancel and reopen any task and work on any task. |
| `tasks.recurrence.manage` | normal | Create, change, pause and delete Recurring Task Definitions that generate tasks on a schedule. |
| `tasks.view` | normal | View all tasks. Callers with only tasks.work see just the tasks assigned to them or their Teams. |
| `tasks.work` | normal | See and work (start, block, unblock, complete) tasks assigned to oneself or to one of one's Teams. |
| `tickets.manage` | normal | Work service-desk records within authorized scope. |
| `tickets.view` | normal | View service-desk records within authorized scope. |
