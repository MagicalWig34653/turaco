#!/usr/bin/env bash
# Smoke-checks the running lab services (make lab-up first). Needs docker and python3.
set -euo pipefail

echo "== Samba AD (LDAP via throwaway ldap-utils container) =="
CA=$(mktemp "$PWD/.lab-ca.XXXXXX"); trap 'rm -f "$CA"' EXIT
docker cp turaco-lab-samba-ad-1:/var/lib/samba/lab-tls/ca.pem "$CA"
docker run --rm --network turaco-lab_default -v "$CA":/ca.pem:ro debian:12.11-slim bash -c '
  apt-get update -qq >/dev/null && apt-get install -y -qq ldap-utils >/dev/null 2>&1
  B=(-x -LLL -H ldap://samba-ad -D svc-turaco@lab.turaco.test -w "Lab-Only-Svc-Passw0rd!")
  echo "users under OU=Staff: $(ldapsearch "${B[@]}" -b OU=Staff,DC=lab,DC=turaco,DC=test "(objectClass=user)" sAMAccountName | grep -c ^sAMAccountName)"
  ldapsearch "${B[@]}" -b DC=lab,DC=turaco,DC=test "(cn=GRP-IT*)" member | grep -v "^$"
  ldapsearch "${B[@]}" -b DC=lab,DC=turaco,DC=test "(sAMAccountName=jonas.sales)" userAccountControl | grep -i ^userAccountControl
  echo "ldaps (lab CA, hostname samba-ad):"; LDAPTLS_CACERT=/ca.pem ldapsearch -x -LLL -H ldaps://samba-ad:636 -D svc-turaco@lab.turaco.test -w "Lab-Only-Svc-Passw0rd!" -b DC=lab,DC=turaco,DC=test -s base dn | head -2'

echo "== Keycloak =="
curl -fsS http://localhost:8180/realms/turaco-lab/.well-known/openid-configuration | python3 -c 'import sys,json;print("issuer:",json.load(sys.stdin)["issuer"])'

echo "== Mailpit =="
python3 - <<'PY'
import smtplib
from email.message import EmailMessage
m = EmailMessage()
m["From"] = "Turaco <turaco@lab.turaco.test>"
m["To"] = "alice.admin@lab.turaco.test"
m["Subject"] = "lab smoke test"
m.set_content("hello from lab-verify")
with smtplib.SMTP("localhost", 1025, timeout=10) as s:
    s.send_message(m)
print("sent via localhost:1025")
PY
curl -fsS http://localhost:8025/api/v1/messages | python3 -c 'import sys,json;d=json.load(sys.stdin);print("mailpit messages:",d["total"])'
