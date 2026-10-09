# Configuration Reference

> Generated from code. Do not edit manually.

| Variable | Type | Required | Secret | Default | Description |
|---|---|---|---|---|---|
| `ADVISORY_SOURCES` | string | false | false | `nvd,cisa_kev` | Comma-separated advisory feeds to synchronize: `nvd` (NVD API 2.0) and/or `cisa_kev` (CISA Known Exploited Vulnerabilities catalog). Used when ADVISORY_SYNC is on and by the admin command. |
| `ADVISORY_SYNC` | bool | false | false | `false` | Synchronize security advisories from the public NVD and CISA KEV feeds (scheduled worker job security.advisory_sync and `turaco-admin security sync-feeds`). No account is needed. Imported advisories start in status new; criteria come from the feed's CPE data and analysts decide applicability. |
| `ADVISORY_SYNC_INTERVAL` | duration | false | false | `6h` | How often the advisory sync job runs; at least 1h. |
| `AI_ENABLED` | bool | false | false | `false` | Startup gate of Turaco AI (ADR-0029, F12): the assistant API, tools and provider calls. Off by default; when off only GET /api/v1/ai/status and the permission-protected configuration routes (settings, providers, usage) are mounted and no provider is called by users. Enabling also needs the audited runtime setting ai.settings.enabled and an enabled AI Provider. API and worker may differ (the worker only runs the cleanup jobs). |
| `AI_SECRET_DIR` | string | false | false | `` | Directory with deployment secret files (for example mounted Docker secrets). An AI Provider's `secretRef` names a file in it (`<dir>/<secretRef>`, one line, read when the provider is built, never stored in the database, logged or sent into prompts). Empty: providers without credentials only (a local Ollama). |
| `APP_ENV` | string | false | false | `development` | Runtime environment name. |
| `AUTH_EMERGENCY_LOGIN_ENABLED` | bool | false | false | `false` | Expose POST /api/v1/auth/emergency-login for the local break-glass account (created with turaco-admin). Every use is audited and logged at error level. |
| `AUTH_LOCAL_LOGIN_ENABLED` | bool | false | false | `false` | Enable local accounts (ADR-0034): People administration can invite Users who are not in the directory, and they sign in with POST /api/v1/auth/local-login after setting a password through a single-use link. Off disables local accounts completely (the endpoints answer 404 and existing local sessions are rejected). Needs EMAIL_BASE_URL for the links. Local accounts never hold high-risk permissions. |
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
| `NVD_API_KEY_FILE` | string | false | true | `` | Path to a file containing an optional NVD API key (for example a Docker secret). With a key the NVD rate limit rises from 5 to 50 requests per 30 seconds. The key is sent only to the NVD API and never stored or logged. |
| `PEOPLE_LOOKUP_ENABLED` | bool | false | false | `true` | Enable GET /api/v1/people/lookup: every signed-in employee can find active internal colleagues by name (at least 3 characters, at most 10 results, only id, display name and department, rate limited, audited without the search text) to raise a support ticket or request for another person. Off answers 404; people with organization.view keep the directory routes. |
| `PRESENCE_ENABLED` | bool | false | false | `false` | Startup gate of Workforce Presence (ADR-0028): routes, the retention job and the public availability contract. Off by default; enabling needs the customer's data protection impact assessment and works-council confirmation, recorded afterwards in the audited runtime setting presence.settings.enabled. API and worker must use the same value. |
| `PRESENCE_RETENTION_DAYS` | int | false | false | `30` | Upper bound of the Workforce Presence retention: past entries are deleted this many days after they ended; 1 to 30, never longer. The runtime setting can only shorten it. |
| `PRESENCE_SOURCE_STALE_AFTER` | duration | false | false | `24h` | An externally sourced Workforce Presence signal older than this is shown as unknown instead of being trusted; at least 1m. |
| `REMOTE_ACCESS_APPROVAL_REQUIRED_OWNERSHIP` | string | false | false | `` | Comma-separated Device ownerships (`corporate`, `personal`, `unknown`) whose Remote Access Sessions need a second approver holding remote_access.admin before they can be launched. Empty requires no approval. |
| `REMOTE_ACCESS_PROVIDERS` | string | false | false | `` | Comma-separated Remote Access Provider keys to enable (`rustdesk`, `anydesk`, `hoptodesk`; launch-link connectors, attended sessions only). Empty switches Remote Access off: no session can be requested. Unknown keys stop turaco-api and turaco-worker at startup. API and worker must use the same value. |
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
| `SOFTWARE_DEPLOY_WRITE` | bool | false | false | `false` | Capability: allow Deployments to write ring assignments to the Management Provider (start, resume, promote, resolving targets, clearing after a cancel or kill switch). The worker job endpoints.deployment_tick runs every minute either way; with the capability off it only runs the kill-switch sweep (pause, halt, queue clearing). API and worker must use the same value. Off by default; enabling is audited (endpoints.deploy_write.enabled). The Graph write client is not implemented yet: the writer is a placeholder whose writes fail permanently, so a ring halts with assignment_failed. |
| `SOFTWARE_PROVIDER_SYNC` | bool | false | false | `false` | Allow the Software Package synchronization (POST /api/v1/software/packages/sync and the scheduled worker job) against the Software Management Provider (IntuneGet). The provider client is not implemented yet: with the switch on, a run reports that the provider is not configured. |
| `TENANT_ID` | string | false | false | `default` | Fixed data plane (tenant) id of this installation (F12 A15, ADR-0007). It is attached to every authenticated principal on the server and written to AI audit entries and AI usage counters; it is never taken from request fields. 1 to 100 characters of letters, digits, `.`, `_` and `-`. |
