#!/usr/bin/env bash
# Bootstrap Turaco inside a Claude Code cloud session (CLAUDE_CODE_REMOTE=true).
#
# This only adapts the cloud container to the toolchain the repository already
# requires, then delegates to the normal `make bootstrap`. It does not replace
# or bypass any local step. See docs/development/cloud-development.md.
#
# Idempotent: safe to run on every session start/resume.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ "${CLAUDE_CODE_REMOTE:-}" != 'true' ]]; then
  echo 'cloud-bootstrap: CLAUDE_CODE_REMOTE is not true; use `make bootstrap` locally.' >&2
  exit 1
fi

# Variables for the rest of the session. Claude Code sources CLAUDE_ENV_FILE
# before each Bash command; without it (manual run) we only affect this script.
env_file="${CLAUDE_ENV_FILE:-/dev/null}"
persist() {
  export "$1=$2"
  printf 'export %s=%q\n' "$1" "$2" >>"$env_file"
}

# 1. Go: the exact toolchain CI uses (.go-version), fetched by the go command.
persist GOTOOLCHAIN "go$(tr -d '[:space:]' <.go-version)"

# 2. Node: the major version from .nvmrc via the image's preinstalled nvm,
#    the same selection semantics as actions/setup-node in CI.
node_major="$(tr -d '[:space:]' <.nvmrc)"
current_major="$(node -p 'process.versions.node.split(`.`)[0]' 2>/dev/null || echo 0)"
if [[ "$current_major" != "$node_major" ]]; then
  base_path="$PATH"
  export NVM_DIR="${NVM_DIR:-/opt/nvm}"
  if [[ ! -s "$NVM_DIR/nvm.sh" ]]; then
    echo "cloud-bootstrap: Node $node_major required and nvm not found at $NVM_DIR" >&2
    exit 1
  fi
  # shellcheck disable=SC1091
  set +u && source "$NVM_DIR/nvm.sh" && nvm install "$node_major" >/dev/null && set -u
  persist PATH "$(dirname "$(nvm which "$node_major")"):$base_path"
fi

# 3. Docker: the cloud image ships the engine but does not start it. This
#    replaces `colima start` on macOS; Compose files stay identical.
if ! docker info >/dev/null 2>&1; then
  if ! command -v dockerd >/dev/null 2>&1; then
    echo 'cloud-bootstrap: Docker daemon unavailable and dockerd not installed' >&2
    exit 1
  fi
  # A container restored from a snapshot can carry a stale PID file, which
  # makes dockerd refuse to start.
  if [[ -f /var/run/docker.pid ]] && ! kill -0 "$(cat /var/run/docker.pid)" 2>/dev/null; then
    rm -f /var/run/docker.pid
  fi
  if ! pgrep -x dockerd >/dev/null 2>&1; then
    echo "--- $(date -u +%FT%TZ) starting dockerd" >>/tmp/turaco-dockerd.log
    nohup dockerd >>/tmp/turaco-dockerd.log 2>&1 &
  fi
  for _ in {1..120}; do
    docker info >/dev/null 2>&1 && break
    sleep 1
  done
  docker info >/dev/null 2>&1 || {
    echo 'cloud-bootstrap: dockerd did not start; last log lines:' >&2
    tail -n 20 /tmp/turaco-dockerd.log >&2
    exit 1
  }
fi

# 4. Database tests must run, not skip, in this environment (as in CI).
persist TURACO_REQUIRE_DB_TESTS true

# 5. The regular bootstrap: doctor, .env, dependencies, infrastructure,
#    migrations and generated reference docs.
make bootstrap
