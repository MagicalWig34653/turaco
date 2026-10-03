# Event Catalog

> Generated from code. Do not edit manually.

| Event | Version | Owner | Description |
|---|---:|---|---|
| `ApprovalDecided` | 1 | approvals | An approval was approved or rejected. Payload: approvalId, subjectType, subjectId, stepIndex, decision. |
| `ApprovalRequested` | 1 | approvals | An approval step became pending. Payload: approvalId, subjectType, subjectId, stepIndex. |
| `AssetAssigned` | 1 | assets | An asset assignment became active. Payload: assetId, status, operation, assigneeType, assigneeId. |
| `AssetCreated` | 1 | assets | A new asset was registered. Payload: assetId, productId. |
| `AssetReturned` | 1 | assets | An assigned asset was returned. Payload: assetId, status, operation, assigneeType, assigneeId (previous assignee). |
| `AssetStatusChanged` | 1 | assets | An asset changed lifecycle status other than by assignment or return. Payload: assetId, status, operation. |
| `BriefingItemPublished` | 1 | briefing | A manual IT Briefing item was published. Payload: itemId, severity. |
| `ChangeScheduled` | 1 | changes | A change received an execution schedule. |
| `DeploymentCompleted` | 1 | endpoint | A deployment reached a terminal completion state. |
| `DeploymentStarted` | 1 | endpoint | A deployment began target execution. |
| `DeploymentTargetFailed` | 1 | endpoint | A deployment target attempt failed. |
| `GoodsReceived` | 1 | inventory | A goods receipt was posted. |
| `ManagementApplicabilityChanged` | 1 | endpoint | Turaco's expected applicability evaluation meaningfully changed for a managed target. |
| `ManagementAssignmentChanged` | 1 | endpoint | A normalized management-provider assignment meaningfully changed. |
| `ServiceRequestApproved` | 1 | requests | A service request was approved (or needed no approval) and entered fulfillment. Payload: requestId. |
| `ServiceRequestCancelled` | 1 | requests | A service request was cancelled. Payload: requestId. |
| `ServiceRequestCompleted` | 1 | requests | A service request was completed. Payload: requestId. |
| `ServiceRequestRejected` | 1 | requests | A service request was rejected by an approver. Payload: requestId. |
| `ServiceRequestSubmitted` | 1 | requests | A service request was submitted. Payload: requestId. |
| `StockReserved` | 1 | inventory | Stock or a serialized asset was reserved. |
| `TaskAssigned` | 1 | tasks | A task was assigned to a User and/or Team. Payload: taskId, assignedUserId, assignedTeamId, previousUserId, previousTeamId. |
| `TaskCancelled` | 1 | tasks | A task was cancelled, by a person or because the record it belongs to was cancelled. Payload: taskId. |
| `TaskCompleted` | 1 | tasks | A task was completed. Payload: taskId, completedByUserId. |
| `TicketCreated` | 1 | service-desk | A ticket was created. |
| `UserSynchronized` | 1 | organization | Directory sync created or changed a canonical user. Payload: userId, providerKey, created, changedFields (names only), statusChanged; see docs/integrations/ldap-ad-sync-design.md. |
