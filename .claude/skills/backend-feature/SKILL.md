---
name: backend-feature
description: Implement a bounded, already-designed Turaco Go backend change using existing architecture and tests.
argument-hint: <approved backend task>
model: claude-sonnet-5-5
effort: medium
---
# Backend Feature

Implement `$ARGUMENTS` from an approved design or clearly bounded requirement.

- Inspect existing module/platform patterns first.
- Respect module ownership and public contracts.
- Use explicit domain operations and explicit SQL.
- Enforce authorization in backend code.
- Add audit/events/idempotency where the design requires them.
- Add focused unit/integration tests.
- Update authoritative documentation.
- Run targeted tests during implementation and `make check` before completion when practical.

Do not introduce a framework/dependency/architecture change as part of routine implementation.
