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
| `ChangeApproved` | 1 | changes | A Change was approved: by its approver, without approval when none was required, or on an emergency justification. Payload: changeId, emergency. |
| `ChangeCompleted` | 1 | changes | A Change in progress was completed. Payload: changeId. |
| `ChangeFailed` | 1 | changes | A Change in progress failed. Payload: changeId, reason, rollbackDone. |
| `ChangeRejected` | 1 | changes | The approver rejected a Change. Payload: changeId. |
| `ChangeScheduled` | 1 | changes | An approved Change was scheduled in its maintenance window; published once per scheduling. Payload: changeId, emergency, windowStart, windowEnd. |
| `ChangeScheduledFanOut` | 1 | changes (internal) | Continuation of the change.scheduled notification fan-out to a large audience (the next chunk of recipients). Payload: changeId, after (the last recipient id already notified). |
| `ChangeStarted` | 1 | changes | A scheduled Change started execution. Payload: changeId. |
| `ChangeSubmitted` | 1 | changes | A draft Change was submitted for assessment. Payload: changeId, kind, risk. |
| `DeploymentCompleted` | 1 | endpoint | A deployment reached a terminal completion state. |
| `DeploymentStarted` | 1 | endpoint | A deployment began target execution. |
| `DeploymentTargetFailed` | 1 | endpoint | A deployment target attempt failed. |
| `DeviceLinked` | 1 | endpoints | A provider-observed Device was linked to an Asset by serial number match or by hand. Payload: deviceId, assetId, method. |
| `DeviceUnlinked` | 1 | endpoints | A Device lost its Asset link: by hand, or because the serial number changed, the serial number is shared by several devices, the device was tombstoned or its manual link collided on revival. Payload: deviceId, assetId, method. |
| `EndpointFindingRaised` | 1 | endpoints | An endpoint finding was raised. Payload: findingId, kind and deviceId, or softwarePackageId for package_hash_mismatch. |
| `GoodsReceived` | 1 | inventory | A goods receipt was posted. |
| `InitiativeStatusChanged` | 1 | planning | An Initiative changed status (planning started, proposed, approved, approval rejected, activated, held, resumed, completed, cancelled). Payload: initiativeId, operation, status, previousStatus; hold, cancel and rejection also reason. |
| `KnowledgeArticlePublished` | 1 | knowledge | A knowledge article was published. Payload: articleId, audience. |
| `MajorIncidentDeclared` | 1 | service-desk | A Major Incident was declared. Payload: majorIncidentId, status. |
| `MajorIncidentUpdated` | 1 | service-desk | A Major Incident changed status or got a public update. Payload: majorIncidentId, status. |
| `ManagementApplicabilityChanged` | 1 | endpoints | Turaco's expected applicability evaluation meaningfully changed for a managed target. |
| `ManagementAssignmentChanged` | 1 | endpoints | The current assignments of a Management Artifact meaningfully changed during a provider ingestion run: assignments were opened or closed, or a changed assignment was replaced; unchanged re-reads publish nothing. One event per artifact and run. Payload: artifactId, provider, opened, closed. |
| `PurchaseOrderApproved` | 1 | procurement | A purchase order was approved. Payload: orderId, supplierId. |
| `PurchaseOrderReceived` | 1 | procurement | Every line of a purchase order was received. Payload: orderId, supplierId. |
| `PurchaseOrderSent` | 1 | procurement | A purchase order was sent to the supplier. Payload: orderId, supplierId. |
| `RackPlacementChanged` | 1 | infrastructure | An Asset was placed into, moved within or removed from a Rack. Payload: placementId, rackId, assetId, operation (placed, moved, removed), uPosition, heightU, face; removals also reason. |
| `ReservationFulfilled` | 1 | inventory | A reservation was fulfilled: stock was issued or the reserved asset was assigned. Payload as StockReserved. |
| `ReservationReleased` | 1 | inventory | A reservation was released and its stock or asset is available again. Payload as StockReserved. |
| `RunbookExecutionCompleted` | 1 | knowledge | Every task of a runbook execution finished. Payload: executionId, runbookId. |
| `RunbookExecutionStarted` | 1 | knowledge | A runbook execution started and created its tasks. Payload: executionId, runbookId. |
| `SecurityAdvisoryPublished` | 1 | security | A Security Advisory became applicable. Payload: advisoryId, severity. |
| `SecurityAdvisoryPublishedFanOut` | 1 | security (internal) | Continuation of security.advisory notification fan-out. Payload: advisoryId, after, sourceEventId. |
| `ServiceCreated` | 1 | services | A Service was created. Payload: serviceId, criticality, status. |
| `ServiceRequestApproved` | 1 | requests | A service request was approved (or needed no approval) and entered fulfillment. Payload: requestId. |
| `ServiceRequestCancelled` | 1 | requests | A service request was cancelled. Payload: requestId. |
| `ServiceRequestCompleted` | 1 | requests | A service request was completed. Payload: requestId. |
| `ServiceRequestRejected` | 1 | requests | A service request was rejected by an approver. Payload: requestId. |
| `ServiceRequestSubmitted` | 1 | requests | A service request was submitted. Payload: requestId. |
| `ServiceStatusChanged` | 1 | services | A Service changed status or was retired. Payload: serviceId, operation (status_changed, retired), status, previousStatus, reason. |
| `SoftwarePackagePublished` | 1 | endpoints | The Software Management Provider reported a Software Package as published into the Management Provider (by a publish operation or a package synchronization). Payload: packageId, versionId, provider. |
| `SoftwareVersionApprovalRequested` | 1 | endpoints | The approval of a Software Version was requested. Payload: versionId, productId. |
| `SoftwareVersionApprovalRequestedFanOut` | 1 | endpoints (internal) | Continuation of the software.approval_requested notification fan-out. Payload: versionId, after, sourceEventId. |
| `SoftwareVersionApproved` | 1 | endpoints | A Software Version was approved, bound to its installer hash. Payload: versionId, productId, installerSha256. |
| `SoftwareVersionRevoked` | 1 | endpoints | The approval of a Software Version was revoked. Payload: versionId, productId, reason. |
| `StockReserved` | 1 | inventory | Stock or a serialized asset was reserved. Payload: reservationId, kind, productId, status, quantity or assetId, contextType, contextId. |
| `TaskAssigned` | 1 | tasks | A task was assigned to a User and/or Team. Payload: taskId, assignedUserId, assignedTeamId, previousUserId, previousTeamId. |
| `TaskCancelled` | 1 | tasks | A task was cancelled, by a person or because the record it belongs to was cancelled. Payload: taskId. |
| `TaskCompleted` | 1 | tasks | A task was completed. Payload: taskId, completedByUserId. |
| `TicketAssigned` | 1 | service-desk | A ticket was assigned to a user. Payload: ticketId, assigneeId. |
| `TicketCommentAdded` | 1 | service-desk | A comment was added to a ticket. Payload: ticketId, commentId, internal, authorId. |
| `TicketCreated` | 1 | service-desk | A ticket was created. Payload: ticketId, reporterId, affectedUserId. |
| `TicketResolved` | 1 | service-desk | A ticket was resolved. Payload: ticketId, reporterId, affectedUserId. |
| `TicketStatusChanged` | 1 | service-desk | A ticket was reopened, closed or cancelled. Payload: ticketId, operation. |
| `UserSynchronized` | 1 | organization | Directory sync created or changed a canonical user. Payload: userId, providerKey, created, changedFields (names only), statusChanged; see docs/integrations/ldap-ad-sync-design.md. |
| `VirtualMachineChanged` | 1 | infrastructure | A Virtual Machine was created, changed, moved to another state or hypervisor, or decommissioned. Payload: virtualMachineId, operation, state, hypervisorAssetId; decommissioning also reason. |
| `VulnerabilityFindingChanged` | 1 | security | A vulnerability finding was created or changed status. Payload: findingId, advisoryId, deviceId, status, confidence, operation, previousStatus and reason when applicable. |
