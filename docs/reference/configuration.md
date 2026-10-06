# Configuration Reference

> Generated from code. Do not edit manually.

| Variable | Type | Required | Secret | Default | Description |
|---|---|---|---|---|---|
| `APP_ENV` | string | false | false | `development` | Runtime environment name. |
| `AUTH_EMERGENCY_LOGIN_ENABLED` | bool | false | false | `false` | Expose POST /api/v1/auth/emergency-login for the local break-glass account (created with turaco-admin). Every use is audited and logged at error level. |
| `AUTOTASK_SYNC` | bool | false | false | `false` | Synchronize tickets with Autotask (external references, push jobs, inbound updates). The REST client is not implemented yet: with the switch on, pushes fail permanently with a visible "not configured" state. |
| `DATABASE_URL` | string | true | true | `` | PostgreSQL connection URL. |
| `EMAIL_BASE_URL` | string | false | false | `` | Externally reachable address of the web application without a path, for example `https://turaco.example.org`; emails link to it. Required when SMTP_HOST is set; must be https outside development. |
| `EMAIL_DEFAULT_LOCALE` | string | false | false | `en` | Language of notification emails: `en` or `de` (recipients have no language setting yet). |
| `HTTP_ADDR` | string | false | false | `:8080` | HTTP listen address for turaco-api. |
| `HTTP_TRUSTED_PROXIES` | string | false | false | `` | Comma-separated CIDR prefixes of reverse proxies (for example the turaco-web container network) whose X-Forwarded-For header is trusted for the client address used by login throttling and audit. Empty trusts no proxy. |
| `INTUNE_SYNC` | bool | false | false | `false` | Allow endpoint synchronization (POST /api/v1/endpoint-sync) from the Intune provider. The Graph client is not implemented yet: with the switch on, a run reports that the provider is not configured. |
| `KERBEROS_KEYTAB_FILE` | string | false | true | `` | Path to the keytab file (a deployment secret, mounted only into turaco-api) holding the key of KERBEROS_SERVICE_PRINCIPAL. Setting it enables Kerberos/SPNEGO login (GET /api/v1/auth/kerberos) and requires LDAP_URL: tickets are mapped to synced directory accounts. Empty disables Kerberos. |
| `KERBEROS_MAX_CLOCK_SKEW` | duration | false | false | `5m` | Maximum clock difference between client and server accepted for Kerberos tickets, which also sets the replay-cache window; at most 15m. |
| `KERBEROS_REALM` | string | false | false | `` | The only accepted Kerberos realm, upper case (for example `EXAMPLE.LOCAL`); tickets of other realms are refused. Required when KERBEROS_KEYTAB_FILE is set. |
| `KERBEROS_SERVICE_PRINCIPAL` | string | false | false | `` | Service principal of turaco-api without realm, for example `HTTP/turaco.example.local`; its key must be in the keytab. Required when KERBEROS_KEYTAB_FILE is set. |
| `LDAP_ALLOW_PLAINTEXT` | bool | false | false | `false` | Allow `ldap://` without StartTLS (credentials in clear text). Accepted only together with APP_ENV=development, for local test directories. |
| `LDAP_BIND_DN` | string | false | false | `` | DN of the read-only service account used for synchronization. Required when LDAP_URL is set. |
| `LDAP_BIND_PASSWORD_FILE` | string | false | true | `` | Path to a file containing the bind password (for example a Docker secret). Required when LDAP_URL is set. The password is never stored in the database or logged. |
| `LDAP_CA_FILE` | string | false | false | `` | PEM file with CA certificates trusted for the directory server, in addition to the system pool. Certificate verification is never disabled. |
| `LDAP_DIRECTORY_TYPE` | string | false | false | `active_directory` | Directory schema: `active_directory` (objectGUID, sAMAccountName, userAccountControl) or `openldap` (entryUUID, uid). |
| `LDAP_GROUP_BASE_DN` | string | false | false | `` | Search base for groups. Required when LDAP_URL is set. |
| `LDAP_GROUP_FILTER` | string | false | false | `` | Group search filter. Default depends on LDAP_DIRECTORY_TYPE. |
| `LDAP_PROVIDER_KEY` | string | false | false | `ad` | Stable key identifying this directory in external identities and directory groups; lowercase letters, digits and hyphens. Changing it makes all existing observations stale. |
| `LDAP_START_TLS` | bool | false | false | `false` | Upgrade an `ldap://` connection with StartTLS. Invalid boolean values are rejected. |
| `LDAP_SYNC_INTERVAL` | duration | false | false | `1h` | Interval between scheduled directory synchronization runs; at least 5m. |
| `LDAP_SYNC_MAX_MISSING_PERCENT` | int | false | false | `10` | Safeguard: when more than this percentage (and more than 5) of the provider's active directory users, or of its observed groups, are missing from a run, that not-observed sweep is withheld (outcome sweep_withheld); everything else, including explicit disables, is applied. 0-100. |
| `LDAP_SYNC_TIMEOUT` | duration | false | false | `15m` | Maximum duration of one directory synchronization run; a run still marked running after this is treated as abandoned. |
| `LDAP_URL` | string | false | false | `` | Directory server URL (`ldaps://host:636`, or `ldap://` with LDAP_START_TLS). Empty disables directory synchronization and password login. |
| `LDAP_USER_BASE_DN` | string | false | false | `` | Search base for user accounts. Required when LDAP_URL is set. |
| `LDAP_USER_FILTER` | string | false | false | `` | User search filter. Default depends on LDAP_DIRECTORY_TYPE. |
| `LOG_LEVEL` | string | false | false | `info` | Application log level. |
| `S3_ACCESS_KEY_ID` | string | false | true | `` | S3 access key when required. |
| `S3_BUCKET` | string | false | false | `turaco-dev` | Object-storage bucket/namespace. |
| `S3_ENDPOINT` | string | false | false | `` | S3-compatible endpoint; set for non-AWS/local providers. |
| `S3_PATH_STYLE` | bool | false | false | `true` | Use path-style S3 addressing. |
| `S3_REGION` | string | false | false | `us-east-1` | S3 region. |
| `S3_SECRET_ACCESS_KEY` | string | false | true | `` | S3 secret key when required. |
| `SESSION_ABSOLUTE_TIMEOUT` | duration | false | false | `24h` | Maximum session lifetime regardless of activity; must be positive. |
| `SESSION_COOKIE_SECURE` | bool | false | false | `true` | Set the Secure attribute on the session cookie; disable only for local plain-HTTP development. |
| `SESSION_IDLE_TIMEOUT` | duration | false | false | `8h` | Session idle timeout; must be positive and not exceed SESSION_ABSOLUTE_TIMEOUT. |
| `SMTP_ALLOW_PLAINTEXT` | bool | false | false | `false` | Allow SMTP_SECURITY=none (clear text, credentials included). Accepted only together with APP_ENV=development, for local test relays. |
| `SMTP_CA_FILE` | string | false | false | `` | PEM file with CA certificates trusted for the relay, in addition to the system pool. |
| `SMTP_FROM` | string | false | false | `` | Sender address of notification emails, for example `Turaco <turaco@example.org>`. Required when SMTP_HOST is set. |
| `SMTP_HOST` | string | false | false | `` | Mail relay host name. Setting it enables the email channel of notifications in turaco-worker; empty disables email (in-app notifications are unaffected). |
| `SMTP_PASSWORD_FILE` | string | false | true | `` | Path to a file containing the relay password (for example a Docker secret). Requires SMTP_USERNAME. The password is never stored in the database or logged. |
| `SMTP_PORT` | int | false | false | `` | Mail relay port. Default 587 for starttls, 465 for tls and 25 for none. |
| `SMTP_SECURITY` | string | false | false | `starttls` | Connection protection: `starttls` (required upgrade before credentials or mail are sent), `tls` (implicit TLS) or `none` (clear text; requires SMTP_ALLOW_PLAINTEXT=true and APP_ENV=development). Certificate verification is never disabled. |
| `SMTP_TIMEOUT` | duration | false | false | `30s` | Maximum duration of sending one email (connect, dialogue and transfer). |
| `SMTP_USERNAME` | string | false | false | `` | Relay account. Requires SMTP_PASSWORD_FILE. |
| `SOFTWARE_PROVIDER_SYNC` | bool | false | false | `false` | Allow the Software Package synchronization (POST /api/v1/software/packages/sync and the scheduled worker job) against the Software Management Provider (IntuneGet). The provider client is not implemented yet: with the switch on, a run reports that the provider is not configured. |
