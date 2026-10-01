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
  # The test database is created with the PostgreSQL user of TEST_DATABASE_URL
  # through the local compose PostgreSQL (the development infrastructure);
  # against another server, create the database yourself before migrating.
  db="$(sed -E 's|^[^/]*//[^/]*/([^?]*).*|\1|' <<<"$TEST_DATABASE_URL")"
  user="$(sed -E 's|^[^/]*//([^:@/]*).*|\1|' <<<"$TEST_DATABASE_URL")"
  if [[ ! "$db" =~ ^[a-z_][a-z0-9_]*$ || ! "$user" =~ ^[a-z_][a-z0-9_]*$ ]]; then
    echo "migrate: unexpected database or user name in TEST_DATABASE_URL" >&2
    exit 1
  fi
  if ./scripts/compose.sh -f deploy/compose/dev.yaml ps --status running postgres 2>/dev/null | grep -q postgres; then
    if ! ./scripts/compose.sh -f deploy/compose/dev.yaml exec -T postgres \
        psql -U "$user" -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname = '$db'" | grep -q 1; then
      ./scripts/compose.sh -f deploy/compose/dev.yaml exec -T postgres psql -U "$user" -d postgres -qc "CREATE DATABASE $db"
    fi
  fi
  DATABASE_URL="$TEST_DATABASE_URL" go run ./backend/cmd/turaco-migrate
fi
