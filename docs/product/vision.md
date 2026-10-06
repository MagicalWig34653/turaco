# Product Vision

**Status:** Proposed

Turaco is a coherent **IT operations platform** for internal IT organizations and managed-service customers. It unifies daily IT work, service delivery and technical inventory around one canonical model instead of replacing one silo with another.

## Positioning

Turaco is not intended to win by becoming a larger generic ITSM suite than established enterprise products. Its target is a simpler operational model for internal IT teams that want deep integration without operating an ITSM platform as a separate discipline.

A concise product description is:

> **Turaco — open-source IT operations for internal IT teams.**

The product should be useful self-hosted and equally capable when operated as an official managed service.

## Primary outcomes

### Employee experience
Employees can:
- open the portal without redundant login prompts through transparent SSO where possible;
- have their current user and likely/current device detected automatically;
- report a problem in a minimal form;
- request hardware, software and access;
- see their requests in plain language;
- receive HTML email/Teams/in-app updates;
- discover relevant knowledge and known outages before creating duplicate tickets.

### IT workspace
IT staff work primarily from **My Work / Today**:
- assigned tickets and tasks;
- approvals and service requests;
- upcoming changes and modernization work;
- security advisories and relevant updates;
- known incidents/service health;
- integration/system problems;
- team/internal news.

### Shared technical model
The platform connects:
- users and organization;
- products, procurement and inventory;
- serialized assets and assignments;
- endpoints and Intune/agent observations, including explainable management assignments and effective-vs-observed state;
- installed software and desired software state;
- services, infrastructure, networks and physical placement;
- tickets, problems, changes, tasks and initiatives;
- knowledge, runbooks and known errors;
- security advisories, vulnerability findings and remediation.

## Build versus integrate

Turaco owns the operational model and workflows where integration creates product value. It should integrate specialized systems where reproducing their depth would dilute the product.

Likely build areas:
- employee portal and service requests;
- My Work / IT Briefing;
- inventory, procurement and asset lifecycle;
- knowledge integrated with operations;
- task/change/workflow/notification platform services;
- normalized endpoint/software context and cross-domain relationships.

Likely integrate-first areas:
- identity through AD/LDAP/Entra/OIDC;
- MDM through Intune;
- deep DCIM/IPAM through NetBox where appropriate;
- EDR/AV through existing providers;
- external vulnerability intelligence;
- software packaging and patch mechanics through Software Management Providers (IntuneGet first; [ADR-0027](../decisions/ADR-0027-software-management-providers.md)) while Turaco owns approval, rollout and context;
- remote-desktop transport through Remote Access Providers (HopToDesk, RustDesk and AnyDesk as first providers; [ADR-0026](../decisions/ADR-0026-remote-access-providers.md)) while Turaco owns authorization, audit and context;
- AI model runtimes through AI Providers (hosted or local; [ADR-0029](../decisions/ADR-0029-turaco-ai.md)) while Turaco owns the tools, permissions and audit;
- presence sources such as Microsoft 365 and HR systems ([ADR-0028](../decisions/ADR-0028-workforce-presence.md)).

## Planned capabilities

1. Service Catalog & Requests
2. Procurement & Goods Receipt
3. Inventory / Warehouse
4. Asset Management / ITAM
5. Service Desk / Incident / Problem / Major Incident
6. Knowledge Base / Procedures / Runbooks
7. My Work / recurring tasks / daily log
8. IT Briefing / internal news / security & update information
9. Endpoint Intelligence from Intune and future Endpoint Agent, including Group/User/Device assignment explainability and comparison
10. Infrastructure Documentation / CMDB / optional IPAM or NetBox integration
11. Change & modernization planning
12. Security correlation against actual software/device inventory
13. Software lifecycle and patch orchestration over Software Management Providers (IntuneGet → Intune first)
14. Remote access over Remote Access Providers (HopToDesk, RustDesk, AnyDesk), Turaco-authorized and audited
15. Workforce Presence: operational availability and team coverage for IT, not HR
16. Turaco AI: provider-independent assistance through permissioned Turaco tools

## Product character

Turaco may become broad, but it must not feel like a bundle of acquired tools. Every new capability should reuse common identity, permissions, relationships, tasks, audit, search, timeline, notifications, targeting, scheduling and events.

The intended experience is **fast, explicit, low-friction and operationally boring**, while the brand can be visually distinctive and colorful.
