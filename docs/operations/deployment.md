# Deployment

## Supported target shapes

Primary: Linux containers. Docker Swarm is a supported/preferred orchestrator for the initial on-prem design. Docker Compose is supported for small/single-node environments. Native Windows services may be provided for components where customers require Windows Server support; Windows containers are not a baseline requirement.

## Stateless compute

API/worker nodes have no required local persistent files. Persistent state is PostgreSQL, S3-compatible storage and external key/secrets material.

## Swarm guidance

Treat Swarm primarily as compute scheduling. Do not assume local Docker volumes are distributed storage. Database/object data require an explicit stateful design. Multi-site compute does not automatically provide multi-site database/storage HA.

For two-site deployments, prefer clear primary + DR semantics for state rather than pretending to have symmetrical active/active persistence. Swarm manager quorum and database/storage RPO/RTO are separate designs.

## Hosted design

Managed-service environments use isolated customer DB/object/key data planes. Compute may be shared or dedicated by tier, but authorization and data storage cannot depend solely on a forgotten `tenant_id` filter.

## Reverse proxy and client addresses

Browsers reach `turaco-api` through `turaco-web` (nginx) or another reverse proxy. Set `HTTP_TRUSTED_PROXIES` on the API to the proxy network so login throttling and audit see the real client address from `X-Forwarded-For`; untrusted senders of that header are ignored. Trust only the proxy's own address(es), not whole networks containing gateways or other containers. Without real client addresses every client appears as the proxy and shares one throttle counter, so 30 failed attempts from anyone would block password and emergency login for everyone for 15 minutes. In Docker Swarm the default `mode: ingress` replaces client addresses with the ingress network's; publish the proxy with `mode: host` (or use PROXY protocol on an external load balancer) when login throttling must see clients. Proxies must preserve the `Host` header (the same-origin CSRF guard compares it with `Origin`) and pass `Authorization`/`WWW-Authenticate` unchanged for Kerberos.

## Kerberos single sign-on

1. Create a dedicated service account in AD for Turaco (no delegation rights, not used for anything else) and register the SPN `HTTP/<turaco host name>` (the name users type in the browser), for example `setspn -S HTTP/turaco.example.local svc-turaco-http`.
2. Export a keytab for that principal with AES encryption types (`ktpass ... -crypto AES256-SHA1 -ptype KRB5_NT_PRINCIPAL`), store it as a secret and mount it **only into turaco-api**.
3. Set `KERBEROS_KEYTAB_FILE`, `KERBEROS_SERVICE_PRINCIPAL=HTTP/turaco.example.local` and `KERBEROS_REALM=EXAMPLE.LOCAL` on the API (directory sync and `LDAP_URL` must be configured: principals map to synced accounts).
4. Add the Turaco URL to the browsers' intranet/trusted zone (group policy); otherwise browsers do not send Negotiate and users see the password form.
5. Keep API and domain controller clocks synchronized (default allowed skew 5 minutes). Proxies must pass `Authorization` and `WWW-Authenticate` unchanged and accept large request headers: tickets with big group memberships exceed nginx defaults (`turaco-web` sets `large_client_header_buffers 4 64k`).

Security notes:

- The keytab is equivalent to the service account's password: whoever holds it can mint tickets for any user of the realm towards Turaco. Keep it readable only by the API process and rotate it (new key version, new keytab) if it may have leaked.
- Mark administrator accounts "Account is sensitive and cannot be delegated" in AD; Turaco needs no delegated credentials.
- The service principal must be `HTTP/<host>`; only AES keys for it in `KERBEROS_REALM` are used, and tickets from trusted foreign realms are refused.
- The Windows PAC is not evaluated: a user disabled in AD can still sign in by Kerberos until the next directory sync marks the User inactive. Shorten the sync interval or run a manual sync when access must end immediately.

## Email notifications

`turaco-worker` sends notification email. `turaco-api` sends only the invitation and password-reset links of local accounts ([ADR-0034](../decisions/ADR-0034-local-accounts-and-external-parties.md)), directly and not through the outbox, so when `AUTH_LOCAL_LOGIN_ENABLED=true` the API needs `EMAIL_BASE_URL` (it refuses to start without it) and, to mail those links, the same `SMTP_*` settings as the worker; without a mail channel an administrator is shown the invitation link of a never-activated account once, and a reset answers 409 `auth.mail_not_configured`. Set `SMTP_HOST`, `SMTP_FROM`, `EMAIL_BASE_URL` (the public https address of the web application, used in links) and, with authentication, `SMTP_USERNAME` plus `SMTP_PASSWORD_FILE` on the worker (and on the API for local accounts); mount the password as a deployment secret. The default `SMTP_SECURITY=starttls` refuses relays that do not offer STARTTLS; `tls` uses implicit TLS. Clear-text relays need `SMTP_SECURITY=none`, `SMTP_ALLOW_PLAINTEXT=true` and `APP_ENV=development`, so a production stack cannot send mail in clear text by accident. Use `SMTP_CA_FILE` for a private CA. Without `SMTP_HOST` email is off and in-app notifications continue. Behavior and limits: [Teams and email notifications](../integrations/teams-email.md).

## First administrator and emergency access

After the first directory sync, grant the first administrator from the worker container (it ships `turaco-admin` and uses the worker's `DATABASE_URL`):

```bash
docker exec <worker> turaco-admin role grant --role platform-administrator --user <username>
```

Further roles are managed in the web UI; the first steps after the first sign-in are in [Getting started as an administrator](administrator-getting-started.md). A local account can never be a platform administrator. For directory outages, create an emergency account once, store its password in the organization's vault, and enable it only when needed:

```bash
docker exec -i <worker> turaco-admin emergency create --login breakglass --display-name "Emergency administrator"
docker exec <worker> turaco-admin role grant --role platform-administrator --user <emergency user id printed above>
docker exec <worker> turaco-admin emergency enable --login breakglass   # during an outage
```

Emergency login additionally requires `AUTH_EMERGENCY_LOGIN_ENABLED=true` on the API. Every use is audited and logged at error level; alert on it. Disable the account and rotate its password after use (`emergency disable`, `emergency set-password`).

## Releases

GHCR images are built by GitHub Actions. Production records immutable digests. `latest` may exist for convenience but is not deployment state.
