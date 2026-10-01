# F1 Slice 3 — LDAP/AD Directory Sync: Feature Design

**Status:** Proposed — awaiting decisions D1–D4 (see "Open decisions"). Not implemented.

Authoritative context: [LDAP/AD integration](ldap-ad.md), [core data model](../domain/core-data-model.md), [module boundaries](../architecture/module-boundaries.md), [agent boundaries](../security/agent-boundaries.md), ADR-0006, ADR-0013, ADR-0014.

## 1. Outcome

Turaco learns Users, Directory Groups and observed User→Group memberships from an on-prem Active Directory/LDAP directory, with source and freshness, so that later slices can:

- authenticate against a known User (LDAP bind in slice 3b/Kerberos in slice 4 need a User to resolve to),
- map Directory Groups to roles (slice 5),
- explain management targeting (F6).

A User disabled or removed in the directory stops being `active`, which already invalidates their sessions (slice 2).

## 2. Reused concepts

User, External Identity (`organization.external_identities`, `(provider_key, external_subject)`), Directory Group, Directory Group Membership, Data Source/Freshness, Source of Truth (`external_wins` for directory-owned fields), Audit Event, Scheduled Job/`platform.jobs` (ADR-0006), configuration registry.

## 3. New concepts

- **Directory Sync Run** — one execution of a Scheduled Job against one directory provider, with start/end, outcome, counts and conflicts. It is a technical execution record, not a domain noun for the glossary; it is the provenance anchor for "observed at" and the place where conflicts become visible instead of hidden log lines. It belongs in `organization` because it carries organization-specific counts and conflicts.
- **Directory Provider** (configuration, not a table in this slice) — a configured directory with `provider_key` (for example `ad-main`), URL, base DNs, filters and bind identity.

No new glossary noun is needed.

## 4. Ownership

- **Organization module** owns the sync application operation (`ApplyDirectorySnapshot`), the persistence and the run records. Only Organization writes its tables.
- **`backend/internal/integrations/ldap`** (new integration package) speaks LDAP and converts entries into a vendor-neutral `organization/public` snapshot contract. No LDAP types cross into the module (module-boundaries: "integrations are adapters").
- **Platform** gains a minimal job runner in `turaco-worker` (claim with `FOR UPDATE SKIP LOCKED`, retry with backoff, observable failure) if D4 selects it.
- Connector Agent: not in this slice. The integration produces the same snapshot contract that a future Connector Agent `ldap.users.read`/`ldap.groups.read` capability will deliver for hosted tenants.

## 5. State and lifecycle

User status mapping (directory-owned only for users that have a directory External Identity):

| Directory observation | External Identity | User.status |
| --- | --- | --- |
| enabled account | `enabled=true`, `last_seen_at=run` | `active` (only from `inactive` set by sync; never overrides `departed`/`external`) |
| disabled (`userAccountControl` ACCOUNTDISABLE) | `enabled=false` | `inactive` |
| not observed in a complete successful run | `enabled=false`, `deleted_observed_at=run` | `inactive` |

Sync never sets `departed`; leaving the company is a business fact, not a directory fact. Status changes are audited.

Directory Group: `first_observed_at`/`last_observed_at`/`deleted_observed_at` already exist; a group missing from a complete run gets `deleted_observed_at`; reappearing clears it.

Membership: see D3.

**Mass-change safeguard:** objects are marked not-observed only after a *complete* paged enumeration succeeded. If a run would deactivate more than `LDAP_SYNC_MAX_DEACTIVATION_PERCENT` (default 10) of directory users, the run fails with `aborted_safeguard` and changes nothing. This protects against a wrong base DN or filter deactivating the company.

## 6. Data model / migrations (forward only)

- `000008`: `organization.external_identities` add `deleted_observed_at timestamptz`, `attributes_hash text` (skip no-op updates); partial index on `(provider_key) WHERE deleted_observed_at IS NULL`.
- `organization.directory_sync_runs` (`id`, `provider_key`, `started_at`, `finished_at`, `outcome` CHECK in `running|succeeded|failed|aborted_safeguard`, counts jsonb, `conflicts` jsonb bounded, `error`), index `(provider_key, started_at DESC)`.
- Membership shape per D3; adds the CHECK constraints listed as deferred in current-status.
- Group nesting: `organization.directory_group_nesting(parent_group_id, child_group_id, last_observed_at, …)` storing *direct* observed edges only. Transitive membership is a later derived read model; sync does not expand nesting.
- `users` gains no per-field provenance columns; directory-owned fields are documented (section 12) and freshness comes from the External Identity `last_seen_at` and the run record.

Users are matched only via `(provider_key, external_subject)` with `external_subject` = `objectGUID` (or `entryUUID` for non-AD LDAP), never DN or email.

## 7. API surface

Read-only additions (permission `organization.read`, existing):

- `GET /api/v1/organization/directory-sync-runs?provider=` (paginated)
- `GET /api/v1/organization/directory-sync-runs/{id}`
- Existing User/Directory Group responses add source/freshness fields (`externalIdentities[].lastSeenAt`, membership `lastObservedAt`).

Triggering a run on demand (`POST …/directory-sync-runs`) requires a new permission `organization.directory.sync` and is audited; it is deliverable only once slice 5 grants permissions, so it is part of this slice's API but inert until then.

## 8. Permissions

- `organization.read` — existing, for run history.
- `organization.directory.sync` — new, trigger a run. Registered in the permission registry and generated reference.

## 9. Audit

Audit (actor = system principal `directory-sync:<provider_key>`, correlation id = run id):

- User created from directory, User status changed by sync, External Identity linked/disabled.
- Run triggered manually (with human actor).
- Run aborted by safeguard.

Not audited individually: attribute refreshes and membership observations; they are observations recorded with freshness in their own tables. The run record holds counts.

## 10. Events, jobs, idempotency

- Job type `organization.directory_sync` with payload `{provider_key}`; one running run per provider (advisory lock keyed by provider).
- Schedule: `LDAP_SYNC_INTERVAL` (default 1h); the worker enqueues when no pending/running job exists.
- Idempotent by construction: applying the same snapshot twice produces no changes (hash compare) and no audit events.
- Writes are batched per object in short transactions; the not-observed sweep and run completion happen in one final transaction.
- Outbox events (registered, generated reference): `organization.user.created`, `organization.user.status_changed`, `organization.directory_sync_run.completed`. No consumers yet.

## 11. Search / relationships / timeline / My Work / notifications

No search index or relationship rows yet (platform services not implemented). User timeline later reads audit events. No My Work or notification impact in this slice; a failed or aborted run becomes an IT Briefing item in F8.

## 12. Integration details

Directory-owned User fields (`external_wins`): `display_name`, `given_name`, `family_name`, `primary_email`, `employee_number`, `manager_user_id` (resolved through the manager's External Identity in the same provider; unresolved means null plus a conflict entry). Platform-owned and untouched: `department_id`, `primary_location_id` (need Department/Location mapping; later slice), `status` values `departed`/`external`.

LDAP specifics: LDAPS or StartTLS required (plain LDAP refused unless `APP_ENV=development`); certificate verification always on, optional custom CA file; paged results (RFC 2696); attribute allowlist (no `userPassword`, `unicodePwd`, `ms-Mcs-AdmPwd` etc. ever requested); configurable user/group filters.

## 13. Security and privacy

- Bind password never stored in the DB in this slice (D1); never logged; read-only service account with least privilege.
- Directory data is personal data: attribute allowlist; no raw entry dumps in logs, run records or errors.
- Linking policy prevents account takeover through a directory account claiming an existing User's email (D2).
- Safeguard against mass deactivation (section 5).
- LDAP injection: filters are configuration, not user input; any value interpolated into filters is escaped.

## 14. Tests and documentation

- Unit: entry→snapshot mapping (objectGUID decoding, UAC flags, manager DN resolution, attribute allowlist).
- Repository/application (PostgreSQL, required in CI): create/update/no-op idempotency, disable, not-observed sweep, safeguard abort, membership history, nesting, linking-policy conflicts, audit rows.
- Integration: OpenLDAP container (`osixia`/`bitnami`-class image pinned by digest) in a test-only compose profile, exercising paging and TLS. AD-only attributes are covered by unit fixtures.
- Docs: this design becomes `ldap-ad.md` sections; current-status, core-data-model (membership history), generated permissions/events/configuration, OpenAPI, security architecture.

## 15. ADR

Yes: **ADR-0022 LDAP client library** (`github.com/go-ldap/ldap/v3`; pure Go, maintained, BSD-licensed, used by Grafana, Gitea and Vault). Also records the test LDAP server image if it joins the dev compose.

## 16. Parallelization

After migrations and the `organization/public` snapshot contract are written by the lead:

- backend-implementer (Sonnet): Organization application/repository/run API; owns `backend/internal/modules/organization/**`, `backend/migrations/000008*`.
- integration-implementer (Sonnet): `backend/internal/integrations/ldap/**`, test LDAP fixtures.
- backend-implementer #2 (Sonnet), only if D4 = job runner: `backend/internal/platform/jobs/**`, `backend/cmd/turaco-worker/**`.

Reviews (Opus): database-reviewer (migration/history), security-reviewer (LDAP/TLS/linking), architecture-reviewer. No frontend in this slice.

## Open decisions

- **D1 Bind credentials/config:** deployment secret (env/file, one directory) now vs implement ADR-0014 encryption and DB-managed connections first.
- **D2 Linking an existing User:** never auto-link (conflict recorded) vs link by unique email when the User has no identity for this provider.
- **D3 Membership history:** interval rows (`observed_from`/`observed_until`) vs a single soft-removal marker per pair.
- **D4 Execution:** minimal platform job runner in the worker vs a `turaco-directory-sync` CLI command for now.
