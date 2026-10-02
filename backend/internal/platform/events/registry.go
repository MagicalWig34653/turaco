package events

type Definition struct {
	Name        string
	Version     int
	Owner       string
	Description string
}

var Registry = []Definition{
	{Name: "UserSynchronized", Version: 1, Owner: "organization", Description: "Directory sync created or changed a canonical user. Payload: userId, providerKey, created, changedFields (names only), statusChanged; see docs/integrations/ldap-ad-sync-design.md."},
	{Name: "TaskAssigned", Version: 1, Owner: "tasks", Description: "A task was assigned to a User and/or Team. Payload: taskId, assignedUserId, assignedTeamId, previousUserId, previousTeamId."},
	{Name: "TaskCompleted", Version: 1, Owner: "tasks", Description: "A task was completed. Payload: taskId, completedByUserId."},
	{Name: "BriefingItemPublished", Version: 1, Owner: "briefing", Description: "A manual IT Briefing item was published. Payload: itemId, severity."},
	{Name: "AssetCreated", Version: 1, Owner: "assets", Description: "A new asset was registered."},
	{Name: "AssetAssigned", Version: 1, Owner: "assets", Description: "An asset assignment became active."},
	{Name: "StockReserved", Version: 1, Owner: "inventory", Description: "Stock or a serialized asset was reserved."},
	{Name: "GoodsReceived", Version: 1, Owner: "inventory", Description: "A goods receipt was posted."},
	{Name: "ServiceRequestSubmitted", Version: 1, Owner: "requests", Description: "A service request was submitted."},
	{Name: "TicketCreated", Version: 1, Owner: "service-desk", Description: "A ticket was created."},
	{Name: "ChangeScheduled", Version: 1, Owner: "changes", Description: "A change received an execution schedule."},
	{Name: "ManagementAssignmentChanged", Version: 1, Owner: "endpoint", Description: "A normalized management-provider assignment meaningfully changed."},
	{Name: "ManagementApplicabilityChanged", Version: 1, Owner: "endpoint", Description: "Turaco's expected applicability evaluation meaningfully changed for a managed target."},
	{Name: "DeploymentStarted", Version: 1, Owner: "endpoint", Description: "A deployment began target execution."},
	{Name: "DeploymentCompleted", Version: 1, Owner: "endpoint", Description: "A deployment reached a terminal completion state."},
	{Name: "DeploymentTargetFailed", Version: 1, Owner: "endpoint", Description: "A deployment target attempt failed."},
}
