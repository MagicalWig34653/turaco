# Turaco

**Turaco** is an open-source-first IT operations platform for internal IT teams. It is designed to connect service delivery, assets, inventory, procurement, endpoint intelligence, infrastructure, knowledge, change work and security context around one canonical operational model.

> Project name status: **Turaco is the current repository and product name.** Public branding remains subject to formal trademark/name clearance before launch; see `docs/product/name-clearance.md`.

## Current status

Architecture/bootstrap repository. The foundation is intentionally small; business modules are implemented incrementally behind documented boundaries. Read `docs/product/current-status.md` before assuming a planned capability already exists.

## Baseline stack

- Go 1.27.x
- React + TypeScript + Vite
- PostgreSQL 18
- S3-compatible object storage
- Docker/Colima for local infrastructure
- Docker/Swarm for primary Linux deployment
- GitHub Actions + GHCR
- Claude Code as the primary implementation environment

## macOS quick start

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

For the full setup, see `docs/development/local-development.md`.

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
