#!/usr/bin/env bash
set -euo pipefail
missing=0
for file in go.sum frontend/package-lock.json; do
  if [[ ! -s "$file" ]]; then
    echo "Missing dependency lockfile: $file (run make bootstrap on a connected developer machine)" >&2
    missing=1
  fi
done
exit "$missing"
