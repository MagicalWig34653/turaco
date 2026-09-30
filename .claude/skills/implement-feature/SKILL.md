---
name: implement-feature
description: Implement an already-designed Turaco feature according to repository architecture and definition of done.
argument-hint: <approved feature/task>
model: claude-sonnet-5-5
effort: high
---
# Implement Feature

Implement `$ARGUMENTS` only after design is coherent.

- Confirm a feature design exists in the issue/plan/current context.
- Inspect current code before editing.
- Implement in small coherent steps using existing patterns.
- Add migration, permission, audit, events, tests and docs where applicable.
- Run targeted tests during implementation.
- Run `make check` before completion.
- Review `git diff --check` and the final diff for unrelated changes.
- Summarize implementation, migration, permissions, events, dependencies, tests, docs and known limitations.
