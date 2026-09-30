#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../frontend"
if [[ -f package-lock.json ]]; then
  npm ci
else
  npm install
  echo
  echo 'Created frontend/package-lock.json. Commit it with the first bootstrap change.'
fi
