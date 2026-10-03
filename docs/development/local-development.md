# Local Development — macOS / Apple Silicon / Colima

Target developer machine: Apple Silicon macOS, including an M4-class Mac with ample memory.

## Principle

Run fast-edit tools natively:
- Claude Code
- Go compiler/tests
- Node/Vite/TypeScript
- IDE

Run infrastructure in Colima:
- PostgreSQL
- S3Mock

This avoids slow rebuild containers for normal code changes while keeping infrastructure reproducible.

## Required tools

- Git
- Go 1.27.x
- Node 24 LTS + npm
- Colima
- Docker CLI + Docker Compose plugin/command
- jq (used by Claude hooks)
- optional: GitHub CLI (`gh`), shellcheck
- Claude Code

`Brewfile` lists convenient Homebrew packages. You may use other version managers; the repository does not depend on Homebrew.

## Colima recommendation

A small VM is sufficient for current dependencies. Example starting point:

```bash
colima start --cpu 6 --memory 12 --disk 80
```

The M4 Pro/48GB host can allocate more, but there is no benefit in starving native IDE/Claude/compilers. Increase only when integration tests or additional services justify it.

Verify:

```bash
docker version
docker compose version
```

## Bootstrap

```bash
brew bundle
colima start --cpu 6 --memory 12 --disk 80
make bootstrap
```

`make bootstrap` creates `.env` when missing, downloads Go modules, installs frontend dependencies, starts PostgreSQL/S3Mock, waits for PostgreSQL, applies migrations and regenerates reference documentation. On the first connected bootstrap it also creates `go.sum` and `frontend/package-lock.json`; commit both immediately.

## Local test instance (one click)

`make dev-setup` (or the GoLand run configuration **Turaco Dev Setup**) starts PostgreSQL and S3Mock, migrates the development and test databases and creates a development administrator through the emergency account: login `devadmin`, password `turaco-dev-password` (public, development only; override with `DEV_ADMIN_LOGIN` and `DEV_ADMIN_PASSWORD`). It also runs `turaco-admin demo seed`, which creates sample manufacturers, categories, products and four example catalog items (notebook, software, system access, new workplace; manager approval, unassigned fulfillment tasks) through the audited application operations. The seed refuses to run unless `APP_ENV=development` and is idempotent. It is safe to run repeatedly.

The shared GoLand run configurations live in `.idea/runConfigurations/`:

| Configuration | Purpose |
|---|---|
| Turaco Dev Environment | Compound: setup, API, worker and web UI together; open http://localhost:5173 and sign in with the emergency account |
| Turaco Dev Setup / API / Worker / Web | the individual parts |
| Turaco Go Tests | all Go tests against the `turaco_test` database with `TURACO_REQUIRE_DB_TESTS=true` (run Dev Setup once first so the test database exists) |
| Turaco Web Tests | frontend unit tests |
| Turaco Quality Gate (make check) | the full local gate |

Email is off unless `SMTP_HOST` is set; to try it locally point `SMTP_HOST`, `SMTP_PORT`, `SMTP_SECURITY=none`, `SMTP_ALLOW_PLAINTEXT=true`, `SMTP_FROM` and `EMAIL_BASE_URL=http://localhost:5173` on the worker at a local test relay such as MailHog.

## Development processes

Use separate terminals:

```bash
make api
make worker
make frontend
```

- API: `http://localhost:8080`
- Vite UI: `http://localhost:5173`
- PostgreSQL: `localhost:5432`
- S3Mock: `http://localhost:9090`

The frontend proxies `/api` to the API.

## Full validation

```bash
make check
```

`make test` loads `.env`, so PostgreSQL-backed tests run whenever the local database is up and skip otherwise. Tests use the separate database `turaco_test` (`TEST_DATABASE_URL`), which `make bootstrap`/`make migrate` create and migrate next to the development database, so development data and test data never mix. Set `TURACO_REQUIRE_DB_TESTS=true` to make an unavailable database a test failure, as CI and Claude Code cloud sessions do.

Claude Code cloud sessions use this same environment through an automatic adapter; see [Claude Code Cloud Development](cloud-development.md).

## Environment

`.env` is ignored. `.env.example` contains non-secret examples only. Never place real customer/production credentials into the repository.

## Real integrations

Normal local development should not require production LDAP/Intune/Autotask. Use local fakes/fixtures/contract tests. Explicit integration testing against real systems belongs in controlled environments with dedicated test credentials.
