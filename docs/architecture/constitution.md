# Product & Architecture Constitution

**Status:** Accepted baseline

1. **One platform, not a collection of tools.** Shared platform services are reused across domains.
2. **Reuse before introduce.** New features first map onto existing concepts; parallel subsystems are prohibited without a clear need.
3. **Explicit domain model.** Ticket, Asset, Task, Deployment and other important concepts remain real types; do not build a generic EAV meta-platform.
4. **One meaning per concept.** The Domain Glossary defines canonical English terminology.
5. **English inside, localized outside.** Code/API/DB/events/logs/docs are English. UI text is localized.
6. **The repository is memory.** Architecture must be reconstructable from Git, code, tests, docs and ADRs; AI memory is never authoritative.
7. **Documentation must match reality.** Stale documentation is a defect. Generate references from code where practical.
8. **CI is stronger than instructions.** Machine-checkable rules are enforced by formatter/compiler/tests/architecture checks.
9. **Architecture changes are deliberate.** Frameworks, major dependencies and infrastructure changes require an ADR.
10. **Boring technology is a feature.** Prefer reliability, clarity, testability and operational simplicity over novelty.
11. **Modular monolith first.** Extraction requires a concrete scaling/security/availability/runtime reason.
12. **Source of Truth is explicit.** Synchronized fields have documented authority and conflict semantics.
13. **Observed data has freshness.** External data includes source and observation/sync timestamps.
14. **Relationships are first-class.** Important cross-domain links are structured, not buried in notes.
15. **History matters.** Current state, historical relationships and snapshots are distinct concerns.
16. **Everything important is auditable.** Security/business-relevant mutations produce immutable audit records.
17. **Employee UX hides IT complexity.** Employees see goals, not ITIL fields.
18. **Context before questions.** Known user/device/location/manager data is prefilled and correctable where uncertain.
19. **Identity without unnecessary friction.** Transparent SSO is preferred; no visible login does not mean no authentication.
20. **External systems must not control responsiveness.** Durable local commit first; slow integrations run asynchronously where possible.
21. **Integrations are adapters.** Domain behavior does not become vendor-specific.
22. **Security by architecture.** Least privilege, strong tenant boundaries, encryption, secrets, audit and supply-chain controls are foundational.
23. **Agents have explicit capabilities.** No implicit arbitrary shell. Cloud configuration cannot silently expand locally denied capabilities.
24. **Work is shared.** One Task model feeds My Work across tickets, requests, changes, deployments and initiatives.
25. **Knowledge is operational.** Articles/runbooks are related to actual services/software/devices/problems/changes.
26. **Automation remains understandable.** Rules, workflows, runbooks and scheduled jobs have separate meanings.
27. **Human control for high-impact actions.** Policies/approval/maintenance windows protect destructive or large-scale operations.
28. **Every number is explorable.** Dashboard counts open the actual underlying records through shared queries.
29. **New features explain their integration.** Ownership, reuse, events, permissions, audit, search, notifications and docs are designed before coding.
30. **Operational completeness is the definition of done.** Happy-path code alone is not a finished feature.

## Leitmotif

The platform may be extensive, but complexity must come from the business domain, not from inconsistent technical subsystems. Each new capability should increase the value of existing data.
