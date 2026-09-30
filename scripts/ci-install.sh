#!/usr/bin/env bash
set -euo pipefail
go mod download
./scripts/npm-install.sh
