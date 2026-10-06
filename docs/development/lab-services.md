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
3. `GET /api/v1/directory-sync-runs` should show a succeeded run observing the 12 users and 23 groups (the 6 `GRP-*` groups plus the 17 built-in AD groups of `CN=Users`, e.g. Domain Admins; the service account lies in `CN=Users`, outside the user base).
4. Expected: 11 active Users and `jonas.sales` `inactive` (disabled account), as designed in the sync design; `GRP-IT-Helpdesk` nested in `GRP-IT`, and the department groups nested in `GRP-All-Staff`.
5. Password login: `POST /api/v1/auth/login` with `{"identifier":"bob.helpdesk","password":"Lab-Only-Passw0rd!"}` (directory-verified bind). API calls with a cookie need an `Origin` header equal to the API host (CSRF check), e.g. `-H 'Origin: http://localhost:18090'` with curl.
6. Change something in the directory and re-run: `docker exec turaco-lab-samba-ad-1 samba-tool user disable kira.it` (undo with `enable`), `samba-tool user create ...`, `samba-tool group addmembers ...`.

Without Turaco, `./scripts/lab-verify.sh` checks the directory with a throwaway `ldap-utils` container.

### Verified against Turaco on 2026-10-07

Throwaway database `turaco_lab`, `turaco-api` on `:18090` and `turaco-worker`, `APP_ENV=development`, `AUTH_EMERGENCY_LOGIN_ENABLED=true`, the environments above and the SMTP variables from "Mail"; emergency admin created with `turaco-admin emergency create/enable` plus `role grant platform-administrator`.

- Passed: first sync (`POST /api/v1/directory-sync-runs`, run succeeded): 12 users created (11 `active`, `jonas.sales` `inactive`), 23 groups, 12 direct memberships, 12 nesting edges; `GRP-IT-Helpdesk` is nested in `GRP-IT` and the four department groups in `GRP-All-Staff`. `unresolvedMembers: 5` (built-in members outside the user base) is normal.
- Passed: change detection. After `samba-tool user disable kira.it` and removing `carla.support` from `GRP-IT-Helpdesk`, the next run reported `usersDeactivated: 1`, `membershipsClosed: 1`; after enabling and re-adding it reported `usersActivated: 1`, `membershipsOpened: 1`.
- Passed: LDAPS with certificate verification (`LDAP_URL=ldaps://localhost:1636`, `LDAP_CA_FILE` = exported lab CA, no `LDAP_ALLOW_PLAINTEXT`): sync and password login.
- Passed: password login for `bob.helpdesk` (username), `hans.hr@lab.turaco.test` (email) and `LAB\eva.finance` (domain form; session `authMethod: ldap`); wrong password and the disabled `jonas.sales` are rejected with 401.
- Passed: mail. Assigning a Task to `bob.helpdesk` (who needs an active Role with `tasks.*`) produced "Task assigned: Lab mail test" in Mailpit within 20 s.
- Not verified: Kerberos/SPNEGO, scheduled sync interval (only the first scheduled run and manual runs were seen).
- Found while verifying (fixed): `turaco-worker` exited at startup with `timeout 45m0s must be shorter than lock timeout 30m0s` for the advisory feed job; the worker now derives its job lock timeout from that job.

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
