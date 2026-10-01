#!/usr/bin/env bash
# Apply migrations to DATABASE_URL and, when TEST_DATABASE_URL is set (local
# and cloud development via .env), create and migrate the separate test
# database so tests never read or write development data. CI sets no
# TEST_DATABASE_URL and tests its fresh DATABASE_URL directly.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
go run ./backend/cmd/turaco-migrate
if [[ -n "${TEST_DATABASE_URL:-}" && "${TEST_DATABASE_URL}" != "${DATABASE_URL:-}" ]]; then
  db="$(sed -E 's|^[^/]*//[^/]*/([^?]*).*|\1|' <<<"$TEST_DATABASE_URL")"
  if [[ ! "$db" =~ ^[a-z_][a-z0-9_]*$ ]]; then
    echo "migrate: unexpected test database name in TEST_DATABASE_URL" >&2
    exit 1
  fi
  if ! ./scripts/compose.sh -f deploy/compose/dev.yaml exec -T postgres \
      psql -U turaco -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname = '$db'" | grep -q 1; then
    ./scripts/compose.sh -f deploy/compose/dev.yaml exec -T postgres psql -U turaco -d postgres -qc "CREATE DATABASE $db"
  fi
  DATABASE_URL="$TEST_DATABASE_URL" go run ./backend/cmd/turaco-migrate
fi
