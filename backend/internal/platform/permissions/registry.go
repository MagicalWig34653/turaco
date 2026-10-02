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
	{Name: "organization.teams.manage", Description: "Create, rename and deactivate Teams and manage their members.", Risk: "elevated"},
	{Name: "tasks.view", Description: "View tasks within authorized scope.", Risk: "normal"},
	{Name: "tasks.manage", Description: "Create and update tasks within authorized scope.", Risk: "normal"},
	{Name: "assets.view", Description: "View assets and device context within authorized scope.", Risk: "normal"},
	{Name: "assets.manage", Description: "Create and update assets within authorized scope.", Risk: "elevated"},
	{Name: "endpoint.management.view", Description: "View normalized endpoint-management artifacts, assignments, applicability and observations within authorized scope.", Risk: "normal"},
	{Name: "integrations.intune.manage", Description: "Administer Intune integration configuration, credentials and synchronization controls.", Risk: "high"},
	{Name: "inventory.manage", Description: "Receive, reserve, transfer and correct inventory.", Risk: "elevated"},
	{Name: "tickets.view", Description: "View service-desk records within authorized scope.", Risk: "normal"},
	{Name: "tickets.manage", Description: "Work service-desk records within authorized scope.", Risk: "normal"},
	{Name: "changes.approve", Description: "Approve infrastructure/service changes according to policy.", Risk: "high"},
	{Name: "deployments.execute", Description: "Start endpoint software/remediation deployments.", Risk: "high"},
	{Name: "remote_support.start", Description: "Start a future remote-support session when enabled by policy.", Risk: "high"},
}
