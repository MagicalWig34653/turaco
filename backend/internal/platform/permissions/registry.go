package permissions

type Permission struct {
	Name        string
	Description string
	Risk        string
}

var Registry = []Permission{
	{Name: "platform.admin", Description: "Administer platform-wide configuration.", Risk: "high"},
	{Name: "platform.roles.view", Description: "View roles, permissions and role assignments.", Risk: "normal"},
	{Name: "platform.roles.manage", Description: "Create, change and delete roles and assign or revoke them; equivalent to administrator access.", Risk: "high"},
	{Name: "platform.audit.view", Description: "Query the audit log.", Risk: "elevated"},
	{Name: "organization.view", Description: "View organization users, teams and locations.", Risk: "normal"},
	{Name: "organization.directory.view", Description: "View observed Directory Groups and their memberships.", Risk: "normal"},
	{Name: "organization.directory.sync", Description: "Request an immediate directory synchronization run.", Risk: "elevated"},
	{Name: "organization.teams.manage", Description: "Create, rename and deactivate Teams and manage their members. Team membership determines which tasks a user with tasks.work can see and which notifications they receive, so this permission indirectly controls task access.", Risk: "elevated"},
	{Name: "tasks.view", Description: "View all tasks. Callers with only tasks.work see just the tasks assigned to them or their Teams.", Risk: "normal"},
	{Name: "tasks.work", Description: "See and work (start, block, unblock, complete) tasks assigned to oneself or to one of one's Teams.", Risk: "normal"},
	{Name: "tasks.recurrence.manage", Description: "Create, change, pause and delete Recurring Task Definitions that generate tasks on a schedule.", Risk: "normal"},
	{Name: "tasks.manage", Description: "Create, edit, assign, cancel and reopen any task and work on any task.", Risk: "normal"},
	{Name: "briefing.view", Description: "View published IT Briefing items.", Risk: "normal"},
	{Name: "briefing.manage", Description: "Create, edit, publish and withdraw IT Briefing items and see drafts and withdrawn items.", Risk: "elevated"},
	{Name: "products.view", Description: "View the product catalog: products, manufacturers and product categories.", Risk: "normal"},
	{Name: "products.manage", Description: "Create and change products, manufacturers and product categories.", Risk: "elevated"},
	{Name: "catalog.manage", Description: "Create and change Catalog Items: form definitions, approval steps and fulfillment task templates; activate and deactivate them.", Risk: "elevated"},
	{Name: "requests.view", Description: "View all service requests, their answers, approvals and fulfillment tasks.", Risk: "elevated"},
	{Name: "requests.manage", Description: "Cancel, put on hold, resume and complete any service request.", Risk: "elevated"},
	{Name: "assets.view", Description: "View assets and device context within authorized scope.", Risk: "normal"},
	{Name: "assets.manage", Description: "Create and update assets within authorized scope.", Risk: "elevated"},
	{Name: "endpoint.management.view", Description: "View normalized endpoint-management artifacts, assignments, applicability and observations within authorized scope.", Risk: "normal"},
	{Name: "integrations.intune.manage", Description: "Administer Intune integration configuration, credentials and synchronization controls.", Risk: "high"},
	{Name: "procurement.view", Description: "View suppliers, procurement requests and purchase orders.", Risk: "normal"},
	{Name: "procurement.manage", Description: "Manage suppliers and procurement requests; create, submit for approval, send, cancel and close purchase orders.", Risk: "elevated"},
	{Name: "inventory.view", Description: "View warehouses, stock balances, the inventory ledger and reservations.", Risk: "normal"},
	{Name: "inventory.manage", Description: "Manage warehouses and storage locations; issue, return, transfer, correct and dispose stock; reserve, release and fulfill reservations; post goods receipts.", Risk: "elevated"},
	{Name: "tickets.view", Description: "View service-desk records within authorized scope.", Risk: "normal"},
	{Name: "tickets.manage", Description: "Work service-desk records within authorized scope.", Risk: "normal"},
	{Name: "changes.approve", Description: "Approve infrastructure/service changes according to policy.", Risk: "high"},
	{Name: "deployments.execute", Description: "Start endpoint software/remediation deployments.", Risk: "high"},
	{Name: "remote_support.start", Description: "Start a future remote-support session when enabled by policy.", Risk: "high"},
}
