#!/usr/bin/env bash
# Prepare a local test instance: start PostgreSQL/S3Mock, migrate the
# development and test databases, and create a development administrator.
# Safe to run repeatedly. Development only: the password below is public.
#
#   login:    devadmin
#   password: turaco-dev-password
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

LOGIN="${DEV_ADMIN_LOGIN:-devadmin}"
PASSWORD="${DEV_ADMIN_PASSWORD:-turaco-dev-password}"

[[ -f .env ]] || cp .env.example .env

./scripts/compose.sh -f deploy/compose/dev.yaml up -d
./scripts/wait-for-postgres.sh
./scripts/with-env.sh ./scripts/migrate.sh

admin() { ./scripts/with-env.sh go run ./backend/cmd/turaco-admin "$@"; }

if ! out="$(printf '%s' "$PASSWORD" | admin emergency create --login "$LOGIN" --display-name "Development Admin" --password-stdin 2>&1)"; then
  echo "$out" | grep -qi "exist" || { echo "$out" >&2; exit 1; }
  echo "Emergency account '$LOGIN' already exists."
else
  echo "$out"
fi
admin emergency enable --login "$LOGIN"

# The role grant needs the User id; read it from the database.
user_id="$(./scripts/compose.sh -f deploy/compose/dev.yaml exec -T postgres \
  psql -U turaco -d turaco -tAc "SELECT id FROM organization.users WHERE display_name = 'Development Admin' ORDER BY created_at LIMIT 1" | tr -d '[:space:]')"
if [[ -z "$user_id" ]]; then
  echo "Could not find the development admin user." >&2
  exit 1
fi
admin role grant --role platform-administrator --user "$user_id" 2>&1 | grep -v "already" || true

admin demo seed

cat <<MSG

Local test instance is ready.
  API      http://localhost:8080   (run 'Turaco API')
  Worker   background jobs         (run 'Turaco Worker')
  Web UI   http://localhost:5173   (run 'Turaco Web')
  Login    emergency account: $LOGIN / $PASSWORD
MSG
