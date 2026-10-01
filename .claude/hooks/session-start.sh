#!/usr/bin/env bash
# SessionStart: bootstrap Claude Code cloud sessions; no-op everywhere else.
set -euo pipefail
[[ "${CLAUDE_CODE_REMOTE:-}" == 'true' ]] || exit 0

log=/tmp/turaco-cloud-bootstrap.log
if "${CLAUDE_PROJECT_DIR}/scripts/cloud-bootstrap.sh" >"$log" 2>&1; then
  echo "Turaco cloud bootstrap complete (log: $log). Validate with: make check"
else
  status=$?
  echo "Turaco cloud bootstrap FAILED (exit $status). Last lines of $log:"
  tail -n 30 "$log"
  exit "$status"
fi
