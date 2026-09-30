#!/usr/bin/env bash
set -euo pipefail
fail=0
check_required() {
  local command_name="$1"
  local hint="$2"
  if command -v "$command_name" >/dev/null 2>&1; then
    printf '✓ %-12s %s\n' "$command_name" "$($command_name --version 2>/dev/null | head -1 || true)"
  else
    printf '✗ %-12s missing (%s)\n' "$command_name" "$hint"
    fail=1
  fi
}
check_optional() {
  local command_name="$1"
  local hint="$2"
  if command -v "$command_name" >/dev/null 2>&1; then
    printf '✓ %-12s %s\n' "$command_name" "$($command_name --version 2>/dev/null | head -1 || true)"
  else
    printf '· %-12s optional (%s)\n' "$command_name" "$hint"
  fi
}

printf 'Local development doctor\n\n'
check_required git 'brew install git'
check_required go 'brew install go'
check_required node 'brew install node@24'
check_required npm 'installed with Node.js'
check_required docker 'brew install docker colima'
check_required colima 'brew install colima'
check_required jq 'brew install jq'
check_optional gh 'brew install gh'
check_optional shellcheck 'brew install shellcheck'

if command -v docker >/dev/null 2>&1; then
  if docker info >/dev/null 2>&1; then
    echo '✓ docker daemon reachable'
  else
    echo '✗ docker daemon is not reachable (run: colima start --cpu 6 --memory 12 --disk 80)'
    fail=1
  fi
  if docker compose version >/dev/null 2>&1 || command -v docker-compose >/dev/null 2>&1; then
    echo '✓ Docker Compose available'
  else
    echo '✗ Docker Compose is unavailable'
    fail=1
  fi
fi

if command -v go >/dev/null 2>&1; then
  gov="$(go env GOVERSION 2>/dev/null || true)"
  case "$gov" in
    go1.27*|go1.28*|go1.29*|go1.3[0-9]*) ;;
    *) echo "✗ Go 1.27+ required; found ${gov:-unknown}"; fail=1 ;;
  esac
fi
if command -v node >/dev/null 2>&1; then
  major="$(node -p 'process.versions.node.split(`.`)[0]' 2>/dev/null || echo 0)"
  if (( major < 24 || major >= 27 )); then
    echo "✗ Node 24-26 required; found $(node --version)"
    fail=1
  fi
fi

if [[ "$(uname -s)" == 'Darwin' ]]; then
  echo "✓ macOS architecture: $(uname -m)"
fi

exit "$fail"
