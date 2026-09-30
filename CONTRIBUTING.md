# Contributing

This repository is optimized for agentic development but uses normal software-engineering controls.

## Workflow

1. Create a focused branch.
2. For substantial features, produce an integration design using `.claude/skills/feature-design`.
3. Implement the smallest coherent change.
4. Add/update tests and documentation.
5. Run `make check`.
6. Review the final diff.
7. Open a pull request using the template.

## Commit language

English only. Prefer Conventional Commit style, for example:

```text
feat(inventory): add serialized asset reservations
fix(servicedesk): preserve device snapshot on reopen
```

## Architecture

Architecture changes require an ADR under `docs/decisions/`. Do not introduce competing patterns because they are locally convenient.

## Migrations

Released migrations are immutable. Add a new forward migration for corrections.

## Dependencies

New major dependencies require justification. New frameworks/infrastructure require an ADR.
