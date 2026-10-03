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
- **Ticket** — generic tracked support record; employee UI should use friendlier language.
- **Incident** — unplanned interruption/degradation/malfunction.
- **Service Request** — structured request to provide/change/grant something.
- **Major Incident** — significant Incident affecting many users/services.
- **Problem** — tracked underlying/repeating cause.
- **Known Error** — Problem whose cause/workaround is sufficiently understood; prefer Problem state/data over a duplicate top-level object.
- **Service** — IT-delivered capability, e.g. SAP, VPN, Corporate Wi-Fi.
- **Service Catalog** — collection of requestable IT offerings.
- **Catalog Item** — user-facing requestable offering; not necessarily a Product.

## Work/change/planning
- **Task** — concrete unit of work shared across the platform.
- **Assignment** — responsibility association between work and User/Team.
- **Approval** — recorded decision required before a flow continues.
- **My Work** — consolidated operational view across tasks/tickets/requests/changes/deployments.
- **Initiative** — medium/long-term modernization effort.
- **Change** — planned modification to the IT environment.
- **Maintenance Window** — time period in which defined operational changes may occur.

## Infrastructure/network
- **Site** — physical geographic site.
- **Building / Room / Rack** — physical topology.
- **Rack Placement** — placement of an Asset/Device in a Rack.
- **Virtual Machine** — virtual compute instance; not necessarily a physical Asset.
- **Hypervisor** — virtualization host, normally represented by an Asset/Device role.
- **Interface** — network interface belonging to Device/VM.
- **VLAN / VRF / Prefix / IP Address** — canonical network/IPAM concepts if natively modeled.

## Endpoint/software
- **Endpoint Finding** — data-quality observation about a Device or its software (for example no matching Asset); distinct from Security Findings and provider-reported errors.
- **Software Product** — canonical normalized software identity.
- **Software Alias** — discovery/provider name mapped to canonical Software Product.
- **Software Version** — known release/version; preserve raw source value.
- **Software Installation** — observed installation on Endpoint; observation, not intent.
- **Software Assignment** — software-focused projection of a Management Assignment (provider-configured targeting); never Turaco rollout intent and does not prove installation.
- **Desired State** — Turaco's intended configuration/software outcome; reached through a Deployment and distinct from the provider's Management Assignment.
- **Observed State** — latest known state with source/freshness.
- **Deployment** — managed operation attempting to establish Desired State on Targets.
- **Deployment Target/Result/Attempt** — concrete resolved endpoint and execution history. For provider-executed deployments the result is derived from fresh Management Observations (Software Installation corroborates), never from a package being published.
- **Deployment Ring** *(planned, ADR-0027)* — ordered rollout stage of a Deployment (for example pilot, early, broad) with its own resolved Targets and a promotion gate (Approval, success threshold on fresh evidence, soak time, Maintenance Window). A Device is in at most one ring per Deployment. States: [state machines](state-machines.md#deployment).
- **Software Management Provider** *(planned, ADR-0027)* — external system that packages a Software Version and publishes or updates it in a Management Provider (IntuneGet → Intune first). It manages no devices and owns no targeting or device results.
- **Software Package** *(planned, ADR-0027)* — provider-built deployable package of one Software Version (provider reference, installer hash, publish status) linked to the Management Artifact it became once published.
- **Software Approval Status** *(planned, ADR-0027)* — Turaco's decision whether a Software Product may be used (`candidate`, `approved`, `deprecated`, `retired`, `blocked`); deployable versions additionally need a version approval bound to the installer hash. The approved software list is the set of approved Software Products; it is not the Service Catalog. Vendor end-of-life is a separate observed fact.
- **Management Provider** — external/internal endpoint-management provider such as Intune or Endpoint Agent.
- **Management Artifact** — provider-managed assignable object such as an app, configuration profile, compliance policy, endpoint-security policy, script or remediation.
- **Management Assignment** — provider intent linking a Management Artifact to a target scope, including intent/include/exclude/filter semantics.
- **Management Filter** — provider assignment filter whose rule and include/exclude mode are preserved; local evaluation is allowed only when deterministic and supported.
- **Management Applicability** — Turaco-derived evaluation of whether an assignment is expected to apply to a User/Device, with confidence/reason/freshness. It is not provider execution state.
- **Management Observation** — provider-reported target result/status for a Management Artifact with source and observed time.
- **Assignment Path** — explainability read model showing why an artifact is expected to apply or be excluded; not authoritative storage.
- **Target** — selected scope of an operation.
- **Dynamic Group** — query-defined continuously evaluated entity set.

## Security/knowledge
- **Security Advisory** — vulnerability/update/threat information item.
- **Vulnerability Finding** — assessment that a specific resource may be affected, with confidence and remediation state.

## Remote access *(planned, ADR-0026)*
- **Remote Access Provider** — external system that provides remote screen/input, terminal and file-transfer transport, NAT traversal and relays (HopToDesk first); integrated through a Connector, never through the Connector or Endpoint Agent.
- **Remote Access Session** — Turaco's record of one authorized remote session on a Device: initiating User, optional Ticket, mode (attended/unattended), policy decision, consent outcome and provider session reference, with provider-reported session facts kept separately with source and freshness. "Remote Access" is the canonical term; the reserved permission `remote_support.start` predates it.

## Workforce presence *(planned, ADR-0028)*
- **Presence Entry** — time-bound statement about one User: planned work location (a Location, remote or travelling) or unavailable, with optional recurrence, source, freshness and visibility. Never carries an absence reason. Not an HR record.
- **Operational Availability** — derived availability of a person for operational work: `available`, `limited`, `unavailable` or `unknown`, with source and freshness.
- **Team Coverage** — derived count of operationally available Team members for a period against an optional per-Team minimum.

## AI *(planned, ADR-0029)*
- **Turaco AI** — the product's AI capability; distinct from AI assistants used to develop Turaco (ADR-0018).
- **AI Provider** — external or local model runtime (for example Anthropic, OpenAI, Azure/Microsoft, GitHub Copilot, Ollama) integrated through a Connector and enabled per Turaco installation.
- **AI Tool** — explicitly registered, typed Turaco application operation that AI may call, with schema, required permission, risk class (`read`, `write`, `high_impact`) and declared data egress; it runs as the requesting User.
- **AI Proposal** — short-lived proposal of an exact write operation that executes only after the requesting User confirms it; not an Approval and not a business record.
- **Knowledge Article** — reusable written knowledge.
- **Procedure** — documented sequence of work.
- **Runbook** — operational Procedure that can be instantiated as trackable work.

## Platform/integration
- **Notification** — message intent delivered through in-app/email/Teams/webhook channels.
- **IT Briefing Item** — operational information highlighted to IT staff; references an authoritative underlying record where possible. A *manual* Briefing Item is authored by a person and has no underlying record (plain text, title, severity, optional expiry); it is `draft → published → withdrawn` and immutable once published.
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
- **Relationship** — typed cross-domain association.
- **Event** — business fact that happened; past-tense name.
- **Audit Event** — immutable security/compliance record of action/state transition.
- **Saved View** — reusable query/filter/presentation.
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
