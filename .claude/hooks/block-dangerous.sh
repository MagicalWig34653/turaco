#!/usr/bin/env bash
set -euo pipefail

input="$(cat)"
command="$(printf '%s' "$input" | jq -r '.tool_input.command // ""')"

# Block commands with a high chance of destroying the repository or host state.
if printf '%s' "$command" | grep -Eqi '(^|[;&|[:space:]])(sudo[[:space:]]+)?rm[[:space:]]+-[^[:space:]]*r[^[:space:]]*f[[:space:]]+(/|~|\$HOME|\.)($|[[:space:]])|git[[:space:]]+reset[[:space:]]+--hard|git[[:space:]]+clean[[:space:]]+-[^[:space:]]*f|docker[[:space:]]+system[[:space:]]+prune[[:space:]]+-a'; then
  jq -n '{hookSpecificOutput:{hookEventName:"PreToolUse",permissionDecision:"deny",permissionDecisionReason:"Potentially destructive command blocked by repository hook. Use a narrower, reversible command."}}'
  exit 0
fi

exit 0
