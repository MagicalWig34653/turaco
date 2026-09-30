#!/usr/bin/env bash
set -euo pipefail
files="$(find backend agents tools -type f -name '*.go' -print)"
[[ -z "$files" ]] && exit 0
unformatted="$(gofmt -l $files)"
if [[ -n "$unformatted" ]]; then
  echo 'The following Go files are not gofmt formatted:' >&2
  echo "$unformatted" >&2
  exit 1
fi
