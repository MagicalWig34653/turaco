# Turaco — Claude Code Instructions

Turaco is designed for long-term AI-maintained development. The repository, tests, ADRs and documentation are the source of truth. Conversation memory is not.

## Start every non-trivial task

1. Read `docs/architecture/constitution.md`.
2. Read `docs/domain/glossary.md`.
3. Read `docs/product/current-status.md` so planned capabilities are not mistaken for implemented behavior.
4. Read the relevant domain/module docs and ADRs.
5. Inspect existing code and tests before proposing a new pattern.
6. For substantial features, invoke `/feature-design` before implementation.

## Non-negotiable rules

- Technical code, identifiers, DB objects, API contracts, logs, commits and technical docs are English only.
- User-facing text uses i18n resources; never hard-code German UI strings.
- Reuse existing platform concepts before introducing new concepts.
- Do not create a parallel task, notification, audit, search, relationship, scheduling or permission system inside a module.
- Do not introduce a new framework, database, broker, search engine, ORM, auth model or major dependency without an accepted ADR.
- Core architecture is a modular monolith. Cross-module access goes through public contracts/events, never another module's repository/private tables.
- State changes use explicit domain operations, not generic `UpdateStatus` APIs.
- Externally observed data must preserve source and freshness. Do not overwrite platform-owned data silently.
- For endpoint-management providers, never collapse configured Assignment, Turaco-derived Expected Applicability and provider Observed result into one status.
- Security-sensitive actions must be authorized, audited and designed for least privilege.
- Connector Agent and Endpoint Agent are separate trust boundaries. Never turn either into an implicit arbitrary remote shell.
- Documentation changes are part of implementation. Stale documentation is a defect.
- Do not edit released migrations; add a new forward migration.
- Prefer explicit, boring code over clever abstractions. Do not design for hypothetical requirements.

## AI model routing

The preferred lead session is **Claude Opus 5.5**. Project skills/subagents route bounded work deliberately:

- Opus 5.5: architecture, domain design, database/concurrency design, security, agent trust boundaries and final critical review.
- Sonnet 5.5: bounded backend/frontend implementation, integrations, tests, docs and repository exploration.

Do not use Sonnet to redefine architecture independently. Do not use Opus for routine implementation merely because it is available. See `docs/development/ai-orchestration.md`.

## Parallel work

Default to focused subagents. Use experimental Agent Teams only when work is genuinely parallel and file ownership can be separated. Turaco's default maximum is three concurrent implementers. Never assign two agents to edit the same files. Run the final repository-wide quality gate once from the lead session.

## Context and compaction

Native Claude Code compaction is the default. External compaction services are not project dependencies. The repository must contain all durable decisions.

Before a major context reset, ensure the current design, code, tests, migration state and unresolved findings are represented in repository/issue/PR state. Sessions are disposable; project knowledge is not.

When manually running `/compact`, preserve at minimum:

- current feature goal and accepted design;
- files/modules currently being changed;
- unresolved failures and test status;
- migration/data implications;
- security and authorization decisions;
- documentation obligations;
- next concrete step.

See `docs/development/context-management.md`.

## Canonical commands

Run from repository root:

```bash
make doctor          # verify local toolchain
make infra-up        # PostgreSQL + S3Mock in Colima/Docker
make migrate         # apply DB migrations
make dev             # instructions for local dev processes
make fmt             # format Go + frontend
make lint            # Go vet + frontend lint
make test            # Go + frontend tests
make archcheck       # module boundary enforcement
make docs-check      # generated references + documentation links
make check           # full local quality gate
```

In Claude Code cloud sessions a SessionStart hook bootstraps toolchain, Docker, infrastructure and migrations automatically; see `docs/development/cloud-development.md`.

Before declaring work complete, run `make check` unless the task cannot reasonably require it. If a check cannot run, report exactly why.

## Architecture map

- `backend/internal/platform/`: cross-cutting platform services.
- `backend/internal/modules/`: business domain modules.
- `agents/connector/`: restricted customer-network connector.
- `agents/endpoint/`: endpoint management agent; higher privilege.
- `frontend/src/modules/`: frontend feature modules mirroring domain concepts.
- `docs/decisions/`: ADRs; architecture changes require one.
- `docs/reference/`: generated/validated reference docs.

## Implementation workflow

For substantial changes, identify before coding:

- affected modules;
- existing concepts reused;
- genuinely new concepts;
- DB/migration impact;
- permissions;
- audit requirements;
- events/background work;
- search/relationships/My Work/timeline impact;
- external integration impact;
- security/privacy impact;
- documentation/tests.

If a feature does not fit cleanly, resolve the design before implementation.

## Definition of done

Follow `docs/development/definition-of-done.md`. In particular:

- backend authorization is mandatory; UI visibility is not authorization;
- important mutations are audited;
- retryable external/background operations are idempotent;
- tests cover new behavior and fixed regressions;
- relevant docs are updated in the same change;
- final diff contains no unrelated files, secrets, debug code or temporary artifacts.

## Documentation hierarchy

Read facts from their authoritative source:

- product principles: `docs/architecture/constitution.md`
- terminology: `docs/domain/glossary.md`
- ownership/boundaries: `docs/architecture/module-boundaries.md`
- technical architecture: `docs/architecture/system-architecture.md`
- data model: `docs/domain/core-data-model.md`
- lifecycles: `docs/domain/state-machines.md`
- workflows: `docs/workflows/`
- decisions: `docs/decisions/`
- current implementation truth: `docs/product/current-status.md`
- generated permissions/events/config: `docs/reference/`

Do not duplicate detailed facts into `CLAUDE.md`; update the authoritative document instead.
