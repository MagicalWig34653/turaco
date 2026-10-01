# Claude Code Cloud Development

Claude Code cloud sessions use the same development environment as local machines and CI (see ADR-0021). Only the way the toolchain and Docker are provided differs.

| Concern | macOS (local) | Claude Code cloud | CI |
| --- | --- | --- | --- |
| Go | Homebrew, 1.27.x | `GOTOOLCHAIN` from `.go-version` | `setup-go` from `.go-version` |
| Node | Homebrew `node@24` | nvm, major from `.nvmrc` | `setup-node` from `.nvmrc` |
| Docker | Colima | `dockerd` started in the container | GitHub service container |
| Infrastructure | `deploy/compose/dev.yaml` | `deploy/compose/dev.yaml` | PostgreSQL service |
| Bootstrap | `make bootstrap` | `make bootstrap` via SessionStart hook | `scripts/ci-install.sh` |
| Quality gate | `make check` | `make check` | `make check` |
| Database tests | run when `DATABASE_URL` reachable, else skipped | required | required |

## Automatic bootstrap

`.claude/hooks/session-start.sh` runs on every session start/resume. When `CLAUDE_CODE_REMOTE=true` it runs `scripts/cloud-bootstrap.sh`, which:

1. exports `GOTOOLCHAIN=go<.go-version>` for the session;
2. installs/selects the Node major from `.nvmrc` with the image's nvm and puts it on `PATH`;
3. starts `dockerd` if the daemon is not reachable (log: `/tmp/turaco-dockerd.log`);
4. exports `TURACO_REQUIRE_DB_TESTS=true`;
5. runs `make bootstrap` (doctor, `.env`, dependencies, PostgreSQL/S3Mock, migrations, generated docs).

Session variables are written to `CLAUDE_ENV_FILE`. Output goes to `/tmp/turaco-cloud-bootstrap.log`; on failure the hook prints the log tail. The script is idempotent and can be re-run manually:

```bash
CLAUDE_CODE_REMOTE=true ./scripts/cloud-bootstrap.sh
```

Outside cloud sessions the hook does nothing, and the script refuses to run; local machines keep using `make bootstrap`.

## Validation

```bash
make check
make build
```

Both run fully in cloud sessions, including PostgreSQL-backed tests.

## Not available in cloud sessions

- `make docker-build`: works, but image builds are slow and are verified by the container workflow; not part of the routine gate.
- `make dev` processes can run, but there is no browser on the developer's side; use tests or headless Chromium (Playwright) for UI checks.
- Colima-specific behaviour and Apple Silicon (arm64) builds can only be verified locally.
- GoLand/JetBrains integration (`.mcp.json`) is unavailable; the repository does not depend on it.
- Real LDAP/Intune/Autotask systems are unreachable, as in normal local development.

## Network

Outbound traffic goes through the session's egress proxy. Docker Hub, the Go module/toolchain proxy, npm and nodejs.org must be allowed by the environment's network policy for bootstrap to succeed.
