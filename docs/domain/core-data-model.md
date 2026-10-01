# Core Data Model v0.1

**Status:** Conceptual model; not a final SQL schema.

## Principles

Every persistent entity has an immutable internal UUIDv7. Human references (`INC-2026-...`, `AST-...`) are separate. Every entity has exactly one owning module. Cross-domain references do not create duplicate authoritative copies. Current state and historical snapshots/relationships are separate concerns.

## Organization

**User**: display name, names, primary email, status, employee number, department/cost center/location/manager references.

**ExternalIdentity**: `user_id`, provider, external subject, username/DN, enabled, last seen. `(provider, external_subject)` identifies one active identity.

**TeamMembership** records User↔Team role and validity/source. Department may be hierarchical. Location is organizational; Site is physical.

**DirectoryGroup** represents a group observed from AD/Entra or another directory provider and is distinct from an operational Team. **DirectoryGroupMembership** records observed User/Device membership with provider identity, source and freshness. Turaco does not silently reinterpret provider dynamic-group rules; it may consume observed membership and only evaluate rules locally when explicitly supported.

*Implemented shape (migrations 000006, 000009):* `directory_groups` is unique per `(provider_key, external_id)` and carries `first_observed_at`, `last_observed_at` and `deleted_observed_at` (soft "no longer observed", never a hard delete). `external_id` must be a stable opaque provider identifier (for example objectGUID / Entra object ID), never a distinguished name. `directory_group_memberships` holds **User** members only (Device membership is added when Assets/Endpoint exist) as **interval history**: one row per continuous observed interval (`observed_from`, `last_observed_at`, `observed_until`; `observed_until IS NULL` = currently observed, at most one open row per group/user). `directory_group_nesting` records observed direct Group→Group edges with the same interval shape; transitive membership is not expanded. External Identities carry `last_seen_at`, `deleted_observed_at` and an attributes hash for change detection. `users.status_source` (`platform`/`directory`) records which side last set the status; directory sync only reactivates users it deactivated. `directory_sync_runs` records each synchronization run with outcome, counts and conflicts.

## Products

Dedicated Products module is recommended because Catalog, Inventory, Procurement and Assets depend on the same definitions.

**Product**: name, manufacturer, category, manufacturer/internal part numbers, serialized?, stock-managed?, asset-managed?, active.

**ProductVariant** is optional and only used where a variant materially changes ordering/inventory. Avoid overlapping Product/Model/Variant/Article concepts.

## Assets/devices

**Asset**: reference, product, serial, asset tag, lifecycle, ownership type, purchase/warranty/supplier/current organizational location.

**AssetAssignment**: historical assignment to User/Team/Location/resource with validity/source. Exclusive primary assignments enforce domain rules.

**Device** specializes Asset with technical identity such as hostname/device type/hardware UUID without duplicating Asset fields.

**DeviceIdentity** stores source identities (serial, BIOS UUID, Intune ID, Agent ID, hardware hash) with first/last seen and confidence, allowing reconciliation into one canonical Device. Ambiguous merges create data-quality findings rather than silent merges.

## Inventory/procurement

**Warehouse** and **StorageLocation** define inventory placement.

**InventoryTransaction** is immutable (`goods_receipt`, `reservation`, `release`, `issue`, `return`, `transfer`, `correction`, `disposal`). Current non-serialized stock balance is a materialized/reconciled view of transactions. Serialized Assets use placement + lifecycle instead of fake quantity rows.

**Reservation** may reserve stock quantity or a serialized Asset for a Service Request/Onboarding/Change/etc. It prevents over-reservation atomically.

**ProcurementRequest** represents acquisition need. **PurchaseOrder** and **PurchaseOrderLine** record supplier, status, quantities/prices and links to originating needs. Goods Receipt reconciles deliveries and may create Assets.

## Catalog/requests

**CatalogItem** is a user-facing offering with visibility/form/workflow/eligibility. It is not Product.

**ServiceRequest** records catalog item, requester/requested-for, lifecycle and fulfillment context. Catalog-specific answers can use schema-defined structured values, but important relationships (requested Device/Product/User/etc.) are promoted to typed references rather than hidden JSON.

**Approval** is shared and can apply to requests/changes/deployments/procurement.

## Service desk

**Ticket** holds reference/title/description/reporter/affected user/state/priority/team/assignee/service/timestamps. **Incident** adds disruption semantics and optional affected Device. **MajorIncident** groups widespread impact. **Problem** tracks root/recurring causes and known-error state/data.

A **DeviceContextSnapshot** may capture technical context at ticket creation so history remains meaningful after the endpoint changes.

## Tasks/services/knowledge/change/planning

**Task** is one shared work model with assignee/team/due/state/priority and primary context. Recurrence definitions generate real Task instances.

**Service** stores owners/support team/criticality/status and structured dependencies to infrastructure/vendor resources.

**KnowledgeArticle** has revision/publication/visibility/review lifecycle and relationships to software/services/problems/devices/etc. Procedures and Runbook Definitions can instantiate Runbook Executions/tasks.

**Change** records planned modification, risk, schedule, rollback and affected resources. **Initiative** groups longer modernization work using existing Changes, Tasks and Procurement rather than duplicating them.

## Infrastructure/network

Site→Building→Room→Rack models physical topology. Physical infrastructure devices are normal Assets/Devices with RackPlacement and specialized metadata, not a parallel device database. VM is an infrastructure resource related to hypervisor/service/IP.

Native Network/IPAM may model VRF, VLAN, Prefix, IPAddress and NetworkInterface, or may integrate NetBox. That implementation decision requires an ADR.

## Endpoint/software/security

**EndpointManagementIdentity** maps canonical Device to provider external IDs.

**DeviceObservation** captures source/observed-at categories such as hardware, OS, compliance and management health. Current HardwareProfile/OperatingSystemState preserve provenance as needed.

**ManagementArtifact** is a normalized provider-managed assignable object such as an application, configuration profile, compliance policy, endpoint-security policy, script or remediation. It retains provider/external identity and may link to canonical SoftwareProduct where appropriate.

**ManagementAssignment** records provider targeting intent: artifact, target scope, include/exclude semantics, application intent where relevant, optional ManagementFilter, provider IDs and revision/freshness.

**ManagementApplicability** is a recomputable/read model that evaluates an Assignment against a User/Device as `applicable`, `excluded`, `not_applicable` or `unknown`, with confidence, reason codes and input freshness. It is not authoritative provider state. **AssignmentPath** is the explainability projection for that evaluation.

**ManagementObservation** stores provider-reported artifact/target outcomes (for example applied, conflict, error, installed, pending) with source and observed time. Raw provider status may be retained alongside a normalized category for troubleshooting.

**SoftwareProduct** + **SoftwareAlias** normalize discovery names. **SoftwareInstallation** is observed Endpoint↔Version with source/first/last seen. **SoftwareAssignment** is the software-focused projection of platform/provider intent and, when sourced from an external management provider, references the relevant ManagementAssignment rather than becoming a second provider-assignment truth.

**DesiredSoftwareState** defines installed/minimum/exact/absent outcomes. **Deployment** targets a Target/DynamicGroup; resolved **DeploymentTarget** rows are frozen historically even when group membership changes; retries become immutable attempts.

**SecurityAdvisory** relates to CVE/software/OS/vendor. **VulnerabilityFinding** links advisory to a concrete resource with confidence (`confirmed`, `probable`, `potential`, `unknown`) and remediation state.

## Shared relationship model

Generic `Relationship(source_type, source_id, type, target_type, target_id, validity, metadata)` supports cross-domain links such as `User USES Device`, `Service DEPENDS_ON VM`, `VM RUNS_ON Device`, `Ticket AFFECTS Service`.

Do not replace important domain-specific relationships (AssetAssignment, Reservation, RackPlacement) with generic Relationship when they carry invariants/history.

## Provenance and source of truth

Examples:
- User.department → AD/LDAP initially
- Asset.lifecycle / AssetAssignment → Platform
- Device.compliance → Intune observation
- Management Assignment/Observation → Management Provider (Intune initially); Turaco owns derived applicability/explainability read models
- Agent status → Endpoint Agent
- SoftwareInstallation → observation source

Each synchronized field/domain defines direction and conflict mode (`external_wins`, `platform_wins`, `manual_review`, `merge`, `observation_only`).

## Invariants

- available stock never becomes negative;
- one serialized Asset cannot have multiple active exclusive reservations;
- primary exclusive endpoint assignment cannot have multiple active users unless shared-use mode explicitly allows it;
- disposed assets cannot be reassigned without an explicit exceptional recovery flow;
- processed external messages/deployments remain idempotent;
- Audit Events are not mutable through normal APIs;
- one external subject/provider cannot map to multiple active Users;
- expected Management Applicability must never overwrite or masquerade as provider-observed result.

## Data quality

Explicit findings should surface duplicate-device candidates, stale management records, unknown software mappings, double IP assignment, departed-user assignments and other reconciliation problems as operational work instead of hidden sync warnings.
