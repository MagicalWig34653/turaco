---
name: release-readiness
description: Perform a judgment-heavy final Turaco release/change readiness review without publishing anything.
disable-model-invocation: true
model: claude-opus-5-5
effort: high
---
# Release Readiness

Review the intended release/change as an independent final gate.

1. Run/inspect `make check`.
2. Review architecture/domain/security/database findings.
3. Inspect migrations and backward/agent compatibility.
4. Verify docs/generated refs/current-status are accurate.
5. Verify no secrets/debug/temp artifacts/unrelated changes exist.
6. Check observability/backup/restore/operational implications.
7. Report blockers and non-blocking risks separately.

Do not push, tag, publish or deploy.
