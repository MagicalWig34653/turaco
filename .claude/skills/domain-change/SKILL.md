---
name: domain-change
description: Design/review a new or changed Turaco entity, state, relationship, invariant or domain term before code changes.
argument-hint: <domain change>
model: claude-opus-5-5
effort: high
---
# Domain Change

For `$ARGUMENTS`:

1. Search the glossary/core data model/state machines for existing concepts.
2. Identify owning module.
3. Define identity, lifecycle, invariants and source of truth.
4. Explain why existing entities/reasons/relationships/custom fields cannot model it.
5. Define history/snapshot/provenance requirements.
6. Define events, permissions, audit, search and cross-module references.
7. Identify required documentation/ADR updates.

Do not add a new domain noun merely for implementation convenience.
