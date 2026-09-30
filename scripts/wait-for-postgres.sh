#!/usr/bin/env bash
set -euo pipefail
for i in {1..45}; do
  if ./scripts/compose.sh -f deploy/compose/dev.yaml exec -T postgres pg_isready -U turaco -d turaco >/dev/null 2>&1; then
    exit 0
  fi
  sleep 1
done
echo 'PostgreSQL did not become ready in time.' >&2
exit 1
