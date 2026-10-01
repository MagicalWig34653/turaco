#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

./scripts/doctor.sh

if [[ ! -f .env ]]; then
  cp .env.example .env
  echo 'Created .env from .env.example'
fi

if [[ ! -f go.sum ]]; then
  go mod tidy
  echo 'Created go.sum. Commit it with the first bootstrap change.'
else
  go mod download
fi

./scripts/npm-install.sh

./scripts/compose.sh -f deploy/compose/dev.yaml up -d
./scripts/wait-for-postgres.sh
./scripts/with-env.sh ./scripts/migrate.sh
go run ./backend/cmd/turaco-docgen

echo
echo 'Bootstrap complete.'
echo 'Run: make dev'
