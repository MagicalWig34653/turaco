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
	{Name: "TaskCancelled", Version: 1, Owner: "tasks", Description: "A task was cancelled, by a person or because the record it belongs to was cancelled. Payload: taskId."},
	{Name: "ApprovalRequested", Version: 1, Owner: "approvals", Description: "An approval step became pending. Payload: approvalId, subjectType, subjectId, stepIndex."},
	{Name: "ApprovalDecided", Version: 1, Owner: "approvals", Description: "An approval was approved or rejected. Payload: approvalId, subjectType, subjectId, stepIndex, decision."},
	{Name: "ServiceRequestApproved", Version: 1, Owner: "requests", Description: "A service request was approved (or needed no approval) and entered fulfillment. Payload: requestId."},
	{Name: "ServiceRequestRejected", Version: 1, Owner: "requests", Description: "A service request was rejected by an approver. Payload: requestId."},
	{Name: "ServiceRequestCompleted", Version: 1, Owner: "requests", Description: "A service request was completed. Payload: requestId."},
	{Name: "ServiceRequestCancelled", Version: 1, Owner: "requests", Description: "A service request was cancelled. Payload: requestId."},
	{Name: "BriefingItemPublished", Version: 1, Owner: "briefing", Description: "A manual IT Briefing item was published. Payload: itemId, severity."},
	{Name: "AssetCreated", Version: 1, Owner: "assets", Description: "A new asset was registered. Payload: assetId, productId."},
	{Name: "AssetAssigned", Version: 1, Owner: "assets", Description: "An asset assignment became active. Payload: assetId, status, operation, assigneeType, assigneeId."},
	{Name: "AssetReturned", Version: 1, Owner: "assets", Description: "An assigned asset was returned. Payload: assetId, status, operation, assigneeType, assigneeId (previous assignee)."},
	{Name: "AssetStatusChanged", Version: 1, Owner: "assets", Description: "An asset changed lifecycle status other than by assignment or return. Payload: assetId, status, operation."},
	{Name: "StockReserved", Version: 1, Owner: "inventory", Description: "Stock or a serialized asset was reserved. Payload: reservationId, kind, productId, status, quantity or assetId, contextType, contextId."},
	{Name: "ReservationReleased", Version: 1, Owner: "inventory", Description: "A reservation was released and its stock or asset is available again. Payload as StockReserved."},
	{Name: "ReservationFulfilled", Version: 1, Owner: "inventory", Description: "A reservation was fulfilled: stock was issued or the reserved asset was assigned. Payload as StockReserved."},
	{Name: "PurchaseOrderApproved", Version: 1, Owner: "procurement", Description: "A purchase order was approved. Payload: orderId, supplierId."},
	{Name: "PurchaseOrderSent", Version: 1, Owner: "procurement", Description: "A purchase order was sent to the supplier. Payload: orderId, supplierId."},
	{Name: "PurchaseOrderReceived", Version: 1, Owner: "procurement", Description: "Every line of a purchase order was received. Payload: orderId, supplierId."},
	{Name: "GoodsReceived", Version: 1, Owner: "inventory", Description: "A goods receipt was posted."},
	{Name: "ServiceRequestSubmitted", Version: 1, Owner: "requests", Description: "A service request was submitted. Payload: requestId."},
	{Name: "KnowledgeArticlePublished", Version: 1, Owner: "knowledge", Description: "A knowledge article was published. Payload: articleId, audience."},
	{Name: "MajorIncidentDeclared", Version: 1, Owner: "service-desk", Description: "A Major Incident was declared. Payload: majorIncidentId, status."},
	{Name: "MajorIncidentUpdated", Version: 1, Owner: "service-desk", Description: "A Major Incident changed status or got a public update. Payload: majorIncidentId, status."},
	{Name: "RunbookExecutionStarted", Version: 1, Owner: "knowledge", Description: "A runbook execution started and created its tasks. Payload: executionId, runbookId."},
	{Name: "RunbookExecutionCompleted", Version: 1, Owner: "knowledge", Description: "Every task of a runbook execution finished. Payload: executionId, runbookId."},
	{Name: "TicketCreated", Version: 1, Owner: "service-desk", Description: "A ticket was created. Payload: ticketId, reporterId, affectedUserId."},
	{Name: "TicketAssigned", Version: 1, Owner: "service-desk", Description: "A ticket was assigned to a user. Payload: ticketId, assigneeId."},
	{Name: "TicketResolved", Version: 1, Owner: "service-desk", Description: "A ticket was resolved. Payload: ticketId, reporterId, affectedUserId."},
	{Name: "TicketStatusChanged", Version: 1, Owner: "service-desk", Description: "A ticket was reopened, closed or cancelled. Payload: ticketId, operation."},
	{Name: "TicketCommentAdded", Version: 1, Owner: "service-desk", Description: "A comment was added to a ticket. Payload: ticketId, commentId, internal, authorId."},
	{Name: "ChangeScheduled", Version: 1, Owner: "changes", Description: "A change received an execution schedule."},
	{Name: "DeviceLinked", Version: 1, Owner: "endpoints", Description: "A provider-observed Device was linked to an Asset by serial number match or by hand. Payload: deviceId, assetId, method."},
	{Name: "DeviceUnlinked", Version: 1, Owner: "endpoints", Description: "A Device lost its Asset link: by hand, or because the serial number changed, the serial number is shared by several devices, the device was tombstoned or its manual link collided on revival. Payload: deviceId, assetId, method."},
	{Name: "EndpointFindingRaised", Version: 1, Owner: "endpoints", Description: "An endpoint data-quality finding was raised. Payload: findingId, deviceId, kind."},
	{Name: "ManagementAssignmentChanged", Version: 1, Owner: "endpoint", Description: "A normalized management-provider assignment meaningfully changed."},
	{Name: "ManagementApplicabilityChanged", Version: 1, Owner: "endpoint", Description: "Turaco's expected applicability evaluation meaningfully changed for a managed target."},
	{Name: "DeploymentStarted", Version: 1, Owner: "endpoint", Description: "A deployment began target execution."},
	{Name: "DeploymentCompleted", Version: 1, Owner: "endpoint", Description: "A deployment reached a terminal completion state."},
	{Name: "DeploymentTargetFailed", Version: 1, Owner: "endpoint", Description: "A deployment target attempt failed."},
}
