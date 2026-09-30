---
name: review-architecture
description: Review a proposed or implemented Turaco change for module boundaries, duplicated concepts and architectural drift.
model: claude-opus-5-5
effort: high
---
# Architecture Review

Review the diff/plan against Constitution, Module Boundaries, Glossary and ADRs. Focus on ownership, dependency direction, duplicate platform mechanisms, new abstractions/dependencies, state model consistency and integration with search/audit/tasks/events/relationships. Report concrete findings with file paths and severity. Do not invent style objections with no architectural consequence.
