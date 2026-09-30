# Domain Glossary

**Status:** Canonical terminology baseline

Source code, APIs, DB schemas, events and technical documentation use these English terms.

## Organization
- **User** — a person known to the platform. Not identical to an LDAP/Entra account.
- **External Identity** — a User representation in AD/LDAP/OIDC/Entra.
- **Employee** — business classification of a User; not a parallel identity model.
- **Team** — operational responsibility group.
- **Directory Group** — group observed from an external directory/identity provider (for example Entra/AD) and used for access/management targeting; not the same as Team.
- **Directory Group Membership** — observed User/Device membership in a Directory Group with source/freshness; dynamic-group rule evaluation stays with the provider unless explicitly supported.
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

## Inventory/procurement
- **Warehouse** — logical inventory boundary.
- **Storage Location** — specific place within a Warehouse.
- **Stock Item/Balance** — current quantity view for non-serialized products.
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
- **Software Product** — canonical normalized software identity.
- **Software Alias** — discovery/provider name mapped to canonical Software Product.
- **Software Version** — known release/version; preserve raw source value.
- **Software Installation** — observed installation on Endpoint; observation, not intent.
- **Software Assignment** — desired/provider-side assignment; does not prove installation.
- **Desired State** — intended configuration/software outcome.
- **Observed State** — latest known state with source/freshness.
- **Deployment** — managed operation attempting to establish Desired State on Targets.
- **Deployment Target/Result/Attempt** — concrete resolved endpoint and execution history.
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
- **Knowledge Article** — reusable written knowledge.
- **Procedure** — documented sequence of work.
- **Runbook** — operational Procedure that can be instantiated as trackable work.

## Platform/integration
- **Notification** — message intent delivered through in-app/email/Teams/webhook channels.
- **IT Briefing Item** — operational information highlighted to IT staff; references an authoritative underlying record where possible.
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

## Canonical distinctions

- Product != Asset: model/type vs specific tracked instance.
- Asset != Device: individually tracked resource vs technical Asset.
- Device != Endpoint: technical device vs endpoint-management participant.
- Installation != Assignment: observed software reality vs configured intent.
- Management Assignment != Management Applicability != Management Observation: configured targeting vs Turaco's expected evaluation vs provider-reported result.
- Desired State != Observed State: what should be true vs what was last seen.
- Ticket != Task: support record vs concrete work.
- Incident != Problem: something broke vs underlying/repeating cause.
- Service Request != Procurement Request: user/business request to IT vs IT acquisition need.
- Location != Site: organizational/support concept vs physical topology.
- Service != Software Product: delivered capability vs installable software.

## New noun rule

Before creating a new domain noun, search this glossary and source tree and answer why no existing concept fits, how lifecycle/ownership differ, what relationships exist and whether this glossary changes.
