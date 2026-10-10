<p align="center">
  <img src="design/icon/preview/default-1024.png" alt="Turaco app icon" width="128" height="128">
</p>

<h1 align="center">Turaco</h1>

<p align="center">
  <strong>One connected workspace for IT operations.</strong><br>
  Service desk, assets, inventory, procurement, endpoint intelligence, infrastructure and change, security advisories and a computed IT briefing around one canonical model of your organization.
</p>

<p align="center">
  <a href="https://magicalwig34653.github.io/turaco/">Website</a> ·
  <a href="https://magicalwig34653.github.io/turaco/docs/README.html">Documentation</a> ·
  <a href="docs/product/current-status.md">Current status</a> ·
  <a href="docs/product/implementation-plan.md">Roadmap</a> ·
  <a href="docs/architecture/constitution.md">Principles</a>
</p>

<p align="center">
  <a href="https://github.com/MagicalWig34653/turaco/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/MagicalWig34653/turaco/actions/workflows/ci.yml/badge.svg"></a>
  <a href="https://github.com/MagicalWig34653/turaco/actions/workflows/security.yml"><img alt="Security" src="https://github.com/MagicalWig34653/turaco/actions/workflows/security.yml/badge.svg"></a>
  <img alt="Go" src="https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white">
  <img alt="React" src="https://img.shields.io/badge/React-TypeScript-3178C6?logo=react&logoColor=white">
  <img alt="PostgreSQL" src="https://img.shields.io/badge/PostgreSQL-18-336791?logo=postgresql&logoColor=white">
  <img alt="Status" src="https://img.shields.io/badge/status-early%20build-orange">
</p>

> **Early build, built in the open.** Turaco is an open-source-first project in active development. Read [`docs/product/current-status.md`](docs/product/current-status.md) before assuming a capability exists. The name and branding are subject to trademark clearance ([`docs/product/name-clearance.md`](docs/product/name-clearance.md)); no distribution license has been granted yet ([`docs/product/open-source-strategy.md`](docs/product/open-source-strategy.md)).

## What it does

| Area                          | What exists today                                                                                                                                                                                            |
| ----------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **Work**                      | Tasks, My Work, notifications (in-app and email), recurring work, manual IT briefing items                                                                                                                   |
| **Requests**                  | Product catalog, request lifecycles, approvals with separation of duties                                                                                                                                     |
| **Service desk**              | Tickets in queues with their own numbering (IT-1042 style) and aliases, major incidents, problems and known errors, knowledge articles, runbooks as tracked task lists, Autotask sync port                   |
| **Assets and inventory**      | Asset lifecycle and assignment, warehouses, stock ledger, reservations, procurement, goods receipt                                                                                                           |
| **Endpoint intelligence**     | Devices, installed software, findings, management artifacts and assignments, _Assigned vs Expected vs Observed_, assignment paths, history and diff                                                          |
| **Infrastructure and change** | Buildings, racks and placements, VMs, services with impact analysis, changes with approvals, planning and a maintenance calendar                                                                             |
| **Security**                  | Advisories, vulnerability findings with confidence, remediation tracking, risk acceptance, residual risk                                                                                                     |
| **Briefing**                  | A computed feed from all of the above, filtered by what you may see                                                                                                                                          |
| **Workbench**                 | Click-together filter builder (operators, AND/OR) over tickets, devices and tasks, saved and shared views, pins with a grouped collapsible sidebar, Cmd+K search, Kanban boards over tasks (early UI)        |
| **Administration**            | People, teams, locations and departments, role templates with an effective-permissions viewer, local accounts with invitations, runtime module switches; restricted external vendor accounts are in progress |
| **Platform**                  | Authentication and roles, audit trail, transactional outbox, jobs, permissions registry, OpenAPI, i18n (English and German), three themes plus Auto                                                          |

Roadmap F0 to F13 are implemented with documented gaps; F14 (administration: people, roles, health) is in progress. Nothing here is production-ready. Implemented with fake provider adapters: software lifecycle and patching (F9) and attended remote access with HopToDesk, RustDesk and AnyDesk launch connectors (F10). Workforce presence (F11, opt-in, privacy-first) is implemented for manual entries; Microsoft 365 and HR sources are planned. Turaco AI (F12) is implemented as a read-only, user-delegated assistant (fake and local Ollama-compatible providers); confirmed writes and an MCP server are planned. The Microsoft Graph (Intune read and assignment write) and Autotask REST clients are implemented per the vendor documentation and unverified against live services. Planned, not built: the OSV and MSRC advisory feeds, an MCP server, AI writes and external presence sources; their ports have fakes and imports. A hospital-IT simulation dataset (`turaco-admin demo seed-hospital`) and a load generator support usability and load tests as development tooling.

## Principles

- **Modular monolith.** Modules own their data; cross-module access goes through public contracts and events, enforced by `make archcheck`.
- **Explicit, audited operations.** No generic status updates; important mutations are audited in the same transaction; optimistic versions everywhere.
- **Honest data.** External data keeps its source and freshness. Assigned, Expected and Observed are never collapsed into one status; "unknown" is a first-class answer.
- **Least privilege.** Backend-enforced permissions, redaction instead of leaking, separate trust boundaries for agents.
- **Open-source first.** Self-hostable, boring technology, documentation as part of every change.

## Stack

Go 1.27 (pgx, explicit SQL) · React + TypeScript + Vite · PostgreSQL 18 · S3-compatible object storage · Docker (Colima locally, Swarm on Linux) · GitHub Actions and GHCR. The interface ships three themes (Turaco, Dark, Cyberpunk) plus an Auto mode that follows your OS.

## Quick start (macOS)

The local workflow is optimized for Apple Silicon macOS. Go, Node and Claude Code run natively; stateful development dependencies run in Colima.

```bash
brew bundle
colima start --cpu 6 --memory 12 --disk 80
make bootstrap
```

Then use three terminals:

```bash
make api
make worker
make frontend
```

Open <http://localhost:5173>. The frontend proxies `/api` to `turaco-api` on port 8080.

For the full setup, see `docs/development/local-development.md`. To install Turaco for a trial or a real deployment, see `docs/operations/installation.md`.

## Claude Code

For substantial work, the recommended lead session is Claude Opus 5.5:

```bash
claude --model claude-opus-5-5
```

Turaco deliberately routes well-scoped implementation work to Sonnet 5.5 and judgment-heavy architecture/security/domain work to Opus 5.5 through project skills and subagents. Start a substantial feature with:

```text
/feature-design <feature description>
```

See:

- `docs/development/claude-code.md`
- `docs/development/ai-orchestration.md`
- `docs/development/context-management.md`

Do **not** run `/init` to replace the curated `CLAUDE.md`.

## Quality gate

```bash
make check
```

This checks formatting, Go tests/vet, frontend lint/typecheck/tests, architecture boundaries, docs and generated references. `make build` performs the full production-style compile/build.

## Product documentation

Start with:

1. `docs/product/vision.md`
2. `docs/architecture/constitution.md`
3. `docs/domain/glossary.md`
4. `docs/architecture/system-architecture.md`
5. `docs/domain/core-data-model.md`
6. `docs/domain/state-machines.md`

## Open-source direction

Turaco is intended to be a genuinely useful self-hosted product, with official managed hosting/support as the commercial model rather than artificial feature crippling. The preferred license direction is **AGPL-3.0-or-later**, but no public distribution license is granted by this bootstrap repository until the license decision receives legal review and an actual `LICENSE` file is committed.

See `docs/product/open-source-strategy.md`.
