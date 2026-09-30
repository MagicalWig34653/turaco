---
name: parallel-plan
description: Split a substantial Turaco change into safe parallel workstreams with explicit ownership before using subagents or Agent Teams.
argument-hint: <approved feature/design>
model: claude-opus-5-5
effort: high
---
# Parallel Plan

For `$ARGUMENTS`, decide first whether parallelism is beneficial.

If yes, define at most three initial implementation workstreams. For each provide:

- exact goal/deliverable;
- owned directories/files;
- inputs/contracts it may read but not redefine;
- targeted tests it should run;
- dependencies/order;
- merge/integration point.

Shared migrations/domain/API contracts are designed before parallel implementation. Never assign overlapping write ownership. Prefer subagents unless teammates need direct coordination.
