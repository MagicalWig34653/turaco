# Context Management

Turaco's durability must not depend on the context window of one AI session.

## Principle

> **Repository is memory. Sessions are disposable.**

Durable decisions belong in code, tests, ADRs, domain docs, feature designs, issues/PRs and coherent Git history.

## Default compaction

Use Claude Code's native auto-compaction. Do not make external compaction providers a project requirement.

When manually invoking `/compact`, use instructions that retain:

- current goal and accepted feature design;
- affected modules/files;
- unresolved failures;
- current test/check status;
- schema/migration/data concerns;
- security/authorization decisions;
- documentation obligations;
- next concrete step.

`CLAUDE.md` and repository docs remain authoritative even when a conversation summary disagrees with them.

## Session reset

Use a clean session rather than stretching a degraded context indefinitely when:

- the feature has reached a clean checkpoint;
- major architectural direction changed;
- the session contains large amounts of obsolete debugging output;
- compaction has made constraints unclear.

Before `/clear` or ending a session:

1. ensure valuable code is in the working tree/commit/branch;
2. ensure accepted architecture is documented;
3. record unresolved TODOs in the relevant issue/design rather than only in chat;
4. run targeted tests if possible;
5. leave the working tree understandable to a fresh agent.

A fresh session should reconstruct work by reading repository state, not by requiring a transcript summary.

## Jev / external compaction

Jev is **not part of Turaco's required environment**.

A developer may experiment privately with a local-only compaction setup (for example a local model served through LM Studio) if they have independently reviewed the plugin and data flow. Such tooling belongs to user-level Claude configuration, not the repository, and must never be required by CI or other contributors.

Do not route Turaco source/session content through third-party compaction services without explicit organizational approval.
