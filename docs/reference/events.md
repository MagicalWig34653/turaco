# Event Catalog

> Generated from code. Do not edit manually.

| Event | Version | Owner | Description |
|---|---:|---|---|
| `AssetAssigned` | 1 | assets | An asset assignment became active. |
| `AssetCreated` | 1 | assets | A new asset was registered. |
| `BriefingItemPublished` | 1 | briefing | A manual IT Briefing item was published. Payload: itemId, severity. |
| `ChangeScheduled` | 1 | changes | A change received an execution schedule. |
| `DeploymentCompleted` | 1 | endpoint | A deployment reached a terminal completion state. |
| `DeploymentStarted` | 1 | endpoint | A deployment began target execution. |
| `DeploymentTargetFailed` | 1 | endpoint | A deployment target attempt failed. |
| `GoodsReceived` | 1 | inventory | A goods receipt was posted. |
| `ManagementApplicabilityChanged` | 1 | endpoint | Turaco's expected applicability evaluation meaningfully changed for a managed target. |
| `ManagementAssignmentChanged` | 1 | endpoint | A normalized management-provider assignment meaningfully changed. |
| `ServiceRequestSubmitted` | 1 | requests | A service request was submitted. |
| `StockReserved` | 1 | inventory | Stock or a serialized asset was reserved. |
| `TaskAssigned` | 1 | tasks | A task was assigned to a User and/or Team. Payload: taskId, assignedUserId, assignedTeamId, previousUserId, previousTeamId. |
| `TaskCompleted` | 1 | tasks | A task was completed. Payload: taskId, completedByUserId. |
| `TicketCreated` | 1 | service-desk | A ticket was created. |
| `UserSynchronized` | 1 | organization | Directory sync created or changed a canonical user. Payload: userId, providerKey, created, changedFields (names only), statusChanged; see docs/integrations/ldap-ad-sync-design.md. |
