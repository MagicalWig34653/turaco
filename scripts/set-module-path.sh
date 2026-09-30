#!/usr/bin/env bash
set -euo pipefail
if [[ $# -ne 1 ]]; then
  echo "Usage: $0 github.com/OWNER/REPOSITORY" >&2
  exit 2
fi
new="$1"
if [[ "$new" != github.com/*/* ]]; then
  echo 'Expected a GitHub module path such as github.com/OWNER/REPOSITORY.' >&2
  exit 2
fi
old="$(awk '$1 == "module" { print $2; exit }' go.mod)"
if [[ -z "$old" ]]; then
  echo 'Could not read current module path from go.mod.' >&2
  exit 1
fi
if [[ "$old" == "$new" ]]; then
  echo "Module path is already $new"
  exit 0
fi

go mod edit -module "$new"
while IFS= read -r file; do
  sed -i.bak "s|$old|$new|g" "$file"
  rm -f "$file.bak"
done < <(grep -rl --include='*.go' -- "$old" backend agents tools 2>/dev/null || true)

echo "Changed Go module path: $old -> $new"
echo 'Run make bootstrap (or go mod tidy) and then make check.'
