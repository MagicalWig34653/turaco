#!/usr/bin/env bash
set -euo pipefail
input="$(cat)"
file="$(printf '%s' "$input" | jq -r '.tool_input.file_path // ""')"
[ -n "$file" ] || exit 0
[ -f "$file" ] || exit 0

case "$file" in
  *.go)
    command -v gofmt >/dev/null 2>&1 && gofmt -w "$file" || true
    ;;
  *.ts|*.tsx|*.js|*.jsx|*.json|*.css)
    if [ -x "${CLAUDE_PROJECT_DIR}/frontend/node_modules/.bin/prettier" ]; then
      (cd "${CLAUDE_PROJECT_DIR}/frontend" && ./node_modules/.bin/prettier --write "${file#${CLAUDE_PROJECT_DIR}/frontend/}") >/dev/null 2>&1 || true
    fi
    ;;
esac
exit 0
