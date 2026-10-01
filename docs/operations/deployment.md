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

1. Create a service account in AD for Turaco and register the SPN `HTTP/<turaco host name>` (the name users type in the browser), for example `setspn -S HTTP/turaco.example.local svc-turaco-http`.
2. Export a keytab for that principal with AES encryption types (`ktpass ... -crypto AES256-SHA1 -ptype KRB5_NT_PRINCIPAL`), store it as a secret and mount it **only into turaco-api**.
3. Set `KERBEROS_KEYTAB_FILE`, `KERBEROS_SERVICE_PRINCIPAL=HTTP/turaco.example.local` and `KERBEROS_REALM=EXAMPLE.LOCAL` on the API (directory sync and `LDAP_URL` must be configured: principals map to synced accounts).
4. Add the Turaco URL to the browsers' intranet/trusted zone (group policy); otherwise browsers do not send Negotiate and users see the password form.
5. Keep API and domain controller clocks synchronized (default allowed skew 5 minutes). Proxies must pass `Authorization` and `WWW-Authenticate` unchanged.

## First administrator and emergency access

After the first directory sync, grant the first administrator from the worker container (it ships `turaco-admin` and uses the worker's `DATABASE_URL`):

```bash
docker exec <worker> turaco-admin role grant --role platform-administrator --user <username>
```

Further roles are managed in the web UI. For directory outages, create an emergency account once, store its password in the organization's vault, and enable it only when needed:

```bash
docker exec -i <worker> turaco-admin emergency create --login breakglass --display-name "Emergency administrator"
docker exec <worker> turaco-admin role grant --role platform-administrator --user <emergency user id printed above>
docker exec <worker> turaco-admin emergency enable --login breakglass   # during an outage
```

Emergency login additionally requires `AUTH_EMERGENCY_LOGIN_ENABLED=true` on the API. Every use is audited and logged at error level; alert on it. Disable the account and rotate its password after use (`emergency disable`, `emergency set-password`).

## Releases

GHCR images are built by GitHub Actions. Production records immutable digests. `latest` may exist for convenience but is not deployment state.
