---
name: feature-design
description: Design a substantial Turaco product feature before implementation and map it onto the existing platform architecture.
argument-hint: <feature goal>
model: claude-opus-5-5
effort: high
---
# Feature Design

Design `$ARGUMENTS` before implementation.

Read `docs/architecture/constitution.md`, `docs/domain/glossary.md`, `docs/architecture/module-boundaries.md`, relevant ADRs and existing implementation.

Produce a concise design containing:

1. User/business outcome.
2. Existing domain concepts reused.
3. Genuinely new concepts and why they are new.
4. Owning module and affected modules.
5. State/lifecycle impact.
6. Data model and migration impact.
7. API surface.
8. Permissions and scopes.
9. Audit requirements.
10. Events/background processing/idempotency.
11. Search, relationships, timeline, My Work and notifications impact.
12. External integrations/agents, including source/freshness and Assigned vs Expected Applicable vs Observed semantics where management providers are involved.
13. Security/privacy risks.
14. Tests and documentation.
15. Whether an ADR is required.
16. Parallelization plan, if useful, with explicit file ownership.

Do not implement until the design is coherent. Prefer extending existing concepts over parallel subsystems.
