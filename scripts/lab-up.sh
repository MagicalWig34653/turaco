#!/usr/bin/env bash
# Starts the optional lab services and waits until every container is healthy.
set -euo pipefail
cd "$(dirname "$0")/.."
./scripts/compose.sh -f deploy/compose/lab.yaml up -d --build
deadline=$((SECONDS + 240))
for svc in mailpit samba-ad keycloak; do
  cid=$(./scripts/compose.sh -f deploy/compose/lab.yaml ps -q "$svc")
  while [ "$(docker inspect -f '{{.State.Health.Status}}' "$cid")" != "healthy" ]; do
    if [ "$SECONDS" -gt "$deadline" ]; then echo "lab: $svc not healthy in time" >&2; exit 1; fi
    sleep 3
  done
  echo "lab: $svc healthy"
done
echo "lab ready: Mailpit UI http://localhost:8025, Keycloak http://localhost:8180, LDAP ldap://localhost:1389. See docs/development/lab-services.md"
