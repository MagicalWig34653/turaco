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

*Implemented shape (migration 000026, schema `assets`):* `assets.assets` (reference `AST-NNNNNN`, product id, serial number unique per product, asset tag unique, lifecycle status with reason, provisioning status, ownership type `owned|leased|loaned`, supplier/location ids without foreign keys, provenance `source_type/source_id` for goods receipts, purchase/warranty dates, version) and `assets.asset_assignments` (user, team or location assignee, assigned_at/returned_at, one active row per asset).

**Device** specializes Asset with technical identity such as hostname/device type/hardware UUID without duplicating Asset fields.

**DeviceIdentity** stores source identities (serial, BIOS UUID, Intune ID, Agent ID, hardware hash) with first/last seen and confidence, allowing reconciliation into one canonical Device. Ambiguous merges create data-quality findings rather than silent merges.

## Inventory/procurement

**Warehouse** and **StorageLocation** define inventory placement.

*Implemented shape (migration 000027, schema `inventory`):* `warehouses` (optional Organization Location id), `storage_locations`, `stock_balances` (primary key product + storage location; `on_hand`, `reserved`; check constraints `on_hand >= 0` and `0 <= reserved <= on_hand`), `inventory_transactions` (append-only ledger guarded by a trigger; `on_hand_delta`, `reserved_delta`, group id, reservation, origin, actor, correlation id) and `reservations` (kind quantity or asset, one active reservation per asset by partial unique index). Balances are only changed together with ledger rows in the same transaction; tests reconcile the sums.

**InventoryTransaction** is immutable (`goods_receipt`, `reservation`, `release`, `issue`, `return`, `transfer`, `correction`, `disposal`). Current non-serialized stock balance is a materialized/reconciled view of transactions. Serialized Assets use placement + lifecycle instead of fake quantity rows.

**Reservation** may reserve stock quantity or a serialized Asset for a Service Request/Onboarding/Change/etc. It prevents over-reservation atomically.

*Implemented shape (migration 000028, schema `procurement`):* `suppliers` (name unique case-insensitively, account reference), `procurement_requests` (product, quantity, status, optional origin), `purchase_orders` (reference, supplier, status, currency, sent/closed times) and `purchase_order_lines` (line number, product, quantity, unit price in cents, received quantity bounded by the check `received_quantity <= quantity`, optional procurement request with a unique index so a need is on one line only).

**ProcurementRequest** represents acquisition need. **PurchaseOrder** and **PurchaseOrderLine** record supplier, status, quantities/prices and links to originating needs. Goods Receipt reconciles deliveries and may create Assets.

*Implemented shape (migration 000029):* `inventory.goods_receipts` (reference `GR-NNNNNN`, order and supplier ids, delivery note), `goods_receipt_lines` (order line, product, quantity, storage location for stock) and `goods_receipt_assets`; all three are immutable (trigger).

## Catalog/requests

**CatalogItem** is a user-facing offering with visibility/form/workflow/eligibility. It is not Product.

**ServiceRequest** records catalog item, requester/requested-for, lifecycle and fulfillment context. Catalog-specific answers can use schema-defined structured values, but important relationships (requested Device/Product/User/etc.) are promoted to typed references rather than hidden JSON.

**Approval** is shared and can apply to requests/changes/deployments/procurement. Implemented (F3): `approvals.approvals` holds one decision per subject step (`subject_type`/`subject_id`, display label, step index, approver User or Team, excluded Users, immutable decision); `catalog.items` holds the validated definition ([ADR-0025](../decisions/ADR-0025-catalog-form-definitions.md)); `requests.service_requests` holds the human reference (`REQ-YYYY-NNNNNN`), the definition snapshot and the answers, with `request_references` (typed links from answers to Users/Products) and `request_tasks` (fulfillment tasks, mandatory or optional).

## Service desk

**Ticket** holds reference/title/description/reporter/affected user/state/priority/team/assignee/service/timestamps. **Incident** adds disruption semantics and optional affected Device. **MajorIncident** groups widespread impact. **Problem** tracks root/recurring causes and known-error state/data.

A **DeviceContextSnapshot** may capture technical context at ticket creation so history remains meaningful after the endpoint changes.

## Tasks/services/knowledge/change/planning

**Task** is one shared work model with assignee/team/due/state/priority and primary context. Recurrence definitions generate real Task instances. Implemented (F2): `platform.tasks` carries a version, a reason for blocked/cancelled tasks, creator/completer and, for generated tasks, `recurrence_definition_id` plus `scheduled_for` (unique together, so generation is idempotent); `platform.recurring_task_definitions` holds template, schedule rule (frequency, interval, weekday or day of month, local time of day, IANA time zone, start date), `next_run_at` and `last_generated_at`. Tasks reference Organization Users and Teams by id without foreign keys (module boundary).

**Notification** (`platform.notifications`) is one in-app row per recipient with a category, parameters, an optional link and read state; **NotificationDelivery** (`platform.notification_deliveries`) is the per-channel send state; `platform.notification_preferences` stores opt-outs. **Briefing Item** (`briefing.items`) is the manual editorial item with status, severity and validity.

**Service** stores owners/support team/criticality/status and structured dependencies to infrastructure/vendor resources.

**KnowledgeArticle** has revision/publication/visibility/review lifecycle and relationships to software/services/problems/devices/etc. Procedures and Runbook Definitions can instantiate Runbook Executions/tasks.

**Change** records planned modification, risk, schedule, rollback and affected resources. **Initiative** groups longer modernization work using existing Changes, Tasks and Procurement rather than duplicating them. Implemented (F7d, schema `planning`, migration `000046`): `planning.initiatives` (reference `INI-xxxxxx` from a sequence, title, bounded goal, `owner_user_id`, status with per-status CHECKs, `status_reason` code, `target_date`, `approval_id` of the current or last proposal, `proposed_by`, `editors`, `approved_at`/`activated_at`/`closed_at`, version), `planning.milestones` (title, `due_date`, `position`, `done_at`/`done_by`, removal with reason code; at most 50 live per Initiative) and the append-only `planning.initiative_transitions` (update/delete/truncate refused by trigger, foreign keys `ON DELETE RESTRICT`). Included Changes, Tasks, Procurement Requests and Services are platform Relationships `initiative INCLUDES ...` (owner `planning`, at most 200 per Initiative); Users are referenced by id without foreign keys.

## Infrastructure/network

Site→Building→Room→Rack models physical topology. Physical infrastructure devices are normal Assets/Devices with RackPlacement and specialized metadata, not a parallel device database. VM is an infrastructure resource related to hypervisor/service/IP.

Implementation (F7a, schema `infrastructure`): `buildings` (references the Site Location by id), `rooms`, `racks`, `rack_placements` (Asset by id, history rows with `removed_at`, `previous_placement_id` for moves), `rack_unit_occupancy` (one row per occupied unit; its primary key `(rack_id, face, u)` is the database guarantee against overlaps) and `virtual_machines` (hypervisor Asset by id). `VM RUNS_ON Asset` is derived as a Relationship row (F7b) from the `hypervisor_asset_id` column, which stays the source of truth. See the [F7 design](../product/f7-infrastructure-change-design.md#slice-1-status).

Native Network/IPAM may model VRF, VLAN, Prefix, IPAddress and NetworkInterface, or may integrate NetBox. That implementation decision requires an ADR.

## Endpoint/software/security

**EndpointManagementIdentity** maps canonical Device to provider external IDs.

**DeviceObservation** captures source/observed-at categories such as hardware, OS, compliance and management health. Current HardwareProfile/OperatingSystemState preserve provenance as needed.

In the implementation (schema `endpoints`) a Device is `endpoints.devices` (provider identity, optional Asset link by id without foreign key, tombstone instead of delete), `endpoints.device_observation_history` is append-only and written only when normalized values change, installed software is `endpoints.software_installations` with raw name/version kept next to the optional normalized `software_products`/`software_aliases`.

The management model (F6 slice 2) adds `endpoints.management_artifacts` and `management_filters` (provider identity, tombstone, revision), `management_assignments` (interval history: `valid_from`/`valid_until`, denormalized `provider`, one current row per artifact and provider assignment id; the group target is the Directory Group's provider external id without foreign key), `management_observations` (one current row per artifact and device, `retired_at` when a Complete snapshot stops reporting it) with append-only `management_observation_history`, and `device_group_memberships` (observed intervals, one current row per device and group), plus `provider_sync_state` (last completed sync, for the sync cooldown).

**ManagementArtifact** is a normalized provider-managed assignable object such as an application, configuration profile, compliance policy, endpoint-security policy, script or remediation. It retains provider/external identity and may link to canonical SoftwareProduct where appropriate.

**ManagementAssignment** records provider targeting intent: artifact, target scope, include/exclude semantics, application intent where relevant, optional ManagementFilter, provider IDs and revision/freshness.

**ManagementApplicability** is a recomputable/read model that evaluates an Assignment against a User/Device as `applicable`, `excluded`, `not_applicable` or `unknown`, with confidence, reason codes and input freshness. It is not authoritative provider state. **AssignmentPath** is the explainability projection for that evaluation.

**ManagementObservation** stores provider-reported artifact/target outcomes (for example applied, conflict, error, installed, pending) with source and observed time. Raw provider status may be retained alongside a normalized category for troubleshooting.

**SoftwareProduct** + **SoftwareAlias** normalize discovery names. **SoftwareInstallation** is observed Endpoint↔Version with source/first/last seen. **SoftwareAssignment** is the software-focused projection of a ManagementAssignment (provider-configured targeting); it references that ManagementAssignment rather than becoming a second provider-assignment truth and never represents Turaco rollout intent.

**DesiredSoftwareState** defines installed/minimum/exact/absent outcomes. **Deployment** targets a Target/DynamicGroup; resolved **DeploymentTarget** rows are frozen historically even when group membership changes; retries become immutable attempts.

*Implemented in F9 G1 ([ADR-0027](../decisions/ADR-0027-software-management-providers.md), migration `000050`):* **SoftwareApprovalStatus** (`approval_status`, `approval_reason`, `version`) on SoftwareProduct with append-only `software_product_transitions`; **SoftwareVersion** (`endpoints.software_versions`) is an immutable binding (product, version string, installer SHA-256, https URL, publisher, install command and detection rule as bounded text plus database-checked SHA-256, binding hash unique per product) carrying its version approval state, with append-only decisions in `software_version_approvals`. **SoftwarePackage** (`endpoints.software_packages`, one per provider and version) holds the provider and provider package id (unique per provider), reported installer hash, status, source/observed/last-synced times, publish request and the resulting ManagementArtifact (linked by external id after the management sync); its state changes are appended to `software_package_observations`. The provider reference is a platform external reference (`platform.external_references`, system = provider, entity type `software_package`, set once); provider and provider package id are mirrored on the row for uniqueness and the package synchronization. Packages also carry `package_attempt`/`publish_attempt` (part of the provider operation keys) and `published_at` (set once, when Turaco accepted the publication); one Management Artifact external id belongs to at most one package. `endpoints.findings` gained `software_package_id` (exactly one subject per finding) for `package_hash_mismatch` and `package_published_after_revoke`.

*Implemented in F9 G2 (migration `000051`):* **TargetSet** (`endpoints.target_sets`, reference `TS-`, name unique case-insensitively among non-archived sets, owner, validated canonical JSON definition, derived `all_devices` and `high_impact_reason` (snapshot), `updated_by`, archive instead of delete) and Deployment planning: **Deployment** (`endpoints.deployments`, reference `DEP-`) carries the Desired Software State as its Software Version and intent (`install|update|uninstall`, `supersede`), the planning status, `high_impact` (snapshot while draft, recomputed by lifecycle steps), the plan Approval id, the plan hash bound at submission/scheduling, editors and owner; **DeploymentRing** (`endpoints.deployment_rings`) holds position (1 = pilot, unique per Deployment, deferred), Target Set, gate configuration (approval required, success threshold, minimum fresh evidence, soak minutes, Change id or `no_window_required` for the pilot only) and `max_targets` (≤ 5000); `deployment_transitions` is append-only. A ring is GATED_BY a Change, referenced by id (`change_id`) and read through `changes/public`; that the Change concerns this software is not enforced. Migration `000052` adds the partial index `devices_live_provider_idx` for live-Device scans and counts.

*Implemented in F9 G3 (migration `000054`):* `deployments` gains `started_by/started_at/finished_at` and the execution statuses; **DeploymentRingRun** (`endpoints.deployment_ring_runs`, one per ring, created at target resolution because the ring definition is frozen by trigger) holds the ring execution state, the Turaco-owned provider group id (`turaco-ring-<ringId>`), assignment request/clear times and the optional promotion Approval (`deployment_ring_transitions` is append-only); **DeploymentTarget** (`endpoints.deployment_targets`, unique per Deployment and Device, with `read_back_at`, `expires_at`, `evidence_observed_at`; `deployment_target_transitions` append-only) is the frozen snapshot of the ring's Devices; **DeploymentAttempt** (`endpoints.deployment_attempts`, immutable, unique `operation_id`) records every Management Assignment Writer call. The finding kinds `deployment_evidence_conflict` and `deployment_clear_failed` (clearing a ring assignment failed 10 times; raised on the first target Device of the ring, detail with Deployment and ring ids) are Device findings. Review additions: `deployments.last_ticked_at` (round robin) and `scheduled_targets` (evaluated target total at scheduling); `deployment_ring_runs` pins the written artifact (`management_artifact_id`, `management_artifact_external_id`) and carries `clear_requested_at`, `attempt_base` and `retry_count`; attempts start as `in_flight` and end `accepted`, `transient_error`, `permanent_error` or `interrupted` (`finished_at`; finishing is the only change allowed); `management_observations.observed_at_provider` says whether `observed_at` is the provider's timestamp; composite foreign keys tie ring runs, targets, attempts and the histories to their Deployment. Per-status CHECKs and transition triggers hold the state machines in the database; histories reject update, delete and truncate.

*Implemented in F9 G4 (migration `000055`):* `deployments.create_tasks` (default true) switches the follow-up Tasks and notifications; `endpoints.findings` gets a third subject (`deployment_id`, with `cluster_key`) for the Turaco-derived kind `deployment_failure_cluster` (a finding has exactly one subject; at most one open finding per Deployment and cluster key; the Device finding lists do not show it, the report does); `endpoints.deployment_followups` records the follow-up Task per Deployment, reason code and ring (unique, append-only, `task_id` without a foreign key across modules). Reports, rollout lists and the security context are derived reads; nothing is stored for them.

*Implemented in F10 R-A (migration 000056, schema `remoteaccess`, [ADR-0026](../decisions/ADR-0026-remote-access-providers.md)):* **RemoteAccessSession** (`sessions`, reference `RAS-nnnnnn`) references Device, Ticket (required) and initiating User by id; keeps Turaco-authorized facts, consent outcome (`granted|declined|not_required|unknown`) and provider-observed facts (`observed_*`, source, observed time; null = unknown) in separate columns, with per-status CHECKs, one open session per Device (partial unique index on `device_id`) and an append-only `session_transitions` table (UPDATE, DELETE and TRUNCATE are refused by triggers). **PeerMapping** (`peer_mappings`) maps a Device to a provider peer id; closed rows stay as history. It is deliberately not a platform external reference: those keep a set-once external id and a sync state, while a peer mapping is remapped by administrators. **LaunchHandle** (`launch_handles`) holds the SHA-256 of a one-time token, its user, expiry and use time. **ProviderSessionRecord** (`provider_session_records`) keeps a provider's connection record (provider, provider session id, peer id, times, bounded operator identity, source) with at most one attributed session and a flag (`unattributed|duplicate|after_close`); `request_attempts` backs the rate limit. Mappings, handles, sessions and records are protected by UPDATE, DELETE and TRUNCATE triggers (only closing, using and status changes are allowed). *Planned:* **UnattendedAccessPolicy**, a typed record naming the Devices (by Endpoint-exposed attributes) eligible for unattended sessions.

*Backend implemented (schema `presence`, migration 000057; [ADR-0028](../decisions/ADR-0028-workforce-presence.md)):* **PresenceEntry** (Workforce Presence module) belongs to one User: period, kind (`location` with a Location reference, `remote`, `travelling`, `unavailable`), optional recurrence rule, source, observed/synced time; no absence reason. External entries are interval history; manual entries are cancelled, not deleted, until retention removes them. **TeamCoverageRequirement** (`team_coverage_minimums`) stores an optional minimum per Team. `presence.settings` holds the opt-in switch, the recorded DPIA and works-council dates and the retention. External source tables (`source_configs`, `source_runs`) arrive with P-C. Operational Availability and Team Coverage are read models.

*Backend implemented (schema `ai`, migration 000058; [ADR-0029](../decisions/ADR-0029-turaco-ai.md), [F12 design](../product/f12-turaco-ai-design.md#data-model-sketch-migration-000058-ai)):* owned by `platform/ai`, no business data. **AiProvider** (`ai.providers`): kind (`fake`, `openai_compatible`), endpoint, model, `local`, allowed data classes, DPA date / no-training / region for external providers, `secret_ref` (a deployment secret file name, never the secret), at most one enabled. **AiSettings** (`ai.settings`, one row): runtime switch, transcript retention, caps. **AiSession** (`ai.sessions`): transient server-held conversation (hashed opaque id, tenant, User, sign-in session, transcript, resource scope, `version`, `busy_until`, expiry). **Usage counters** (`ai.usage`, `ai.usage_hours`, `ai.installation_usage`): counts and token reservations only. **Retained transcript** (`ai.conversations`, `ai.messages`): user and assistant text, only while retention is on, with `expires_at`. `platform.audit_events` gained nullable `via`, `tenant_id` and `ai_proposal_id` (A15). AI Proposals (`ai.proposals`) arrive with A-C.

*Planned ([ADR-0029](../decisions/ADR-0029-turaco-ai.md)):* the AI runtime stores no domain data of its own beyond audit of tool calls and short-lived AI Proposals; prompts and responses are not retained by default.

**SecurityAdvisory** (F8a backend, schema `security`) has an `ADV-` reference, source/external identity unique per source, bounded plain-text title/summary, source severity, published/modified times, HTTPS source URL, status/reason, optimistic version and match freshness/revision/truncation fields. **AdvisoryCriterion** identifies a canonical SoftwareProduct or an unnormalized product name/publisher, optional OS platform and ordered version rules (`introduced`, `fixed`, `lt`, `le`, `eq`); normalization is recorded as `matched|unmatched`. Import is idempotent by source/external id.

**VulnerabilityFinding** has a `VUL-` reference and unique `(advisory, Device, SoftwareProduct)` identity. It records installed version, Turaco-derived confidence (`probable` or `potential`, never automatically `confirmed`), state/reason, first/last observed times, remediated time and version. `risk_accepted` requires accepting User, acceptance time, reason code and review date within twelve months. Advisory and Finding transition rows have immutable histories guarded against update, delete and truncate; per-status CHECK constraints guard the current rows. Device and SoftwareProduct ids are external references to the Endpoints module, accessed through `endpoints/public`, with no cross-module foreign key. A newer Endpoint observation, or a Device/installation tombstone, is required for automatic remediation.

## Platform: module switches
*Implemented (schema `platform`, migration 000059; [ADR-0032](../decisions/ADR-0032-module-switches.md)):* **ModuleSwitch** (`platform.module_switches`) has one row per optional module whose switch was ever changed: `module_key` (the code catalog is the list of modules; no row means the catalog default and version 0), `enabled`, `version`, `reason_code`, `updated_by` (User id, no foreign key like other platform audit references), `updated_at`. The upgrade migration seeds `presence` and `ai` from their runtime settings. A switch owns no business data and deletes none.

## Shared relationship model

Generic `Relationship(source_type, source_id, type, target_type, target_id, validity, metadata)` supports cross-domain links such as `User USES Device`, `Service DEPENDS_ON VM`, `VM RUNS_ON Device`, `Ticket AFFECTS Service`.

Implementation (F7b, `platform/relationships`, table `platform.relationships`): rows carry `confidence` (`declared|derived|observed`), `valid_from`/`valid_until` with an `end_reason` code, `created_by` and a `source` label; modules register the allowed `(source_type, type, target_type)` triples with an owning module in a Go registry (unknown triples and non-owners are rejected; only the owner links and unlinks a triple), one current row per triple is guaranteed by a partial unique index, and traversal is bounded (depth 6, 500 nodes, truncation flags). Registered today: `Service DEPENDS_ON service|vm|asset|location` and `VM RUNS_ON Asset` (derived from the VM's hypervisor). `change AFFECTS service|vm|asset|location` is registered by Changes (F7c, owner `changes`, confidence `declared`); `Ticket AFFECTS Service` is not registered yet. Services live in schema `services` (`services.services`: reference, name, description, owner User/Team and support Team by id, criticality, status, version); their dependencies are Relationships, not columns.

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

**F8b remediation tracking:** Security creates ordinary `platform.tasks` with `context_type` `security_advisory` or `security_finding` (at most 50 per object). It owns the `advisory REMEDIATED_BY change` Relationship triple (at most 25 current links per Advisory). Task status does not change Finding status. The daily risk-review reminder uses notification dedupe keys per Finding, review date, reminder threshold and recipient, so no new table or migration is required.
