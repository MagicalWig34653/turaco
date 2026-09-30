# Claude Code Development Model

The repository is intentionally structured so a fresh Claude Code session can recover project philosophy from files instead of conversation history.

## Recommended lead session

For substantial Turaco work:

```bash
claude --model claude-opus-5-5
```

Opus acts as lead/architect. Project skills and subagents intentionally route bounded implementation work to Sonnet 5.5. See `ai-orchestration.md`.

## Context layers

- `CLAUDE.md`: short always-on rules and canonical commands.
- `.claude/rules/`: path-specific language/database/security/docs guidance.
- `.claude/skills/`: repeatable engineering workflows such as `/feature-design`, `/database-change` and `/release-readiness`.
- `.claude/agents/`: independent specialists with isolated context and explicit model routing.
- `.claude/hooks/`: deterministic guardrails/formatting; these are stronger than prompt instructions.
- `docs/decisions/`: long-lived rationale for architecture choices.

Do not expand `CLAUDE.md` into a manual. Keep detailed reference in docs/skills/rules.

## Recommended session pattern

For substantial work:

```text
1. /feature-design <goal>              [Opus]
2. review/adjust design
3. bounded implementation skill/agents [Sonnet]
4. Opus reviewers where relevant
5. optional independent Codex review
6. make check
7. final diff review
```

Claude should investigate files before claiming how existing code works. Do not optimize for passing tests by hard-coding test data; implement the general domain behavior.

## Parallel work

Default to subagents. Agent Teams are experimental and are suitable only for genuinely independent frontend/backend/test or research/review workstreams with explicit file ownership. Turaco's normal ceiling is three concurrent implementers.

See `ai-orchestration.md` for the policy and local opt-in.

## Context durability

Native Claude Code compaction is the default. External compaction plugins are not repository dependencies. Sessions may be discarded when context quality degrades; repository state must be sufficient to continue.

See `context-management.md`.

## Git as memory

Small coherent commits and PRs make architectural history discoverable. Avoid giant "AI implementation" commits that mix unrelated domains.

## Generated facts

Permissions, event catalog and configuration reference are generated from code via `turaco-docgen`. Change the source registry, regenerate, and commit the result; CI checks drift.
