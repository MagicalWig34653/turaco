#!/usr/bin/env bash
# LAB ONLY: provisions domain LAB.TURACO.TEST on first start, then runs samba in the foreground.
set -euo pipefail
REALM="${LAB_REALM:-LAB.TURACO.TEST}"
DOMAIN="${LAB_DOMAIN:-LAB}"
ADMIN_PASSWORD="${LAB_ADMIN_PASSWORD:?}"

if [ ! -f /var/lib/samba/private/sam.ldb ]; then
  rm -f /etc/samba/smb.conf
  samba-tool domain provision --use-rfc2307 --realm="$REALM" --domain="$DOMAIN" \
    --server-role=dc --dns-backend=SAMBA_INTERNAL --adminpass="$ADMIN_PASSWORD" \
    --option="dns forwarder=127.0.0.11" --option="ldap server require strong auth = no" --option="posix:eadb = /var/lib/samba/eadb.tdb"
  cp /etc/samba/smb.conf /var/lib/samba/smb.conf.lab
fi
# Config lives in the container layer; restore it when only the container was recreated.
cp /var/lib/samba/smb.conf.lab /etc/samba/smb.conf
# LAB ONLY: allow simple binds over plain LDAP (port 389) so Turaco can use LDAP_ALLOW_PLAINTEXT in development.
grep -q "ldap server require strong auth" /etc/samba/smb.conf \
  || sed -i 's/^\[global\]$/[global]\n\tldap server require strong auth = no/' /etc/samba/smb.conf

# LAB ONLY: own CA and LDAPS certificate valid for localhost, so Turaco can verify it (LDAP_CA_FILE).
LABTLS=/var/lib/samba/lab-tls
if [ ! -f "$LABTLS/cert.pem" ]; then
  mkdir -p "$LABTLS"
  openssl req -x509 -newkey rsa:2048 -nodes -days 3650 -subj "/CN=Turaco Lab CA" \
    -keyout "$LABTLS/ca.key" -out "$LABTLS/ca.pem" 2>/dev/null
  openssl req -newkey rsa:2048 -nodes -subj "/CN=localhost" -keyout "$LABTLS/key.pem" -out "$LABTLS/req.csr" 2>/dev/null
  printf 'subjectAltName=DNS:localhost,DNS:samba-ad,DNS:dc1,DNS:dc1.lab.turaco.test,IP:127.0.0.1' >"$LABTLS/san.ext"
  openssl x509 -req -in "$LABTLS/req.csr" -CA "$LABTLS/ca.pem" -CAkey "$LABTLS/ca.key" -CAcreateserial \
    -days 3650 -extfile "$LABTLS/san.ext" -out "$LABTLS/cert.pem" 2>/dev/null
fi
grep -q "tls certfile" /etc/samba/smb.conf \
  || sed -i 's|^\[global\]$|[global]\n\ttls enabled = yes\n\ttls certfile = '"$LABTLS"'/cert.pem\n\ttls keyfile = '"$LABTLS"'/key.pem\n\ttls cafile = '"$LABTLS"'/ca.pem|' /etc/samba/smb.conf
cp /var/lib/samba/private/krb5.conf /etc/krb5.conf

# Run samba, wait until LDAP answers, then provision lab users once.
samba --foreground --no-process-group &
SAMBA_PID=$!
for _ in $(seq 1 60); do
  ldapsearch -x -H ldap://127.0.0.1 -s base -b "" namingContexts >/dev/null 2>&1 && break
  sleep 1
done
if [ ! -f /var/lib/samba/.lab-provisioned ]; then
  /usr/local/bin/provision-users.sh
  touch /var/lib/samba/.lab-provisioned
fi
wait "$SAMBA_PID"
