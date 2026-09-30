# AI Orchestration

**Status:** Accepted development policy

Turaco uses AI agents as an engineering organization rather than treating every session as an interchangeable coder.

## Default topology

```text
Claude Opus 5.5 lead
        |
        +-- Sonnet 5.5 backend implementation
        +-- Sonnet 5.5 frontend implementation
        +-- Sonnet 5.5 integration/test/docs work
        |
        +-- Opus 5.5 architecture/domain/database/security review
        |
        +-- optional independent Codex review
        |
        +-- make check
```

The lead owns synthesis and integration. Implementers do not independently change architectural policy.

## Model routing

### Opus 5.5
Use for work where mistakes create long-lived structural cost:

- feature architecture;
- new domain concepts and state machines;
- cross-module boundaries;
- schema ownership and difficult migrations;
- concurrency/transaction design;
- authentication/authorization/security/crypto;
- Connector/Endpoint Agent trust boundaries;
- difficult ambiguous defects;
- final critical review of major changes.

### Sonnet 5.5
Use for well-scoped work after the design is known:

- Go application/API implementation;
- React/UI/i18n implementation;
- tests and fixtures;
- routine migrations with approved design;
- integrations following established adapters;
- documentation updates;
- repository exploration;
- bounded refactoring and bug fixes.

Exact model IDs are pinned in project skill/subagent definitions so a lead-session model change does not silently change the review/implementation split.

## Subagents first

Subagents are the default parallel mechanism because they isolate noisy exploration/review context and report a focused result to the lead.

Turaco defines project agents under `.claude/agents/` for:

- exploration;
- architecture/domain/database/security review;
- backend/frontend/integration/agent implementation;
- tests;
- documentation review.

Reviewer agents should normally review requirements + repository state + actual diff independently rather than being given the implementer's full reasoning transcript.

## Agent Teams

Claude Code Agent Teams are experimental and therefore not enabled by the repository by default.

Use them only when all conditions are true:

1. at least two workstreams are materially independent;
2. each teammate has explicit file/directory ownership;
3. shared domain contracts/migrations are designed first;
4. the expected parallel speedup exceeds coordination cost;
5. the lead will perform integration and final `make check`.

Turaco default: **3 concurrent implementers maximum**. Increase only deliberately.

Good split:

```text
backend/internal/modules/knowledge/**     -> backend teammate
frontend/src/modules/knowledge/**         -> frontend teammate
...tests/docs owned separately            -> test/docs teammate
```

Bad split:

```text
three agents all "implement Knowledge Base"
```

Do not have several teammates run repository-wide expensive checks continuously. Workers run targeted tests; the lead runs the final full quality gate.

## Enabling Agent Teams locally

Because the feature is experimental, enable it in a local/user setting rather than committing it as a project requirement:

```json
{
  "env": {
    "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS": "1"
  }
}
```

In split-pane mode, use a supported terminal/tmux setup. In-process mode needs no special terminal integration.

## Independent Codex review

Codex is optional and never required for Turaco development. When available, use it primarily as an independent reviewer rather than duplicating the Claude implementation path.

Useful review prompts:

- find correctness bugs that the implementation/review team may have missed;
- inspect SQL/migrations for data loss, locking and rollback/forward-migration hazards;
- look for races, non-idempotent retries and transaction-boundary problems;
- inspect authz/tenant-boundary/secret handling;
- identify architecture violations with exact file references;
- propose adversarial tests for changed behavior.

Treat findings as review input, not automatically correct instructions. Resolve findings against Turaco's repository source of truth.
