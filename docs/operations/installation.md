# Installation

This guide installs Turaco from the repository for a single-host trial or a real deployment. It describes only what exists in the repository today; [current status](../product/current-status.md) is authoritative for features. Facts that could not be confirmed from the repository are marked **not verified**. Every environment variable is listed in the generated [configuration reference](../reference/configuration.md); this page names only those an installation needs.

Turaco is an early build. There is no packaged release, installer, Helm chart or Windows service in the repository. Install either from the container images that `deploy/docker/` builds, or from native binaries you build yourself.

## 1. Prerequisites

| Component | Version | Notes |
| --- | --- | --- |
| Go | 1.27.0 or newer (`go.mod`) | Only to build from source. The Dockerfiles use `golang:1.27.1-alpine`. |
| Node.js | `>=24 <27` (`frontend/package.json`) | Only to build the frontend. The web image uses `node:26.10.0-alpine`. |
| PostgreSQL | 18 | Required. The compose files use `postgres:18.6-alpine`. Migration `000060` runs `CREATE EXTENSION IF NOT EXISTS pg_trgm` (a contrib extension), so the migrating role must be allowed to create it or an administrator must create it beforehand (**not verified** for managed PostgreSQL services). |
| File storage for attachments | optional | A directory (`STORAGE_DRIVER=filesystem`) or an S3-compatible bucket (`STORAGE_DRIVER=s3`, the `S3_*` settings), see [section 5.1](#51-file-storage-and-attachments). Compose uses Adobe S3Mock, a test double that is not for production. Nothing else in Turaco uses the bucket today. |
| ClamAV | optional, required with attachments | A `clamd` daemon reachable over TCP, in its own container ([section 5.1](#51-file-storage-and-attachments)). |
| Reverse proxy with TLS | any | Required for production; the API itself speaks plain HTTP only. |
| SMTP relay | optional | Notification email and the invitation and reset links of local accounts. |
| LDAP / Active Directory | optional | Directory sync and password login; see [LDAP/AD](../integrations/ldap-ad.md). |
| Kerberos keytab | optional | Single sign-on; see [Deployment](deployment.md#kerberos-single-sign-on). |
| Docker with Compose | optional | Only for the container paths. |

## 2. Architecture of an installation

```text
 Browser --HTTPS--> reverse proxy (TLS) ----+--> static frontend files (frontend/dist)
                                            |
                                            +--> /api/ and /health/ --> turaco-api (HTTP :8080)
                                                                          |
                    turaco-worker (no listener) ---------+----------------+
                                                         v                v
                                                    PostgreSQL 18     S3-compatible bucket
```

- **turaco-api** serves the REST API under `/api/v1` and the probes `/health/live` and `/health/ready`. It does not serve the frontend files.
- **turaco-worker** runs the outbox, scheduled jobs (directory sync, email, retention, advisory feeds) and writes a heartbeat. It has no HTTP listener and needs the same database.
- **Frontend** is a static single-page application built by Vite (`frontend/dist`). The API does not embed it. The `turaco-web` image (nginx) serves it and proxies `/api/` and `/health/` to `turaco-api:8080` (`deploy/docker/nginx.conf`); on a native install any web server with the same rules does.
- **turaco-migrate** applies SQL migrations once per deployment. It is a separate binary and is not part of the API or worker images (section 6).
- **turaco-admin** is the operator CLI (first administrator, emergency account). The container images ship it in the worker image only.

State lives in PostgreSQL and object storage; API and worker nodes keep no required local files ([Deployment](deployment.md)).

## 3. Obtain and build

```bash
git clone <repository url> turaco && cd turaco
```

The repository URL and published image names are not documented here (**not verified**; `deploy/swarm/stack.example.yaml` only has `ghcr.io/OWNER/REPOSITORY/...` placeholders and [Release](release.md) says images go to GHCR).

### Native binaries and frontend

To build only the server parts:

```bash
mkdir -p dist/bin
for c in turaco-api turaco-worker turaco-migrate turaco-admin; do
  go build -trimpath -o dist/bin/$c ./backend/cmd/$c
done
(cd frontend && npm ci && npm run build)   # output: frontend/dist
```

`make build` builds the same four binaries plus `connector-agent` and `endpoint-agent` into `dist/bin/` and runs `npm run build` in `frontend/`; it needs `npm ci` done first. The agents are not required for the server.

### Container images

```bash
make docker-build   # turaco/api:dev, turaco/worker:dev, turaco/web:dev
```

The images contain no migrations and no `turaco-migrate`. Run migrations from a binary built as above (section 6).

## 4. Database

Create a database and a role. Turaco uses one `DATABASE_URL` for the API, the worker and the migration tool, so that role must own the objects and be able to run migrations.

```sql
CREATE ROLE turaco LOGIN PASSWORD '<generated secret>';
CREATE DATABASE turaco OWNER turaco;
-- if the role may not create extensions, as a superuser, once:
\c turaco
CREATE EXTENSION IF NOT EXISTS pg_trgm;
```

Least privilege and its limits:

- Use a dedicated role that is not a superuser and is not shared with other applications. `turaco-migrate` creates the `platform` schema and `platform.schema_migrations` itself.
- There is one `DATABASE_URL` variable, so a separate runtime role and migration role is **not supported by the configuration today**. Because API and worker share one database role, the audit retention floor is enforced inside the SQL function `platform.purge_audit_before`, which clamps any cutoff to at least 365 days, instead of by a grant ([ADR-0034](../decisions/ADR-0034-local-accounts-and-external-parties.md), review rule R9; migration `000071`). Retention is off by default (`AUDIT_RETENTION_DAYS=0`).
- Require TLS to PostgreSQL in production with `sslmode=verify-full` in the URL. The compose files use `sslmode=disable` on their private network only.

Create the bucket and an access key limited to it with your storage provider's tooling (provider specific, **not verified** here).

## 5. Configuration

The processes are configured only by environment variables; there is no configuration file. Pass secrets from a root-only file or the orchestrator's secret store. Variables ending in `_FILE` take a path to a file containing the secret.

Minimum for a production installation (API and worker unless stated):

| Variable | Value | Meaning |
| --- | --- | --- |
| `APP_ENV` | `production` | Defaults to `development`. Only `development` allows clear-text LDAP and SMTP (`LDAP_ALLOW_PLAINTEXT`, `SMTP_ALLOW_PLAINTEXT`) and defaults `EMAIL_BASE_URL` to `http://localhost:5173`. Do not leave the default. |
| `DATABASE_URL` | `postgres://turaco:...@host:5432/turaco?sslmode=verify-full` | Required. Secret. |
| `HTTP_ADDR` | `:8080` (default) | API listen address. Bind to a loopback or private address when the proxy is on the same host. |
| `EMAIL_BASE_URL` | `https://turaco.example.org` | Public address without a path; links in email are built from it. Must be https outside development, and every non-development environment must set it. Required when `SMTP_HOST` is set and when `AUTH_LOCAL_LOGIN_ENABLED=true`. |
| `SESSION_COOKIE_SECURE` | `true` (default) | Keep the default; `false` is for plain-HTTP development only. |
| `SESSION_IDLE_TIMEOUT`, `SESSION_ABSOLUTE_TIMEOUT` | `8h`, `24h` (defaults) | Adjust to policy; idle must not exceed absolute. |
| `HTTP_TRUSTED_PROXIES` | CIDR of the reverse proxy (API) | Without it login throttling treats every client as the proxy; see [Deployment](deployment.md#reverse-proxy-and-client-addresses). |
| `S3_ENDPOINT`, `S3_BUCKET`, `S3_REGION`, `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY`, `S3_PATH_STYLE` | provider values | Used only with `STORAGE_DRIVER=s3`. Defaults: bucket `turaco-dev`, region `us-east-1`, path style on. Set a real bucket name. |
| `STORAGE_DRIVER`, `STORAGE_PATH`, `STORAGE_MASTER_KEY_FILE`, `CLAMAV_ADDRESS` | see [5.1](#51-file-storage-and-attachments) | Attachments. Empty `STORAGE_DRIVER` leaves them off. Same values on API and worker. |
| `TENANT_ID` | `default` (default) | Fixed data plane id of this installation. |
| `LOG_LEVEL` | `info` (default) | Application log level. |

Authentication mode (enable at least one way to sign in):

| Mode | Settings | Notes |
| --- | --- | --- |
| Directory (LDAP/AD) | `LDAP_URL` (`ldaps://`), `LDAP_BIND_DN`, `LDAP_BIND_PASSWORD_FILE` (worker), `LDAP_USER_BASE_DN`, `LDAP_GROUP_BASE_DN`, `LDAP_PROVIDER_KEY` (same on API and worker) | Enables directory sync and password login. The API binds as the user and does not need the bind secret. Plain `ldap://` needs StartTLS, or `APP_ENV=development`. |
| Kerberos | `KERBEROS_KEYTAB_FILE`, `KERBEROS_SERVICE_PRINCIPAL`, `KERBEROS_REALM` on the API | Needs the LDAP settings. See [Deployment](deployment.md#kerberos-single-sign-on). |
| Local accounts | `AUTH_LOCAL_LOGIN_ENABLED=true`, `EMAIL_BASE_URL`, `SMTP_*` | Default off. The API refuses to start without `EMAIL_BASE_URL`. Local accounts can never be platform administrators ([ADR-0034](../decisions/ADR-0034-local-accounts-and-external-parties.md)). |
| Emergency (break-glass) | `AUTH_EMERGENCY_LOGIN_ENABLED=true` on the API, plus the CLI steps in section 9 | Default off. Audited and logged at error level. Enable only when needed. |

Email (optional): `SMTP_HOST`, `SMTP_FROM`, `SMTP_SECURITY` (`starttls` default or `tls`), `SMTP_USERNAME`, `SMTP_PASSWORD_FILE`, `SMTP_CA_FILE`. Set them on the worker and, for local-account links, on the API.

Microsoft Teams channel posts (optional): `TEAMS_CHANNEL_DESTINATIONS_FILE` (a secret file mapping destination keys to Workflows webhook URLs), read-only in the worker and the API; then switch on the module Microsoft Teams and add routes. See [Teams channel posts](../integrations/teams-email.md#teams-channel-posts-t-a).

Optional module gates and flags such as `PRESENCE_ENABLED`, `AI_ENABLED`, `REMOTE_ACCESS_PROVIDERS`, `ADVISORY_SYNC` and `AUDIT_RETENTION_DAYS` are explained in the configuration reference and in [Getting started as an administrator](administrator-getting-started.md#3-modules). Settings that both processes read must have the same value on API and worker; the reference states which.

Do not enable `INTUNE_SYNC`, `AUTOTASK_SYNC`, `SOFTWARE_PROVIDER_SYNC` or `SOFTWARE_DEPLOY_WRITE` expecting live integrations: the Intune Graph, Autotask REST and software provider clients are not implemented, and with the switch on runs report "not configured" or fail permanently ([current status](../product/current-status.md)).

### 5.1 File storage and attachments

Attachments on tickets and knowledge articles ([ADR-0037](../decisions/ADR-0037-file-storage-and-attachments.md)) are optional. They are off until `STORAGE_DRIVER` is set; every other function works without them.

1. **Choose the driver.**
   - `filesystem` suits a single server and on-premises installs: set `STORAGE_PATH` to an absolute directory that exists on a persistent disk (Turaco creates it with mode `0700`). API and worker must see the same directory, so on several hosts use a shared volume or choose `s3`. The directory holds only encrypted objects named by random ids; never a user file name.
   - `s3` uses the `S3_*` settings and a private bucket whose access key is limited to that bucket.
2. **Create the master key** once and keep a copy apart from the data backups: `openssl rand -hex 32 > /etc/turaco/storage-master.key`, owned by the user that runs the API and worker with mode `0400` or `0600` (`chmod 0400`); Turaco refuses to start when group or others have any access to the file (unix); then `STORAGE_MASTER_KEY_FILE=/etc/turaco/storage-master.key`. Upload exhaustion is bounded by `ATTACHMENT_UPLOADS_PER_HOUR` (per user, per process), `ATTACHMENT_USER_QUOTA_BYTES` and `ATTACHMENT_INSTALLATION_QUOTA_BYTES`; size the installation quota to the free space of the storage. Each file is encrypted with its own random data key, which the master key wraps (AES-256-GCM). **Without the master key every stored attachment is unreadable**; there is no recovery and master key rotation is not implemented.
3. **Run ClamAV in its own container** and set `CLAMAV_ADDRESS=host:3310`. Uploads stay `pending` and cannot be downloaded until the worker has scanned them clean; infected files are quarantined and never downloadable. If ClamAV is down, uploads still succeed and wait as `pending` (platform health shows `attachment_scanner` failing with the backlog). `STORAGE_DRIVER` cannot be set without `CLAMAV_ADDRESS`.
   - Development: `make infra-up` starts `clamav/clamav` from `deploy/compose/dev.yaml` on port 3310 (the first start downloads signatures and needs internet access for a minute or two).
   - Production: run the official `clamav/clamav` image (clamd and freshclam in one container) on the private network, **never publish port 3310** (clamd has no authentication or TLS), pin the image by digest, give it persistent storage for `/var/lib/clamav` so restarts do not re-download the database, set `CLAMD_CONF_StreamMaxLength` and `CLAMD_CONF_MaxFileSize` to at least `ATTACHMENT_MAX_BYTES` (a smaller clamd limit makes large files `failed`), size memory generously (about 2 GiB, signature loading is memory hungry) and allow outbound access to the signature mirrors or provide an internal mirror for air-gapped sites (**not verified** for your environment). Monitor that the signatures are recent; Turaco does not.
4. **Limits.** `ATTACHMENT_MAX_BYTES` (default 25 MiB, at most 100 MiB) and `ATTACHMENT_ALLOWED_TYPES` (default PDF, PNG, JPEG, GIF, WebP, plain text, CSV, Office Open XML documents; archives are opt-in; HTML, SVG and executables are never accepted). The web container's nginx allows request bodies up to 101 MiB on `/api/v1/attachments`; a different reverse proxy must allow `ATTACHMENT_MAX_BYTES` plus about 64 KiB, stream the body and allow ten minutes for transfers.
5. **Verify.** `/admin/health` shows `object_storage` (a write, read and delete of a probe object) and `attachment_scanner` (pending scans, quarantined and failed counts); the setup checklist item "attachments" turns done when both are healthy.

## 6. Run migrations

Migrations are forward-only SQL files in `backend/migrations/` (`NNNNNN_name.up.sql`). `turaco-migrate` reads `DATABASE_URL`, creates `platform.schema_migrations` if needed and applies each unapplied file in version order. It takes the directory from `-dir` or `MIGRATIONS_DIR` (default `backend/migrations`, relative to the working directory).

```bash
export DATABASE_URL='postgres://turaco:...@db.example.org:5432/turaco?sslmode=verify-full'
dist/bin/turaco-migrate -dir backend/migrations
```

`make migrate` is the development wrapper (`scripts/migrate.sh` through `scripts/with-env.sh`, which reads a `.env` file); it also creates a test database when `TEST_DATABASE_URL` is set, so use the binary directly for a real deployment. Released migrations are never edited; a fix is a new migration. Whether the tool guards against two parallel runs is **not verified**: run it from one place at a time.

## 7. Start the API and the worker

Run migrations first, then start the worker and the API with the same environment file (each reads only the settings it needs).

### Native, with systemd (example)

Example only, not shipped by the repository; adjust paths, user and hardening to your policy.

```ini
# /etc/systemd/system/turaco-api.service
[Unit]
Description=Turaco API
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
User=turaco
Group=turaco
EnvironmentFile=/etc/turaco/turaco.env
ExecStart=/opt/turaco/bin/turaco-api
Restart=on-failure
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

```ini
# /etc/systemd/system/turaco-worker.service
[Unit]
Description=Turaco worker
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
User=turaco
Group=turaco
EnvironmentFile=/etc/turaco/turaco.env
ExecStart=/opt/turaco/bin/turaco-worker
Restart=on-failure
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

`/etc/turaco/turaco.env` holds `KEY=value` lines, owned by root, group `turaco`, mode `0640`. Then `systemctl daemon-reload && systemctl enable --now turaco-worker turaco-api`.

### Containers

`deploy/compose/full.yaml` builds and starts PostgreSQL, S3Mock, `turaco-api`, `turaco-worker` and `turaco-web` (nginx, host port 8088). It is a **single-host trial**, not a production stack: it uses `APP_ENV: local-compose`, the password `change-me`, `sslmode=disable`, S3Mock and no TLS. It contains no migration step and does not publish the PostgreSQL port, so apply migrations yourself (for example by temporarily adding `ports: ["5432:5432"]` to the `postgres` service and running `turaco-migrate` from the host; a migration container is not provided, **not verified**). For plain-HTTP trials on `http://localhost:8088` the session cookie is `Secure` by default; whether your browser keeps it on localhost is **not verified**, and `SESSION_COOKIE_SECURE=false` on the API is the documented development switch.

```bash
docker compose -f deploy/compose/full.yaml up -d --build
```

For production containers, `deploy/swarm/stack.example.yaml` is an incomplete example (image digests, `APP_ENV: production`, a `database_url` secret, LDAP placeholders). It states that secrets, database HA, object storage, TLS, backups and monitoring are environment specific. The example passes `database_url` as a Docker secret while the configuration reference defines only an environment variable `DATABASE_URL` without a `_FILE` variant; how that secret reaches the process is **not verified**.

## 8. Serve the frontend and terminate TLS

The frontend is `frontend/dist` after `npm run build`. Serve it as static files with a single-page fallback to `index.html`, forward `/api/` and `/health/` to the API, preserve the `Host` header (the same-origin guard compares it with `Origin`), set `X-Forwarded-For`, and set the API's `HTTP_TRUSTED_PROXIES` to the proxy's own address.

Example nginx configuration (example only, derived from `deploy/docker/nginx.conf`; certificates and hardening headers are yours to supply):

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name turaco.example.org;
    ssl_certificate     /etc/ssl/turaco/fullchain.pem;
    ssl_certificate_key /etc/ssl/turaco/privkey.pem;

    root /var/www/turaco;                 # contents of frontend/dist
    index index.html;
    large_client_header_buffers 4 64k;    # needed for Kerberos tickets

    location /api/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }
    location /health/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        allow 127.0.0.1;                  # limit probes to monitoring
        deny all;
    }
    location / {
        try_files $uri $uri/ /index.html;
    }
}
server {
    listen 80;
    server_name turaco.example.org;
    return 301 https://$host$request_uri;
}
```

With this block set `HTTP_TRUSTED_PROXIES=127.0.0.1/32` on the API. The Vite dev server (`make frontend`) is for development only and is not a deployment option.

## 9. First sign-in and first-run setup

A fresh installation has no administrator, and no web wizard creates one. The first platform administrator is granted on the command line with `turaco-admin`, which uses `DATABASE_URL` and the configuration from its environment. Its subcommands (from `backend/cmd/turaco-admin/main.go`):

```text
turaco-admin role list
turaco-admin role grant  --role <key> (--user <username|email|uuid> | --group <uuid>)
turaco-admin role revoke --assignment <uuid>
turaco-admin emergency create --login <name> --display-name <text> [--password-stdin]
turaco-admin emergency set-password --login <name> [--password-stdin]
turaco-admin emergency enable  --login <name>
turaco-admin emergency disable --login <name>
turaco-admin security import < advisories.json
turaco-admin security sync-feeds [--source nvd|cisa_kev] [--since YYYY-MM-DD]
```

`turaco-admin demo seed` and `demo seed-hospital` are development only; do not run them in production.

- **With a directory:** configure `LDAP_*`, wait for the first sync, run `turaco-admin role grant --role platform-administrator --user <username>` and sign in with the directory password.
- **Without a directory:** `turaco-admin emergency create`, `role grant` for the emergency user id it prints, `emergency enable`, set `AUTH_EMERGENCY_LOGIN_ENABLED=true` on the API and sign in with the emergency option. Emergency sessions last at most one hour.

In the container images `turaco-admin` is in the worker image: `docker exec <worker> turaco-admin ...`. Natively, use `dist/bin/turaco-admin` with the same environment file. Details and the break-glass procedure: [Deployment](deployment.md#first-administrator-and-emergency-access) and [Getting started as an administrator](administrator-getting-started.md#2-first-administrator-and-first-sign-in). A platform administrator then works through the setup checklist at `/admin/setup` (permission `platform.health.view`; changing items needs `platform.admin`).

## 10. Verify health

| Check | How | Expected |
| --- | --- | --- |
| API process alive | `curl -fsS http://127.0.0.1:8080/health/live` | `{"status":"ok"}` |
| API can reach the database | `curl -fsS http://127.0.0.1:8080/health/ready` | `{"status":"ready"}`; 503 `platform.database_unavailable` otherwise (a 2 second database ping; it checks neither migrations nor the worker) |
| Version and environment | `GET /api/v1/meta` | `environment` is `production` |
| Platform health | `/admin/health` in the web UI, API `GET /api/v1/admin/health` | Checks for database, migrations, worker heartbeat, jobs, outbox, directory, mail, object storage, attachment scanner and modules; providers without a client report `not_configured` |
| Setup checklist | `/admin/setup` | Items derived from the same facts |

The probe paths are `/health/live` and `/health/ready`, not `/healthz` and `/readyz`. The `/admin/health` and `/admin/setup` backend is documented as implemented (F14 A-C); the screens (A-F) may still be listed as not implemented in [current status](../product/current-status.md), so confirm in your build (**not verified**). The worker has no HTTP probe; use the worker heartbeat in platform health or your process supervisor.

## 11. Backups and restore basics

Strategy and restore order are in [Backup and Restore](backup-restore.md); this is the minimum.

- **PostgreSQL:** logical dumps (`pg_dump -Fc turaco`) or base backups with WAL archiving. Sessions, audit events, jobs and all domain data are in the database.
- **Attachments:** back up `STORAGE_PATH` (or replicate the bucket) in step with PostgreSQL: the database holds the attachment metadata, the storage holds the encrypted content. A restore of one without the other leaves attachments without content or content without metadata (orphans are removed by the purge job). Back up `STORAGE_MASTER_KEY_FILE` separately from, and never inside, the data backup; without it the objects cannot be decrypted.
- **Keys and configuration:** keep the environment file and mounted secrets (keytab, LDAP bind password file) in your secret manager. Key material follows the separate protected procedure in [Encryption](../security/encryption.md); key provider setup for a plain install is **not verified**.
- **Restore order:** keys, PostgreSQL, object storage, then API and worker. Restore periodically into an isolated environment and record the date.
- After restoring a database, start binaries of a version that matches its schema, or run `turaco-migrate` of the newer version before starting them.

## 12. Upgrade

1. Read [current status](../product/current-status.md) and compare the [configuration reference](../reference/configuration.md) for new or changed variables.
2. Back up PostgreSQL and the bucket.
3. Build or pull the new binaries or images and record image digests ([Release](release.md)).
4. Run `turaco-migrate` of the new version against the database. Migrations are forward-only; there are no down migrations, so rollback means restoring the backup.
5. Restart the worker and the API (Swarm: rolling with `start-first`). Replace the frontend files with the new `frontend/dist`.
6. Verify as in section 10. After an upgrade optional modules are on, except Workforce Presence and Turaco AI.

Compatibility of old binaries with a newer schema during a rolling upgrade is **not verified**; do not rely on it.

## 13. Troubleshooting

| Symptom | Likely cause | Action |
| --- | --- | --- |
| API logs `load configuration`, or `DATABASE_URL is required` from `turaco-migrate` | `DATABASE_URL` unset or invalid | Set it in the process environment (not only in your shell). |
| API logs `load email configuration` | `EMAIL_BASE_URL` missing or not https outside development | Set `EMAIL_BASE_URL=https://...` (required with `AUTH_LOCAL_LOGIN_ENABLED=true` or `SMTP_HOST`). |
| Startup fails on SMTP or LDAP settings | `SMTP_SECURITY=none`, plain `ldap://` or `*_ALLOW_PLAINTEXT` outside `APP_ENV=development` | Use `starttls`/`tls`/`ldaps://`, or correct `APP_ENV`. |
| API logs `configure kerberos login` | `KERBEROS_KEYTAB_FILE` without `LDAP_URL`, realm or principal | Complete the LDAP and Kerberos settings. |
| API or worker logs `configure remote access providers` | Unknown key in `REMOTE_ACCESS_PROVIDERS` | Use only `rustdesk`, `anydesk`, `hoptodesk`. |
| Errors that a relation or column does not exist | Migrations not applied | Run `turaco-migrate` (section 6); inspect `platform.schema_migrations`. |
| `turaco-migrate` finds no files | Wrong working directory | Pass `-dir` or set `MIGRATIONS_DIR`. |
| Migration fails creating `pg_trgm` | Role may not create extensions | Create the extension as a superuser (section 4). |
| `/health/ready` returns 503 | Database unreachable, wrong credentials or TLS mode | Check `DATABASE_URL`, firewall and `sslmode`. |
| Sign-in succeeds, then the session is lost | `SESSION_COOKIE_SECURE=true` over plain HTTP | Use HTTPS; disable the flag only for local development. |
| One client's failed logins block everyone | Proxy not trusted, all clients share one counter | Set `HTTP_TRUSTED_PROXIES` to the proxy address ([Deployment](deployment.md#reverse-proxy-and-client-addresses)). |
| Unsafe requests (POST, PUT) are rejected | Proxy does not preserve `Host`, or cross-origin access | Pass `Host $host`; use the public URL only. |
| Optional module blocked or its routes answer 404 | Startup gate or precondition unmet (`PRESENCE_ENABLED`, `AI_ENABLED`, `REMOTE_ACCESS_PROVIDERS`), or module switched off | Administration > Modules (`/admin/modules`); [Getting started](administrator-getting-started.md#3-modules). |
| Local login answers 404 | `AUTH_LOCAL_LOGIN_ENABLED=false` | Enable it (needs `EMAIL_BASE_URL`). |
| Password reset answers 409 `auth.mail_not_configured` | No SMTP settings on the API | Set the `SMTP_*` variables on the API. |
| No notification email | `SMTP_HOST` unset, or the worker lacks the `SMTP_*` settings | Configure the worker ([Deployment](deployment.md#email-notifications)). |
| No directory users | Sync not run, or worker not running | Check the worker, `LDAP_*` and Administration > Directory Sync. |
| Kerberos login not offered | Browser does not send Negotiate, site not in the intranet zone, or clock skew above 5 minutes | See [Deployment](deployment.md#kerberos-single-sign-on). |
| Integration sync reports "not configured" | Live Intune, Autotask and software provider clients do not exist yet | Expected; see current status. |

## 14. Security hardening checklist

- [ ] `APP_ENV=production`; no `*_ALLOW_PLAINTEXT` variable set.
- [ ] TLS terminates at the reverse proxy, HTTP redirects to HTTPS, `EMAIL_BASE_URL` is https, `SESSION_COOKIE_SECURE` is `true`.
- [ ] `turaco-api` is reachable only from the proxy (private bind address or firewall for `:8080`); `/health/` is limited to monitoring.
- [ ] `HTTP_TRUSTED_PROXIES` lists only the proxy's own address(es).
- [ ] PostgreSQL requires TLS (`sslmode=verify-full`), listens on a private network and is accessed by a dedicated non-superuser role; the password is not in the repository, shell history or world-readable unit files.
- [ ] The attachment storage (bucket or `STORAGE_PATH`) is private and writable only by the API and worker; the master key file is `0400`, copied to a vault and not in the backups of the data; `clamd` port 3310 is reachable only from API-adjacent hosts (the worker) and never from outside.
- [ ] Secrets are in a root-only environment file or an orchestrator secret store; `_FILE` secrets (LDAP bind, SMTP, keytab) are readable only by the process that needs them; the keytab is mounted only into `turaco-api`.
- [ ] `LDAP_URL` uses `ldaps://` (or StartTLS) with `LDAP_CA_FILE` for a private CA; the sync account is read-only.
- [ ] Emergency login is disabled (`AUTH_EMERGENCY_LOGIN_ENABLED=false`, `emergency disable`) except during an outage; its password is in a vault and use raises an alert.
- [ ] Few platform administrators; local accounts are not used for high-risk work (they cannot hold high-risk permissions).
- [ ] Optional modules the organization does not need stay off (Administration > Modules).
- [ ] `AUDIT_RETENTION_DAYS` (0 or at least 365) matches legal requirements; the audit export permission is restricted.
- [ ] Processes run as an unprivileged user (systemd hardening above, or the images' `app` user); the host receives OS updates.
- [ ] Backups are encrypted, stored off-host and restore-tested.
- [ ] Container images are pinned by digest ([Release](release.md), [supply chain](../security/supply-chain.md)).

## 15. Known limits

- **No packaged installer or release artifacts** are documented in the repository; install is from source or locally built images. Published GHCR image names and tags are **not verified**.
- **No migration step in the container images or compose files.** Migrations run with the separate `turaco-migrate` binary.
- **No production first-administrator path in the web UI.** The first administrator comes from `turaco-admin role grant`, which needs a synced directory user or the emergency account. `make dev-setup` and the dev admin are development tooling ([current status](../product/current-status.md)) and must not be used in production. Without a directory the only administrator is the break-glass account, because local accounts cannot be platform administrators ([ADR-0034](../decisions/ADR-0034-local-accounts-and-external-parties.md)). No MFA exists.
- **One `DATABASE_URL` and one database role** for migrations, API and worker.
- **The API does not serve the frontend or terminate TLS.**
- **The single-host trial is not highly available:** the compose file runs one instance of each service and a test double for object storage.
- **Live Intune, Autotask and software provider clients, an endpoint-agent transport and External Parties are not built** ([current status](../product/current-status.md)).
- Windows service packaging and Kubernetes manifests do not exist in the repository.
