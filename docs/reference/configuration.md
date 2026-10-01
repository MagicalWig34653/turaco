# Configuration Reference

> Generated from code. Do not edit manually.

| Variable | Type | Required | Secret | Default | Description |
|---|---|---|---|---|---|
| `APP_ENV` | string | false | false | `development` | Runtime environment name. |
| `AUTH_EMERGENCY_LOGIN_ENABLED` | bool | false | false | `false` | Expose POST /api/v1/auth/emergency-login for the local break-glass account (created with turaco-admin). Every use is audited and logged at error level. |
| `DATABASE_URL` | string | true | true | `` | PostgreSQL connection URL. |
| `HTTP_ADDR` | string | false | false | `:8080` | HTTP listen address for turaco-api. |
| `HTTP_TRUSTED_PROXIES` | string | false | false | `` | Comma-separated CIDR prefixes of reverse proxies (for example the turaco-web container network) whose X-Forwarded-For header is trusted for the client address used by login throttling and audit. Empty trusts no proxy. |
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
| `LDAP_URL` | string | false | false | `` | Directory server URL (`ldaps://host:636`, or `ldap://` with LDAP_START_TLS). Empty disables directory synchronization. |
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
