# Repository Bootstrap

This repository is intentionally delivered before a final GitHub organization/repository name is known.

## 1. Choose the GitHub repository path

Choose the private repository owner/name, but do not push the initial tree until dependency lockfiles have been generated.

## 2. Go module path

The module path is currently:

```text
github.com/MagicalWig34653/turaco
```

It was set with `scripts/set-module-path.sh` (the original placeholder was `github.com/your-org/turaco`). If the repository path changes, use the script instead of editing imports by hand:

```bash
./scripts/set-module-path.sh github.com/YOUR-ORG/YOUR-REPO
```

The script updates `go.mod` and bootstrap Go imports consistently. Run the connected bootstrap next.

Record the final path in the first repository commit/PR. It is not a product architecture decision and needs no ADR.

## 3. Connected bootstrap

Run:

```bash
brew bundle
colima start --cpu 6 --memory 12 --disk 80
make bootstrap
```

The first connected run creates dependency lockfiles that cannot be generated in an offline artifact environment:

- `go.sum`
- `frontend/package-lock.json`

Commit both immediately. Subsequent installs use the lockfiles.

Initialize Git and push the first commit only after `make check` and `make build` pass.

## 4. Protect `main`

Recommended GitHub rules:

- pull requests required,
- `CI / quality` required,
- security checks according to organization policy,
- direct pushes disabled,
- force pushes disabled,
- branch deletion disabled.

## 5. Configure GHCR

The container workflow publishes release images below:

```text
ghcr.io/<owner>/<repo>/turaco-api
ghcr.io/<owner>/<repo>/turaco-worker
ghcr.io/<owner>/<repo>/turaco-web
```

## 6. Start Claude Code

Start Claude Code in the repository root. A fresh session must read `CLAUDE.md` before implementation. Use repository skills such as `/feature-design` before substantial changes.
