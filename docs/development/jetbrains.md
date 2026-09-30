# JetBrains / Air

No IDE is required by the repository.

## GoLand / IntelliJ family

GoLand is a good human inspection/debugging environment for the Go backend while Claude Code works in the same checkout or a worktree. Do not commit `.idea/` because local IDE state is not project architecture.

Recommended actions:
- open repository root, not only `backend/`;
- use the repository Go SDK from `.go-version`;
- configure frontend as the existing `frontend/package.json` project;
- run canonical Make targets rather than IDE-only build configurations where possible.

## JetBrains Air

Air can be useful as an agent-oriented review/orchestration surface, but it is optional. Claude Code remains repository-compatible from terminal/IDE. If using Air with Colima, verify normal `docker` commands work in the terminal first; do not introduce Air-specific build assumptions into project files.

The source of truth remains Git + repository rules/tests/docs, not IDE or Air session state.
