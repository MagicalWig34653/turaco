# Domain Glossary

**Status:** Canonical terminology baseline

Source code, APIs, DB schemas, events and technical documentation use these English terms.

## Organization
- **User** — a person known to the platform. Not identical to an LDAP/Entra account.
- **External Identity** — a User representation in AD/LDAP/OIDC/Entra.
- **Employee** — business classification of a User; not a parallel identity model.
- **Team** — operational responsibility group.
- **Directory Group** — group observed from an external directory/identity provider (for example Entra/AD) and used for access/management targeting; not the same as Team.
- **Directory Group Membership** — observed User/Device membership in a Directory Group with source/freshness, kept as interval history; dynamic-group rule evaluation stays with the provider unless explicitly supported.
- **Directory Group Nesting** — observed direct membership of one Directory Group in another, kept as interval history; transitive membership is derived, not stored.
- **Directory Sync Run** — one execution of directory synchronization for one provider, with outcome, counts and conflicts; the provenance record for directory observations.
- **Department** — organizational business unit.
- **Location** — organizational/support/inventory location, distinct from physical infrastructure Site.
- **Cost Center** — accounting/organizational allocation.

## Products, assets and devices
- **Product** — generic catalog/procurement/inventory item, e.g. Dell Latitude 7450.
- **Product Variant** — optional materially distinct purchasable/stocked variant.
- **Manufacturer** — maker of a product; distinct from Supplier.
- **Asset** — uniquely tracked instance with lifecycle, e.g. serial ABC123.
- **Device** — technical Asset with device characteristics.
- **Endpoint** — Device eligible for endpoint-management capabilities.
- **Peripheral** — product/asset used with another device; may be serialized or quantity-based.
- **Asset Tag** — organization-assigned identifier, distinct from manufacturer Serial Number.
- **Asset Reference** — Turaco's own human identifier of an Asset (`AST-000001`), distinct from serial number and asset tag.

## Inventory/procurement
- **Warehouse** — logical inventory boundary.
- **Storage Location** — specific place within a Warehouse.
- **Stock Item/Balance** — current quantity view for non-serialized products.
- **Available stock** — on-hand quantity minus reserved quantity of a Product at a Storage Location.
- **Inventory Transaction** — immutable stock movement/correction record.
- **Reservation** — temporary allocation of stock/serialized Asset for future use.
- **Goods Receipt** — process/record of delivered goods entering inventory.
- **Procurement Request** — internal need to acquire something.
- **Purchase Order** — formal order against Supplier.
- **Supplier** — organization goods/services are purchased from.

## Service management
- **Field Catalog** — module-declared list fields and their caller-visible filter, sort and search operators; SQL expressions stay server-owned.
- **Filter AST** — versioned tree of conditions and AND/OR groups evaluated under the caller's resource scope.
- **Saved View** — stored query intent (Filter AST, sort, columns) of one resource with an owner and a version; never results and never a grant of row access: it always runs as the viewer, through the owning module's query endpoint (`platform/views`, F13 Q-B).
- **View Share** — explicit, revocable access to a Saved View for a User, Team, role or everyone at level `use` or `edit`; resolved at read time; grants no data access.
- **Board** — planned Task presentation over a Saved View with columns; card moves use Task lifecycle operations.
- **Pin** — personal sidebar placement of a Saved View (group, position, hidden).
- **Pin Rule** — Team- or role-wide Pin created with `views.pin_for_groups`; shows the View only to members who may use it and grants no access; a member can hide it for themselves.
- **Ticket Queue** — a Service Desk desk (IT, HR, Facility) with a frozen key and prefix, its own committed number counter, explicit grants (`create`, `view`, `work`, `manage`) and a visibility (`internal`, `public`); every Ticket belongs to exactly one. Distinct from a Saved View and from the routing Team (`routing_team`), which only hints who handles a Ticket inside its desk.
- **Reference alias** — an earlier display number of a Ticket (it got a new one when it moved to another Queue); always resolves to the Ticket, never reissued, shown only to callers who may know the Queue it was issued from.
- **System View** — a built-in, per-caller View definition offered by a module (Tickets: My open tickets, Unassigned, one per viewed Queue); cannot be edited or shared, runs through the views engine as the viewer.
- **Work Item source** — a module's contribution to My Work (`platform/workitems`): authorizes every item and its count for the caller, returns items in the shared order and runs only while its module is on.
- **Ticket** — generic tracked support record; employee UI should use friendlier language.
- **Incident** — unplanned interruption/degradation/malfunction.
- **Service Request** — structured request to provide/change/grant something.
- **Major Incident** — significant Incident affecting many users/services.
- **Problem** — tracked underlying/repeating cause.
- **Known Error** — Problem whose cause/workaround is sufficiently understood; prefer Problem state/data over a duplicate top-level object.
- **Service** — IT-delivered capability, e.g. SAP, VPN, Corporate Wi-Fi. Implemented in F7b as a record with owner, support team, criticality (`low|medium|high|critical`) and status (`operational|degraded|outage|planned|retired`); its dependencies are Relationships.
- **Service Catalog** — collection of requestable IT offerings.
- **Catalog Item** — user-facing requestable offering; not necessarily a Product.

## Work/change/planning
- **Briefing Feed Entry** — a computed, permission-filtered presentation of an authoritative record or health count. It is not a copy of that record; manual Briefing Items remain editorial records.
- **Task** — concrete unit of work shared across the platform.
- **Remediation Task** — an ordinary Task attached to a Security Advisory or Vulnerability Finding, with a fixed reference-based title; completing it does not remediate a Finding.
- **Residual Risk** — Turaco-derived label (`none|low|medium|high`) from an Advisory’s severity and live probable/potential Findings, including accepted risks.
- **Assignment** — responsibility association between work and User/Team.
- **Approval** — recorded decision required before a flow continues.
- **My Work** — consolidated operational view across tasks/tickets/requests/changes/deployments.
- **Initiative** — medium/long-term modernization effort (module `planning`, reference `INI-xxxxxx`): title, goal, owner, target date, Milestones, an Approval before work starts, and the Changes, Tasks, Procurement Requests and Services it includes as Relationships (`initiative INCLUDES ...`, no copies). Lifecycle: [state machines](state-machines.md#initiative). The owner, creator, proposer and editors can never approve it.
- **Milestone** — dated entry of an Initiative (title, due date, position, done or open); removal keeps the row with a reason code.
- **Change** — planned modification to the IT environment (module `changes`, reference `CHG-xxxxxx`): kind `standard|normal|emergency`, risk `low|medium|high`, rollback plan, one maintenance window, affected resources (Relationships `change AFFECTS service|vm|asset|location`), an Approval when the policy requires one, and execution Tasks. Lifecycle: [state machines](state-machines.md#change). Requester (creates), owner (the assignee who executes) and approver are separate roles; the requester and everybody who edited, submitted or assessed a Change can never approve it.
- **Maintenance Window** — time period in which defined operational changes may occur. Implemented as the `window_start`/`window_end` fields of a Change (at most 30 days); there is no scheduling subsystem, the maintenance calendar (F7d, `GET /api/v1/maintenance-calendar`) is a read model over approved, scheduled and in-progress Changes.

## Infrastructure/network
- **Site** — physical geographic site; an Organization Location that has Buildings.
- **Building** — physical building at a Site (an Organization Location); contains Rooms.
- **Room** — room in a Building (optional floor); contains Racks.
- **Rack** — rack in a Room with a fixed height in units (U, 1-60).
- **Rack Placement** — placement of an Asset in a Rack: first unit, height in U and face (`front|rear`). Removed or moved placements stay as history; an Asset has at most one active placement and no unit of a face is occupied twice.
- **Virtual Machine** — virtual compute instance entered by hand (name, state `running|stopped|unknown|decommissioned`, vCPU, memory, management address, optional hypervisor Asset); not necessarily a physical Asset.
- **Hypervisor** — virtualization host, normally represented by an Asset/Device role.
- **Interface** — network interface belonging to Device/VM.
- **VLAN / VRF / Prefix / IP Address** — canonical network/IPAM concepts if natively modeled.

## Endpoint/software
- **Endpoint Finding** — data-quality observation about a Device or its software (for example no matching Asset); distinct from Security Findings and provider-reported errors. Kinds: ingestion-derived (`no_asset_match`, `serial_conflict`, `duplicate_device`, `unmatched_software`), provider-reported (`provider_reported_error`) and Turaco-derived from management data (`assignment_ineffective`); the two management kinds are visible only with management access.
- **Software Product** — canonical normalized software identity.
- **Software Alias** — discovery/provider name mapped to canonical Software Product.
- **Software Version** — known release/version; preserve raw source value. For approval and packaging (F9 G1) a registered Software Version is one immutable binding of product, version string, installer SHA-256, https installer URL, publisher, install command and detection rule; any changed value is a new Software Version.
- **Software Version Approval** — Turaco's decision that one Software Version binding may be packaged, published and (later) deployed: `registered → pending → approved | rejected`, `approved → revoked`, bound to the installer SHA-256 and the binding hash. The person who registered or requested a version cannot approve it. Not an Approval record. [State machine](state-machines.md#software-approval-status).
- **Software Installation** — observed installation on Endpoint; observation, not intent.
- **Software Assignment** — software-focused projection of a Management Assignment (provider-configured targeting); never Turaco rollout intent and does not prove installation.
- **Desired State** — Turaco's intended configuration/software outcome; reached through a Deployment and distinct from the provider's Management Assignment.
- **Desired Software State** *(F9 G2)* — the Desired State of one Software Version: an approved version with an intent (`install`, `update`, `uninstall`, optionally superseding earlier versions) for the Target Sets of a Deployment's rings. Stored as the version and intent of the Deployment, not as a separate record.
- **Observed State** — latest known state with source/freshness.
- **Deployment** — managed operation attempting to establish Desired State on Targets. F9 G2 implements Deployment planning (draft, plan Approval for high-impact plans, scheduled, cancelled); execution is G3.
- **Management Assignment Writer** *(F9 G3)* — the only write port of the endpoint provider: sets or clears the assignment of a published Management Artifact to a Turaco-owned ring group, idempotent per operation id. A write counts as Assigned only after the normal management synchronization reads it back. Disabled by default (`SOFTWARE_DEPLOY_WRITE`).
- **Deployment Target/Result/Attempt** — concrete resolved endpoint and execution history. For provider-executed deployments the result is derived from fresh Management Observations (Software Installation corroborates), never from a package being published.
- **Deployment Ring** *(ADR-0027; definition implemented in F9 G2, execution in F9 G3)* — ordered rollout stage of a Deployment (position 1 is the pilot) targeting one Target Set, with its Promotion Gate configuration and a target cap (default and maximum 5000). Only the pilot may run without a Maintenance Window; every other ring names an approved or scheduled Change. Execution state lives in a ring run; resolved Targets are snapshotted when the Deployment starts, and a Device is in the first ring that selects it only. States: [state machines](state-machines.md#deployment).
- **Failure Correlation** *(F9 G4)* — Turaco's own analysis of a Deployment's failed and expired targets: grouped by error code (the provider's raw status), device model, manufacturer, OS version and ring, a group of at least three targets that covers at least 20 % of all failures or at least 50 % of its own targets is a *failure cluster* and raises the Turaco-derived Endpoint Finding `deployment_failure_cluster` (subject: the Deployment). It is distinct from the provider's own `provider_reported_error` and from `deployment_evidence_conflict`; it resolves by itself when the cluster disappears or the Deployment ends. Follow-up: one Task (context `deployment`) for the Deployment owner per Deployment, reason and ring, and a `deployment.attention` notification, switched by the Deployment's `create_tasks` flag.
- **Promotion Gate** *(F9 G2 configuration; evaluation planned for G3)* — the conditions a ring must meet before the next ring starts: optional Approval, success threshold on fresh evidence (1–100 %, optional minimum share of fresh evidence), soak time (at most 30 days) and the Maintenance Window of a Change.
- **Software Management Provider** *(ADR-0027; port implemented in F9 G1, no real client yet)* — external system that packages a Software Version and publishes or updates it in a Management Provider (IntuneGet → Intune first). It manages no devices and owns no targeting or device results.
- **Software Package** *(F9 G1)* — provider-built deployable package of one approved Software Version (provider reference, installer hash as reported by the provider, publish status with source and freshness) linked to the Management Artifact it became once published and synchronized. Publishing is never assignment, installation or deployment success.
- **Software Approval Status** *(F9 G1)* — Turaco's decision whether a Software Product may be used (`candidate`, `approved`, `deprecated`, `retired`, `blocked`); deployable versions additionally need a version approval bound to the installer hash. The approved software list is the set of approved Software Products; it is not the Service Catalog. Vendor end-of-life is a separate observed fact.
- **Management Provider** — external/internal endpoint-management provider such as Intune or Endpoint Agent.
- **Management Artifact** — provider-managed assignable object such as an app, configuration profile, compliance policy, endpoint-security policy, script or remediation.
- **Management Assignment** — provider intent linking a Management Artifact to a target scope, including intent/include/exclude/filter semantics.
- **Management Filter** — provider assignment filter whose rule and include/exclude mode are preserved; local evaluation is allowed only when deterministic and supported.
- **Management Applicability** — Turaco-derived evaluation of whether an assignment is expected to apply to a User/Device, with confidence/reason/freshness. It is not provider execution state.
- **Management Observation** — provider-reported target result/status for a Management Artifact with source and observed time.
- **Assignment Path** — explainability read model showing why an artifact is expected to apply or be excluded; not authoritative storage.
- **Target** — selected scope of an operation.
- **Target Set** *(F9 G2)* — a saved, bounded Device query (platform, OS version prefix, ownership, compliance, manufacturer, model, Device Group membership with optional nested Directory Groups, linked Asset's location) plus explicit include/exclude lists (at most 500 Devices each), evaluated on demand (bounded, explainable per clause), never continuously. Without any effective filter (a list covering its whole enum counts as unset) it selects all Devices, which is high impact, as is a root Directory Group with its nested groups; a Deployment is also high impact by its summed target count (≥ 200 Devices or ≥ 25 % of the live Devices). Replaces the planned Dynamic Group for endpoint targeting.
- **Dynamic Group** — query-defined continuously evaluated entity set (not implemented; endpoint targeting uses Target Sets).

## Security/knowledge
- **Security Advisory** — sourced vulnerability/update/threat information item (module `security`, reference `ADV-xxxxxx`), with affected software, OS and version criteria, severity and an explicit applicability/remediation lifecycle. Feed identity is the source and external id; a hand-entered Advisory has source `manual`.
- **Advisory Criterion** — one affected Software Product or product name/publisher with optional OS platform and version rules (`introduced`, `fixed`, `lt`, `le`, `eq`). Normalization is `matched` or `unmatched`; an unmatched product never silently becomes a confirmed exposure.
- **Vulnerability Finding** — Turaco-derived match of one Advisory, Device and Software Product (reference `VUL-xxxxxx`), with installed version, first/last observation time, confidence `probable` or `potential` and explicit triage/remediation state. It is distinct from an Endpoint Finding. Automatic `confirmed` confidence is not supported.
- **Risk Acceptance** — a Vulnerability Finding state recorded by a User with `security.accept_risk`, a reason code and a review date no later than twelve months ahead; it does not erase the observed exposure.

## Remote access *(ADR-0026; attended sessions implemented in F10 R-A)*
- **Remote Access Provider** — external system that provides remote screen/input, terminal and file-transfer transport, NAT traversal and relays (HopToDesk, RustDesk and AnyDesk as first providers); integrated through a Connector, never through the Connector or Endpoint Agent.
- **Peer Mapping** — explicit, audited link between a Device and its identity (peer id) at one Remote Access Provider; one active mapping per Device and provider, replaced mappings are closed, never deleted; never inferred from hostnames.
- **Launch Handle** — one-time, 60-second, user-bound token that Turaco exchanges for the provider launch link at the moment of use; only its hash is stored and the link is never stored.
- **Remote Access Session** — Turaco's record of one authorized remote session on a Device: initiating User, optional Ticket, mode (attended/unattended), policy decision, consent outcome and provider session reference, with provider-reported session facts kept separately with source and freshness. "Remote Access" is the canonical term; its permissions use the `remote_access.*` namespace (formerly `remote_support.start`).

## Workforce presence *(implemented: backend and UI; external sources planned, ADR-0028)*
- **Presence Entry** — time-bound statement about one User: planned work location (a Location, remote or travelling) or unavailable, with optional recurrence, source, freshness and visibility. Never carries an absence reason. Not an HR record.
- **Operational Availability** — derived availability of a person for operational work: `available`, `limited`, `unavailable` or `unknown`, with source and freshness.
- **Team Coverage** — derived count of operationally available Team members for a period against an optional per-Team minimum.

## AI *(read-only slice implemented, ADR-0029)*
- **Turaco AI** — the product's AI capability; distinct from AI assistants used to develop Turaco (ADR-0018).
- **AI Provider** — external or local model runtime (for example Anthropic, OpenAI, Azure/Microsoft, GitHub Copilot, Ollama) integrated through a Connector and enabled per Turaco installation.
- **AI Tool** — explicitly registered, typed Turaco application operation that AI may call, with schema, required permission, risk class (`read`, `write`, `high_impact`) and declared data egress; it runs as the requesting User.
- **Data class** — closed egress classification (`public_reference`, `business_record`, `personal_contact`, `device_context`) of the fields an AI Tool returns; a provider receives a tool's result only if it is allowed every class of the tool's output fields. Secrets, Audit records, Workforce Presence details, raw provider payloads, file contents and Remote Access session data are not classes and are never sent.
- **AI Conversation** — transient, server-held exchange between one User and the assistant (`ai.sessions`: transcript including tool calls and results, resource scope, 30 minutes idle); the browser holds only an opaque id. Not a business record and not retained unless the administrator enables transcript retention.
- **Conversation resource scope** — the records the User named, opened as context or explicitly consented to in a conversation; a tool may read only these. Ids that appear only in tool output never extend it.
- **AI Proposal** — short-lived proposal of an exact write operation that executes only after the requesting User confirms it; not an Approval and not a business record.
- **Knowledge Article** — reusable written knowledge.
- **Procedure** — documented sequence of work.
- **Runbook** — operational Procedure that can be instantiated as trackable work.

## Platform/integration
- **Notification** — message intent delivered through in-app/email/Teams/webhook channels.
- **IT Briefing Item** — operational information highlighted to IT staff; references an authoritative underlying record where possible. A *manual* Briefing Item is authored by a person and has no underlying record (plain text, title, severity, optional expiry); it is `draft → published → withdrawn` and immutable once published.
- **Module** — a unit of product capability in the code catalog (`platform/modules`): *core* modules are always on, *optional* modules have a **Module Switch**. Not a plugin: modules are compiled in.
- **Module Switch** — the runtime on/off state of one optional Module (versioned, audited, with a reason code). Off hides the module's API and pauses its jobs but keeps its data. It never bypasses the module's own preconditions (startup gate, privacy record, provider); a switch that is on while a precondition is unmet is shown as `blocked`.
- **Recurring Task Definition** — a template plus schedule rule that generates real Tasks; neither a Scheduled Job (technical timed execution) nor a Workflow (multi-step process). Generated Tasks keep a reference to their definition and the run they stand for.
- **Notification Delivery** — the state of sending one Notification through one channel (for example email), separate from the Notification and from the state of the record it is about.
- **Notification Preference** — a User's opt-out of a channel for a Notification category.
- **Rule** — small deterministic condition/action automation.
- **Workflow** — multi-step business process with state.
- **Scheduled Job** — technical/operational timed execution.
- **Integration** — connection to an external system.
- **Connector** — implementation of an Integration.
- **Connector Agent** — restricted customer-network bridge; not a general remote shell.
- **Endpoint Agent** — endpoint telemetry/management agent; separate, higher trust boundary.
- **Data Source** — origin of an observation.
- **Source of Truth** — authoritative owner for a field/domain.
- **Data Freshness** — how current an observation is.
- **Relationship** — typed cross-domain association between two records of any module (`platform/relationships`): allowed triples are registered by modules, at most one current row per triple, with confidence `declared` (by a person), `derived` (by Turaco) or `observed` (from an integration).
- **Impact** — the bounded set of records affected if a record is down (downstream: its dependents) or that it depends on (upstream), found by walking current Relationships up to depth 6 and 500 records; the result says when it was truncated.
- **Event** — business fact that happened; past-tense name.
- **Audit Event** — immutable security/compliance record of action/state transition.
- **Role** — named set of permissions; custom, or the built-in immutable `platform-administrator` holding all permissions. Not a Team and not an Assignment of work.
- **Role Assignment** — grant of a Role to a User or a Directory Group within a scope (currently only `global`); revoked assignments are kept as history.
- **Emergency Account** — local break-glass login for a dedicated User when directory login is unavailable; disabled by default and managed only by the operator CLI.

## Canonical distinctions

- Product != Asset: model/type vs specific tracked instance.
- Asset != Device: individually tracked resource vs technical Asset.
- Device != Endpoint: technical device vs endpoint-management participant.
- Installation != Assignment: observed software reality vs configured intent.
- Management Assignment != Management Applicability != Management Observation: configured targeting vs Turaco's expected evaluation vs provider-reported result.
- Desired State/Deployment != Management Assignment: Turaco's rollout intent vs targeting configured in the provider (counted as Assigned only once sync reads it back).
- Software Package != Management Artifact != Software Installation: provider-built package vs provider object it was published as vs observed installation.
- Software Management Provider != Management Provider: packages and publishes software vs targets devices and reports their results.
- Remote Access Session (Turaco-authorized) != provider-observed session: what Turaco authorized vs what the provider reports happened.
- Operational Availability != Asset `available` != Available stock: a person's availability for work vs an Asset status vs a stock quantity.
- AI Proposal confirmation != Approval: self-confirmation of a delegated action vs a recorded decision with separation of duties.
- Desired State != Observed State: what should be true vs what was last seen.
- Ticket != Task: support record vs concrete work.
- Role Assignment != Assignment: access grant vs responsibility for work.
- Incident != Problem: something broke vs underlying/repeating cause.
- Service Request != Procurement Request: user/business request to IT vs IT acquisition need.
- Location != Site: organizational/support concept vs physical topology.
- Service != Software Product: delivered capability vs installable software.

## New noun rule

Before creating a new domain noun, search this glossary and source tree and answer why no existing concept fits, how lifecycle/ownership differ, what relationships exist and whether this glossary changes.
