# LDAP / Active Directory Integration

## Responsibilities

Separate concerns:
- authentication / transparent SSO integration;
- directory synchronization;
- group-to-role mapping;
- computer discovery where useful.

On-prem Platform may connect directly over LDAPS. Hosted Platform can use Connector Agent or customer-approved private connectivity. Do not expose LDAP directly to the public Internet.

## User model

AD/LDAP account maps to ExternalIdentity; User is the stable platform identity. Directory changes update fields whose Source of Truth is directory-owned without overwriting platform-owned history.

## Passwords

LDAP user passwords are never stored. The synchronization bind password is a deployment secret file read only by `turaco-worker` (decision D1 of the [sync design](ldap-ad-sync-design.md)); it is never stored in PostgreSQL. Storing directory credentials in the database requires the application-level encryption of ADR-0014 first. Interactive agent authentication must be explicitly capability-scoped and audited without recording user passwords.

## Transparent Windows experience

Kerberos/SPNEGO may provide browser SSO in suitable domain environments, either directly or through a dedicated identity layer. The platform consumes a trusted authenticated identity; it does not make anonymous identity guesses.

## Directory synchronization (F1 slice 3)

Design and exact semantics: [LDAP/AD Directory Sync Design](ldap-ad-sync-design.md).

- One directory per deployment, configured with `LDAP_*` variables ([configuration reference](../reference/configuration.md)). Synchronization is disabled while `LDAP_URL` is empty.
- `turaco-worker` runs a sync every `LDAP_SYNC_INTERVAL`; `POST /api/v1/directory-sync-runs` requests an immediate run (`organization.directory.sync`). Run history, counts and conflicts are readable through `GET /api/v1/directory-sync-runs`.
- Users are matched only by the immutable directory ID (`objectGUID` / `entryUUID`). Turaco never links a directory account to an existing User by email; such accounts are reported as `email_in_use` conflicts.
- Directory-owned User fields are overwritten by the directory; department, location and cost center remain platform-owned for now.
- A disabled or removed account makes its User `inactive` and revokes the User's sessions. Sync never marks a User `departed`.
- A disabled or removed account makes its User `inactive`; an account with malformed identity attributes is kept as observed and reported as an `invalid_attributes` conflict instead of failing the run.
- Mass-removal safeguard: when more than `LDAP_SYNC_MAX_MISSING_PERCENT` (and more than 5) of the provider's active users, or of its groups, are missing from a run, that "not observed" sweep is withheld and the run ends as `sweep_withheld` (explicit disables are still applied). This protects against a wrong base DN or filter. After verifying an intended large removal, raise the percentage for one run and restore it.
- Changing `LDAP_PROVIDER_KEY` after the first sync is refused (runs fail permanently): it would orphan all existing directory Users.
- `turaco-api` only needs `LDAP_URL` and `LDAP_PROVIDER_KEY` (to accept manual run requests); the bind secret is mounted only into `turaco-worker`. Both must use the same values.

### Service account and secret

Use a dedicated, read-only directory account; it needs read access to the configured user and group subtrees only (in AD the default authenticated-user read access is sufficient; do not grant Replicating Directory Changes or admin rights). Provide its password as a file (Docker/Swarm secret) and set `LDAP_BIND_PASSWORD_FILE`; the password is never put into environment variables, the database or logs.

### TLS

Use `ldaps://` (port 636) or `ldap://` with `LDAP_START_TLS=true`. Certificate verification cannot be disabled; for an internal CA set `LDAP_CA_FILE`. Plain LDAP is accepted only with both `LDAP_ALLOW_PLAINTEXT=true` and `APP_ENV=development` (local test directories).

### Testing against a real directory

Automated tests use fake directory connections. Before enabling sync in an environment, run it once against a test AD/OpenLDAP with a deliberately small `LDAP_USER_BASE_DN` and check the first run's counts and conflicts.
