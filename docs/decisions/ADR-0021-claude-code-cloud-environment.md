# ADR-0021: Claude Code Cloud Sessions Reuse the Local Development Environment

- Status: Accepted
- Extends: ADR-0010

## Context

Turaco is also developed from Claude Code cloud sessions (`CLAUDE_CODE_REMOTE=true`): ephemeral Ubuntu containers with Go, an nvm installation, the Docker engine (not started) and outbound access through an egress proxy. There is no IDE, no Colima and no persistent state. Without automation, every fresh session had to rediscover how to start Docker and select toolchain versions, and `make check` could pass while silently skipping database tests.

## Decision

There is one conceptual development environment: native Go/Node toolchain plus PostgreSQL/S3Mock from `deploy/compose/dev.yaml`, driven by the same Makefile targets and scripts. Cloud sessions add only a thin adapter:

- `.claude/hooks/session-start.sh` (SessionStart hook) runs `scripts/cloud-bootstrap.sh` only when `CLAUDE_CODE_REMOTE=true`, and is a no-op elsewhere.
- The adapter selects the Go toolchain from `.go-version` (`GOTOOLCHAIN`) and the Node major from `.nvmrc` (preinstalled nvm), matching CI, starts `dockerd` where macOS would run `colima start`, and then delegates to `make bootstrap`.
- `scripts/doctor.sh` requires Colima only on macOS; everywhere it requires a reachable Docker daemon.
- CI and cloud sessions set `TURACO_REQUIRE_DB_TESTS=true`, so database tests fail instead of skipping when PostgreSQL is unavailable.

No production code, configuration or container image changes. No secrets are required; `.env` is created from `.env.example`.

## Consequences

- Fresh cloud sessions are validated with the same `make check` as local machines and CI.
- Cloud-specific logic is confined to two small scripts; changes to bootstrap steps belong in the shared scripts, not the adapter.
- The adapter depends on the cloud image providing `dockerd` and nvm; if either disappears, bootstrap fails loudly rather than degrading.
