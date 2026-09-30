---
name: context-checkpoint
description: Prepare a Turaco workstream for compaction, /clear, handoff or a new lead session without losing durable project state.
disable-model-invocation: true
model: claude-sonnet-5-5
effort: medium
---
# Context Checkpoint

Do not create conversational memory files merely to preserve chat.

Before handoff/reset:

1. inspect working tree and current tests;
2. ensure accepted design/ADR/doc changes are persisted where they belong;
3. ensure unresolved work is recorded in the relevant issue/design/TODO with context;
4. summarize uncommitted files and known failures;
5. state the next concrete action a fresh session should take;
6. do not commit/push unless explicitly authorized.

The result should make the repository understandable without the old transcript.
