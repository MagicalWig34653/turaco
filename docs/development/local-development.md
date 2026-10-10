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

`make dev-setup` (or the GoLand run configuration **Turaco Dev Setup**) starts PostgreSQL and S3Mock, migrates the development and test databases and creates a development administrator through the emergency account: login `devadmin`, password `turaco-dev-password` (public, development only; override with `DEV_ADMIN_LOGIN` and `DEV_ADMIN_PASSWORD`). It also runs `turaco-admin demo seed`, which creates sample manufacturers, categories, products four example catalog items (notebook, software, system access, new workplace; manager approval, unassigned fulfillment tasks), a main warehouse with two shelves and opening stock, six demo assets (notebooks, a monitor, a docking station), a supplier, an open procurement request and four help articles through the audited application operations. The seed refuses to run unless `APP_ENV=development` and is idempotent. It is safe to run repeatedly.

For usability tests there is a second, much larger dataset: `turaco-admin demo seed-hospital` creates a simulated hospital group with 31 local logins, teams, roles, 60 assets, 9 virtual machines, 6 IT services with dependencies, 3 changes with maintenance windows, 27 tickets, Known Errors, a Major Incident, knowledge, tasks, briefing items and catalog items. It follows the same rules (development only, idempotent, audited operations). Personas, passwords and test scripts are in [Hospital IT simulation](simulation-hospital.md).

The shared GoLand run configurations live in `.idea/runConfigurations/`:

| Configuration | Purpose |
|---|---|
| Turaco Dev Environment | Compound: setup, API, worker and web UI together; open http://localhost:5173 and sign in with the emergency account |
| Turaco Dev Setup / API / Worker / Web | the individual parts |
| Turaco Go Tests | all Go tests against the `turaco_test` database with `TURACO_REQUIRE_DB_TESTS=true` (run Dev Setup once first so the test database exists) |
| Turaco Web Tests | frontend unit tests |
| Turaco Quality Gate (make check) | the full local gate |

Email is off unless `SMTP_HOST` is set; to try it locally point `SMTP_HOST`, `SMTP_PORT`, `SMTP_SECURITY=none`, `SMTP_ALLOW_PLAINTEXT=true`, `SMTP_FROM` (and `EMAIL_BASE_URL` unless you rely on the development default `http://localhost:5173`) on the worker at a local test relay; the optional [lab services](lab-services.md) (`make lab-up`) provide Mailpit, a Samba AD directory and Keycloak with ready-made values.

## Configuration switches you need locally

The complete list is the generated [configuration reference](../reference/configuration.md); `.env.example` holds only the non-secret development defaults. The GoLand **Turaco API** run configuration sets `AUTH_EMERGENCY_LOGIN_ENABLED=true`; when you start the API with `make api` from a terminal, add it to `.env` (or the shell) yourself, otherwise the `devadmin` login of `make dev-setup` and every simulation persona answers 404.

| Variable | Local use |
|---|---|
| `AUTH_EMERGENCY_LOGIN_ENABLED=true` | Sign in as `devadmin` and as the simulation personas (they are emergency-style accounts). Never enable it on a shared environment without alerting on its audit trail. |
| `AUTH_LOCAL_LOGIN_ENABLED=true` (`EMAIL_BASE_URL` defaults to `http://localhost:5173` when `APP_ENV=development`; other environments must set it) | Try invitations and password resets of local accounts (People administration). Without a mail relay (`SMTP_*`, for example Mailpit from the [lab services](lab-services.md)) the invitation link of a never-activated account is shown once on screen; resets and later invitations need mail. |
| `PEOPLE_LOOKUP_ENABLED` (default `true`) | Colleague search when raising a ticket or request for someone else. |
| `PRESENCE_ENABLED=true`, `PRESENCE_RETENTION_DAYS` | Workforce Presence module (API and worker need the same value); then enable the runtime setting in Administration > Presence (it records the data protection dates) and the module in Administration > Modules. |
| `AI_ENABLED=true`, `AI_SECRET_DIR` | Turaco AI. Add an AI Provider in Administration > AI: the `fake` provider needs nothing, `openai_compatible` points at a local runtime such as Ollama and may name a secret file below `AI_SECRET_DIR`. |
| `REMOTE_ACCESS_PROVIDERS=rustdesk,anydesk,hoptodesk` | Launch-link connectors for attended Remote Access sessions (API and worker need the same value). |
| `SOFTWARE_PROVIDER_SYNC`, `SOFTWARE_DEPLOY_WRITE`, `INTUNE_SYNC`, `AUTOTASK_SYNC`, `ADVISORY_SYNC` | Integration switches; the Intune, IntuneGet and Autotask clients are not implemented, so these run against fakes or report "not configured" (see [current status](../product/current-status.md)). |

Optional modules can also be switched at runtime in Administration > Modules (`modules.manage`); a startup gate such as `PRESENCE_ENABLED` must be on before the switch can take effect.

## Simulation, load test and first-administrator walkthrough

- **Hospital simulation:** `make dev-setup`, then `./scripts/with-env.sh go run ./backend/cmd/turaco-admin demo seed-hospital` (idempotent, `APP_ENV=development` only). Personas, passwords and test scripts: [Hospital IT simulation](simulation-hospital.md).
- **Load test:** with the API running (`AUTH_EMERGENCY_LOGIN_ENABLED=true`) and the hospital seeded, `make load-test` runs the smoke profile; `make load-test LOADTEST_ARGS="--profile ramp --rate-scale 0.1"` ramps up. It only talks to a local development API. Details and safety rules: [Load testing](load-testing.md).
- **Administration walkthrough:** the steps an administrator takes after installation (first administrator, modules, people, roles from templates, Queues, Views) are in [Getting started as an administrator](../operations/administrator-getting-started.md); try them against a fresh `make dev-setup` database.

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

## Deployment write capability: API and worker

`SOFTWARE_DEPLOY_WRITE` must have the same value for the API and for the worker (`turaco-worker`). The API checks it when a person starts, resumes or promotes a Deployment; the worker needs it to resolve targets, write ring assignments and clear them. Hazards of a split:

- API on, worker off: a started Deployment stays in `resolving_targets` (the worker only runs the kill-switch sweep). The progress read reports `resolvingStuck` after 15 minutes; check the worker's environment first.
- API off, worker on: running Deployments continue, but nobody can start, resume or promote.
- Worker off while the kill switch or a cancel queued the clearing of ring assignments: the assignments stay at the provider until the worker runs with the capability on; the progress read reports `clearPending` for the Deployment and the ring.

Enabling the capability is audited once per process (`endpoints.deploy_write.enabled`). Change the value for both processes in the same deployment step.

## Real integrations

Normal local development should not require production LDAP/Intune/Autotask. Use local fakes/fixtures/contract tests. Explicit integration testing against real systems belongs in controlled environments with dedicated test credentials.
