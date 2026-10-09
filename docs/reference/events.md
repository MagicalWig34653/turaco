# Event Catalog

> Generated from code. Do not edit manually.

| Event | Version | Owner | Description |
|---|---:|---|---|
| `AIProviderChanged` | 1 | platform | An AI Provider was created or reconfigured. Payload: providerId, operation (created, updated), enabled. Carries no endpoint, secret reference or settings. |
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
| `DeploymentCancelled` | 1 | endpoints | A Deployment plan was cancelled with a reason code. Payload: deploymentId, reason, previousStatus. |
| `DeploymentCompleted` | 1 | endpoints | A Deployment finished all rings. Payload: deploymentId, status (completed|completed_with_errors), successful, failed. |
| `DeploymentFailed` | 1 | endpoints | A Deployment failed before or during execution (target resolution refused, gates closed). Payload: deploymentId, reason. |
| `DeploymentFailureClusterDetected` | 1 | endpoints | The correlation found a failure cluster of a Deployment (failed or expired targets sharing an error code, device model, manufacturer, OS version or ring); the Endpoint Finding deployment_failure_cluster was raised. Payload: deploymentId, findingId, dimension, failed. |
| `DeploymentScheduled` | 1 | endpoints | A valid Deployment plan was scheduled (high-impact plans only after their plan Approval, with an unchanged plan). Payload: deploymentId, versionId, productId, intent, highImpact, ringCount. |
| `DeploymentStarted` | 1 | endpoints | A scheduled Deployment was started (target resolution begins). Payload: deploymentId, versionId, intent, ringCount, highImpact. |
| `DeviceLinked` | 1 | endpoints | A provider-observed Device was linked to an Asset by serial number match or by hand. Payload: deviceId, assetId, method. |
| `DeviceUnlinked` | 1 | endpoints | A Device lost its Asset link: by hand, or because the serial number changed, the serial number is shared by several devices, the device was tombstoned or its manual link collided on revival. Payload: deviceId, assetId, method. |
| `EndpointFindingRaised` | 1 | endpoints | An endpoint finding was raised. Payload: findingId, kind and deviceId, softwarePackageId for the package findings package_hash_mismatch and package_published_after_revoke, or deploymentId for deployment_failure_cluster. |
| `GoodsReceived` | 1 | inventory | A goods receipt was posted. |
| `InitiativeStatusChanged` | 1 | planning | An Initiative changed status (planning started, proposed, approved, approval rejected, activated, held, resumed, completed, cancelled). Payload: initiativeId, operation, status, previousStatus; hold, cancel and rejection also reason. |
| `KnowledgeArticlePublished` | 1 | knowledge | A knowledge article was published. Payload: articleId, audience. |
| `MajorIncidentDeclared` | 1 | service-desk | A Major Incident was declared. Payload: majorIncidentId, status. |
| `MajorIncidentUpdated` | 1 | service-desk | A Major Incident changed status or got a public update. Payload: majorIncidentId, status. |
| `ManagementApplicabilityChanged` | 1 | endpoints | Turaco's expected applicability evaluation meaningfully changed for a managed target. |
| `ManagementAssignmentChanged` | 1 | endpoints | The current assignments of a Management Artifact meaningfully changed during a provider ingestion run: assignments were opened or closed, or a changed assignment was replaced; unchanged re-reads publish nothing. One event per artifact and run. Payload: artifactId, provider, opened, closed. |
| `PresenceEntryChanged` | 1 | presence | A Workforce Presence entry was created, rescheduled, relocated, re-recurred or cancelled. Payload: entryId, userId, operation, from, to. Carries no kind, location or reason. |
| `PurchaseOrderApproved` | 1 | procurement | A purchase order was approved. Payload: orderId, supplierId. |
| `PurchaseOrderReceived` | 1 | procurement | Every line of a purchase order was received. Payload: orderId, supplierId. |
| `PurchaseOrderSent` | 1 | procurement | A purchase order was sent to the supplier. Payload: orderId, supplierId. |
| `RackPlacementChanged` | 1 | infrastructure | An Asset was placed into, moved within or removed from a Rack. Payload: placementId, rackId, assetId, operation (placed, moved, removed), uPosition, heightU, face; removals also reason. |
| `RemoteAccessSessionAuthorized` | 1 | remoteaccess | A Remote Access Session was authorized, at once or by its approver. Payload: sessionId, deviceId, ticketId, provider, status, previousStatus, operation. |
| `RemoteAccessSessionClosed` | 1 | remoteaccess | A Remote Access Session ended: closed, rejected, cancelled, expired or failed. Payload: sessionId, deviceId, ticketId, provider, status, previousStatus, operation, reason. |
| `RemoteAccessSessionLaunched` | 1 | remoteaccess | The technician exchanged the one-time launch handle and the provider client was launched. Payload: sessionId, deviceId, ticketId, provider, status, previousStatus, operation. Never carries the launch link. |
| `RemoteAccessSessionRequested` | 1 | remoteaccess | A Remote Access Session was requested. Payload: sessionId, deviceId, ticketId, provider, approvalRequired. |
| `ReservationFulfilled` | 1 | inventory | A reservation was fulfilled: stock was issued or the reserved asset was assigned. Payload as StockReserved. |
| `ReservationReleased` | 1 | inventory | A reservation was released and its stock or asset is available again. Payload as StockReserved. |
| `RingActivated` | 1 | endpoints | A Deployment Ring became active (first ring after target resolution, next ring after a promotion). Payload: deploymentId, ringId, ringRunId, position. |
| `RingHalted` | 1 | endpoints | A Deployment Ring was halted (failure threshold, assignment failure, kill switch, a person or cancellation). Payload: deploymentId, ringId, ringRunId, reason. |
| `RingPromoted` | 1 | endpoints | A Deployment Ring was promoted (by a person, or the last ring by the engine when the Deployment completes). Payload: deploymentId, ringId, ringRunId, position. |
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
| `SoftwarePackagePublished` | 1 | endpoints | Turaco accepted a Software Package as published into the Management Provider (provider report after a Turaco publish request of an approved version with matching hash; emitted once per package). Payload: packageId, versionId, provider. |
| `SoftwareVersionApprovalRequested` | 1 | endpoints | The approval of a Software Version was requested. Payload: versionId, productId. |
| `SoftwareVersionApprovalRequestedFanOut` | 1 | endpoints (internal) | Continuation of the software.approval_requested notification fan-out. Payload: versionId, after, sourceEventId. |
| `SoftwareVersionApproved` | 1 | endpoints | A Software Version was approved, bound to its installer hash. Payload: versionId, productId, installerSha256. |
| `SoftwareVersionRevoked` | 1 | endpoints | The approval of a Software Version was revoked. Payload: versionId, productId, reason. |
| `StockReserved` | 1 | inventory | Stock or a serialized asset was reserved. Payload: reservationId, kind, productId, status, quantity or assetId, contextType, contextId. |
| `TargetSetChanged` | 1 | endpoints | A Target Set was created, changed or archived. Payload: targetSetId, operation (created|updated|archived), version, allDevices. |
| `TaskAssigned` | 1 | tasks | A task was assigned to a User and/or Team. Payload: taskId, assignedUserId, assignedTeamId, previousUserId, previousTeamId. |
| `TaskBoardArchived` | 1 | tasks | A Task Board was archived by its owner; its shares stop working. Payload: boardId, viewId. |
| `TaskBoardCreated` | 1 | tasks | A Task Board was created. Payload: boardId, viewId. Carries no name and no filter. |
| `TaskCancelled` | 1 | tasks | A task was cancelled, by a person or because the record it belongs to was cancelled. Payload: taskId. |
| `TaskCompleted` | 1 | tasks | A task was completed. Payload: taskId, completedByUserId. |
| `TicketAssigned` | 1 | service-desk | A ticket was assigned to a user. Payload: ticketId, assigneeId. |
| `TicketCommentAdded` | 1 | service-desk | A comment was added to a ticket. Payload: ticketId, commentId, internal, authorId. |
| `TicketCreated` | 1 | service-desk | A ticket was created. Payload: ticketId, reporterId, affectedUserId, queueId. |
| `TicketQueueChanged` | 1 | service-desk | A ticket was moved to another queue and got a new reference; the old one stays as an alias. Payload: ticketId, fromQueueId, toQueueId, oldReference, newReference. |
| `TicketResolved` | 1 | service-desk | A ticket was resolved. Payload: ticketId, reporterId, affectedUserId. |
| `TicketStatusChanged` | 1 | service-desk | A ticket was reopened, closed or cancelled. Payload: ticketId, operation. |
| `UserSynchronized` | 1 | organization | Directory sync created or changed a canonical user. Payload: userId, providerKey, created, changedFields (names only), statusChanged; see docs/integrations/ldap-ad-sync-design.md. |
| `ViewArchived` | 1 | views | A Saved View was archived by its owner or an administrator; its shares stop working. Payload: viewId, resource. |
| `ViewShared` | 1 | views | A Saved View was shared with a User, Team, role or everyone (or the level of a share changed). Payload: viewId, resource, subjectType, subjectId (empty for everyone), level. Carries no name and no filter. |
| `VirtualMachineChanged` | 1 | infrastructure | A Virtual Machine was created, changed, moved to another state or hypervisor, or decommissioned. Payload: virtualMachineId, operation, state, hypervisorAssetId; decommissioning also reason. |
| `VulnerabilityFindingChanged` | 1 | security | A vulnerability finding was created or changed status. Payload: findingId, advisoryId, deviceId, status, confidence, operation, previousStatus and reason when applicable. |
