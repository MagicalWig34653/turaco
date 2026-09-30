# Platform Services & Module Boundaries

**Status:** Accepted baseline

## Architecture style

The core is a modular monolith. Each domain owns its model, application operations, persistence and business rules. Other modules consume public contracts or events, never private repositories/tables.

## Platform services

`backend/internal/platform/` owns cross-cutting capabilities:
- authentication context and identity-provider abstraction;
- authorization (permissions + scopes);
- immutable audit;
- events/outbox/background job foundations;
- object/file storage abstraction;
- notifications/channels/templates/preferences;
- relationships;
- global search contracts;
- secrets/encryption abstractions;
- tasks/My Work primitives;
- scheduling and maintenance windows;
- targeting/dynamic groups;
- configuration and feature flags;
- localization and shared API/error conventions.

A domain may use these capabilities but must not reimplement its own alternative.

## Business modules

| Module | Owns |
|---|---|
| Organization | users, departments, teams, locations, cost centers, external Directory Groups and observed memberships |
| Products | product, optional variant, manufacturer, categories |
| Catalog | requestable services/forms/eligibility |
| Requests | service request lifecycle and fulfillment coordination |
| Service Desk | incidents, major incidents, problems/known error state |
| Knowledge | articles, procedures, runbook definitions/executions |
| Assets | asset/device identity, lifecycle, assignment |
| Inventory | warehouse, storage location, stock, transactions, reservations, goods receipt |
| Procurement | procurement request, supplier, purchase order |
| Endpoint | desired state, deployment, management identities/providers, normalized management artifacts/assignments/filters/applicability/observations, Endpoint Agent integration |
| Software | normalized products/versions/aliases/install observations/assignments |
| Security | advisories, findings, remediation tracking |
| Infrastructure | sites/buildings/rooms/racks/VMs/physical topology |
| Network/IPAM | optional native VLAN/prefix/IP/interface model; decision remains separate |
| Services | service ownership, criticality, service relationships/status |
| Changes | changes, risk, approvals, execution/review |
| Planning | initiatives/modernization/milestones |
| Briefing | presentation/editorial aggregation; underlying entities remain authoritative |

## Integration modules

`backend/internal/integrations/` translates external systems into public domain/application contracts. Vendor SDK/types do not leak across the domain model.

## Communication

Synchronous calls use a module's small public application contract when an immediate result is required. Asynchronous reactions use domain/integration events through the outbox.

Forbidden examples:
- Service Desk importing Inventory repository code;
- Endpoint querying Assets tables directly;
- an integration writing domain tables with raw SQL;
- frontend module A reaching into private state of module B.

## Database ownership

One PostgreSQL database is acceptable, using schemas such as `platform`, `organization`, `products`, `assets`, `inventory`, `service_desk`, etc. Database co-location does **not** remove logical ownership. Cross-module foreign keys are deliberate, not automatic.

## Extraction

A module becomes an independent service only for a concrete reason such as different scaling, independent security/availability boundary, network placement or incompatible runtime. "Microservices are modern" is not a reason.
