# Lab Services (Mailpit, Samba AD, Keycloak)

Optional local replacements for external services, so Turaco's integrations can be tried without any external account. Part of step 2 of the [integration sandboxes](integration-sandboxes.md#practical-order) plan. Separate from the dev stack (`make infra-up`): own compose project `turaco-lab`, own network and volumes, every port bound to `127.0.0.1`.

**Lab only.** All passwords below are public lab values. Never reuse them anywhere real. Compose file: `deploy/compose/lab.yaml`; images and provisioning: `deploy/lab/`.

## Commands

```bash
make lab-up      # build/start Mailpit, Samba AD, Keycloak and wait until all are healthy (first start ~1 min)
make lab-down    # stop, keep data (the directory and users survive)
make lab-reset   # stop and delete all lab data (fresh directory on the next lab-up)
./scripts/lab-verify.sh   # smoke checks: LDAP/LDAPS query, Keycloak realm, test mail
```

Resources: about 1.5 GiB RAM in total (Keycloak dominates). Runs natively on arm64 (Colima); the Samba image is built locally from a pinned `debian:12.11-slim`.

| Service | Image | Host ports (127.0.0.1) | Credentials |
| --- | --- | --- | --- |
| Mailpit | `axllent/mailpit:v1.27.10` | SMTP 1025, UI http://localhost:8025 | none |
| Samba AD DC | `turaco-lab-samba-ad` (Debian 12 + Samba 4.17) | LDAP 1389, LDAPS 1636, Kerberos 1088 | see below |
| Keycloak | `quay.io/keycloak/keycloak:26.4.7` (`start-dev`) | http://localhost:8180, management 9100 | console admin / `lab-only-admin` |

The dev stack uses 5432 and 9090; Turaco uses 8080 and 5173. No clashes.

## Mail (worker)

Start the worker with:

```bash
export SMTP_HOST=localhost SMTP_PORT=1025 SMTP_SECURITY=none SMTP_ALLOW_PLAINTEXT=true \
       SMTP_FROM='Turaco <turaco@lab.turaco.test>' EMAIL_BASE_URL=http://localhost:5173
```

(`APP_ENV=development` is required for plaintext SMTP.) Sent mail appears in the Mailpit UI; nothing is delivered anywhere.

## Directory (Samba AD)

Domain `LAB.TURACO.TEST` (NetBIOS `LAB`). Provisioned once on first start by `deploy/lab/samba/provision-users.sh`.

- Read-only sync account: `svc-turaco@lab.turaco.test`, password `Lab-Only-Svc-Passw0rd!` (default authenticated-user read access only).
- Domain Administrator: `Administrator`, password `Lab-Only-Adm1n-Passw0rd!`.
- 12 users in `OU=Staff` (sub-OUs IT, Finance, Sales, HR), all with password `Lab-Only-Passw0rd!`, mail `<name>@lab.turaco.test`: `alice.admin`, `bob.helpdesk`, `carla.support`, `kira.it` (IT); `dieter.finance`, `eva.finance`, `leo.finance` (Finance); `frank.sales`, `gina.sales`, `jonas.sales` (Sales; **`jonas.sales` is disabled**, `userAccountControl` 66050); `hans.hr`, `ines.hr` (HR).
- Groups (in `CN=Users`): `GRP-IT` (alice.admin, kira.it, and the nested group `GRP-IT-Helpdesk` with bob.helpdesk, carla.support), `GRP-Finance`, `GRP-Sales`, `GRP-HR`, and `GRP-All-Staff`, which contains the four department groups (nesting two levels deep).

Turaco environment for `turaco-api` and `turaco-worker` (plain LDAP is accepted only in development):

```bash
mkdir -p .local/lab && printf '%s' 'Lab-Only-Svc-Passw0rd!' > .local/lab/ldap-bind-password   # .local/ is git-ignored
export APP_ENV=development LDAP_PROVIDER_KEY=lab-ad LDAP_DIRECTORY_TYPE=active_directory \
       LDAP_URL=ldap://localhost:1389 LDAP_ALLOW_PLAINTEXT=true \
       LDAP_BIND_DN='CN=svc-turaco,CN=Users,DC=lab,DC=turaco,DC=test' \
       LDAP_BIND_PASSWORD_FILE="$PWD/.local/lab/ldap-bind-password" \
       LDAP_USER_BASE_DN='OU=Staff,DC=lab,DC=turaco,DC=test' \
       LDAP_GROUP_BASE_DN='CN=Users,DC=lab,DC=turaco,DC=test'
```

LDAPS with certificate verification (as production requires): the container creates a lab CA and a certificate valid for `localhost`, `samba-ad`, `dc1`. Export the CA and use it:

```bash
docker cp turaco-lab-samba-ad-1:/var/lib/samba/lab-tls/ca.pem .local/lab/ca.pem
export LDAP_URL=ldaps://localhost:1636 LDAP_CA_FILE="$PWD/.local/lab/ca.pem"   # LDAP_ALLOW_PLAINTEXT no longer needed
```

Kerberos (KDC `localhost:1088`, realm `LAB.TURACO.TEST`): the DC runs a KDC and the realm is usable for the `KERBEROS_*` settings, but SPN/keytab creation and browser SPNEGO against `localhost` are **not** part of this lab and are unverified.

### Verification procedure

1. `make lab-up`, set the environment above, start `make api` and `make worker` (after `make dev-setup`).
2. Sign in as `devadmin`, request a run: `POST /api/v1/directory-sync-runs` (permission `organization.directory.sync`), or wait for `LDAP_SYNC_INTERVAL` (minimum 5m).
3. `GET /api/v1/directory-sync-runs` should show a succeeded run observing the 12 users and 6 groups (the service account lies in `CN=Users`, outside the user base).
4. Expected: 11 active Users and `jonas.sales` `inactive` (disabled account), as designed in the sync design (not yet run against this lab by the author); `GRP-IT-Helpdesk` nested in `GRP-IT`, and the department groups nested in `GRP-All-Staff`.
5. Password login: sign in as `bob.helpdesk` / `Lab-Only-Passw0rd!` (directory-verified bind).
6. Change something in the directory and re-run: `docker exec turaco-lab-samba-ad-1 samba-tool user disable kira.it` (undo with `enable`), `samba-tool user create ...`, `samba-tool group addmembers ...`.

Without Turaco, `./scripts/lab-verify.sh` checks the directory with a throwaway `ldap-utils` container.

## Identity provider (Keycloak)

Realm `turaco-lab` is imported from `deploy/lab/keycloak/turaco-lab-realm.json` at start (dev mode, in-container H2 database: data is discarded when the container is removed).

- Issuer / discovery: `http://localhost:8180/realms/turaco-lab/.well-known/openid-configuration`; admin console http://localhost:8180 (`admin` / `lab-only-admin`).
- Confidential client `turaco`, secret `lab-only-turaco-oidc-secret`, authorization-code flow with PKCE (S256), redirect URIs `http://localhost:5173/*` and `http://localhost:8080/*`. A `groups` claim (group names without path) is added by the `turaco-groups` scope.
- Users (all password `Lab-Only-Passw0rd!`, email verified): `alice.admin` (GRP-IT), `bob.helpdesk` (GRP-IT/GRP-IT-Helpdesk), `dieter.finance` (GRP-Finance), `jonas.sales` (disabled); all also in `GRP-All-Staff`.

**OIDC login is planned, not implemented** ([ADR-0013](../decisions/ADR-0013-authentication-abstraction.md)); the realm is provisioned so a future OIDC provider can be developed against it. Check the realm manually: request a token with the direct-grant flow and inspect the `groups` claim:

```bash
curl -s -d grant_type=password -d client_id=turaco -d client_secret=lab-only-turaco-oidc-secret \
  -d username=bob.helpdesk -d 'password=Lab-Only-Passw0rd!' -d scope=openid \
  http://localhost:8180/realms/turaco-lab/protocol/openid-connect/token
```

Keycloak is not connected to the Samba directory; the user and group names mirror it by hand.

## Troubleshooting

- Samba health stays unhealthy: `docker logs turaco-lab-samba-ad-1`. After changing `deploy/lab/samba/*` run `make lab-reset && make lab-up`, because provisioning happens once per volume.
- The Samba container stores NT ACLs in `/var/lib/samba/eadb.tdb` (`posix:eadb`) because extended attributes cannot be set on Docker volumes in Colima. This is a lab-only setting.
- Plain LDAP simple binds are allowed (`ldap server require strong auth = no`) for development; never copy this to a real directory.
