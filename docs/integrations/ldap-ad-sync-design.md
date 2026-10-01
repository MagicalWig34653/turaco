# F1 Slice 3 — LDAP/AD Directory Sync: Feature Design

**Status:** Accepted 2026-10-01 (decisions D1–D4 below). Implementation in progress; [current status](../product/current-status.md) is authoritative for what exists.

Authoritative context: [LDAP/AD integration](ldap-ad.md), [core data model](../domain/core-data-model.md), [module boundaries](../architecture/module-boundaries.md), [agent boundaries](../security/agent-boundaries.md), ADR-0006, ADR-0013, ADR-0014, ADR-0022.

## Decisions

- **D1 Credentials/config:** one directory per deployment, configured by environment (`LDAP_*`, see [configuration reference](../reference/configuration.md)); the bind password comes from `LDAP_BIND_PASSWORD_FILE` (deployment secret). Nothing credential-related is stored in PostgreSQL. DB-managed, encrypted multi-directory configuration waits for ADR-0014 key management.
- **D2 Linking:** sync never links a directory account to an existing User. Only `(provider_key, external_subject)` matches. A new account whose email belongs to an existing User is skipped and recorded as a run conflict.
- **D3 Membership history:** observed User→Group memberships and Group→Group nesting are interval rows (`observed_from`, `last_observed_at`, `observed_until`); `observed_until IS NULL` means currently observed.
- **D4 Execution:** a minimal PostgreSQL job runner in `turaco-worker` (ADR-0006) runs sync on a schedule and on manual request.

## 1. Outcome

Turaco learns Users, Directory Groups and observed memberships from an on-prem Active Directory/LDAP directory with source and freshness, so later slices can authenticate known Users (slices 3b/4), map Directory Groups to roles (slice 5) and explain management targeting (F6). An account disabled or removed in the directory stops being `active`, which already invalidates its sessions.

## 2. Reused concepts

User, External Identity, Directory Group, Directory Group Membership, Data Source/Freshness, Source of Truth (`external_wins` for directory-owned fields), Audit Event, outbox event `UserSynchronized` (already registered), Scheduled Job/`platform.jobs`, configuration and permission registries.

## 3. New concepts

- **Directory Sync Run** (`organization.directory_sync_runs`): one execution against one provider with trigger, outcome, counts and conflicts. A technical execution record, not a glossary noun; it is where conflicts become visible instead of hidden log lines.
- **Directory group nesting** (`organization.directory_group_nesting`): observed direct Group→Group edges, the group-member counterpart of Directory Group Membership.
- **Job runner** (`backend/internal/platform/jobs`): realizes the already-accepted ADR-0006 queue.

## 4. Ownership and flow

```text
turaco-worker
  jobs.Runner ── claims "organization.directory_sync" ──> handler (wired in cmd/turaco-worker)
                                                          │
  integrations/ldap.Source ── Fetch() ──> organization/public.DirectorySnapshot
                                                          │
  organization/public.DirectorySync.Run(ctx, source, trigger)
      start run → fetch → apply snapshot in one transaction → finish run
```

- `backend/internal/integrations/ldap` speaks LDAP (ADR-0022) and returns `public.DirectorySnapshot`. It imports only `organization/public`.
- The Organization module owns run records and all writes to its tables.
- `backend/internal/platform/jobs` knows nothing about Organization.
- Connector Agent is not involved; a future `ldap.users.read`/`ldap.groups.read` capability returns the same snapshot contract.

## 5. Sync algorithm

`Run` steps:

1. **Start run** (own transaction): mark this provider's `running` runs older than `LDAP_SYNC_TIMEOUT` as `failed` with error `abandoned`; insert a `running` run. If the partial unique index rejects it, return `ErrSyncAlreadyRunning` (the job retries later).
2. **Fetch** the snapshot with a context deadline of `LDAP_SYNC_TIMEOUT`. On error, finish the run as `failed` with a sanitized error and return the error (retryable).
3. **Apply** the snapshot in **one transaction**, with `observed_at` = the time the fetch completed:
   1. Validate the snapshot: non-empty, unique `ExternalID`s, and no user or group IDs used twice. An invalid snapshot fails the run (no changes).
   2. **Safeguard.** `active_before` is the number of this provider's identities with `enabled AND deleted_observed_at IS NULL`. `deactivations` is how many of those are missing from the snapshot or disabled in it. If `deactivations > 5` and `deactivations * 100 > LDAP_SYNC_MAX_DEACTIVATION_PERCENT * active_before`, roll back, finish the run as `aborted_safeguard`, audit `organization.directory_sync.aborted`, and return `jobs.Permanent(ErrSyncSafeguard)`.
   3. **Users.** For each snapshot user, look up the identity by `(provider_key, external_subject)`:
      - **Existing identity.** Compute `attributes_hash` (SHA-256 over a canonical encoding of every directory field except the manager). If the hash is unchanged and the identity is not deleted, only collect it for the bulk `last_seen_at` update. Otherwise update the identity (`username`, `distinguished_name`, `enabled`, hash, `deleted_observed_at = NULL`, `last_seen_at`) and the User's directory-owned fields. An email already used by another User is not applied: keep the old value and record conflict `email_in_use`.
      - **New account.** If the email belongs to an existing User (case-insensitive), record conflict `email_in_use` and skip (D2). Otherwise insert the User with `status = active` if enabled else `inactive`, `status_source = directory`, and the identity, then audit `organization.user.created_from_directory`.
   4. **Not observed.** Set `enabled = false, deleted_observed_at = observed_at` on this provider's non-deleted identities that are missing from the snapshot.
   5. **Status.** For every User touched by steps 3–4, apply the status rule below. Audit each status change as `organization.user.status_changed` with before/after `{status, statusSource}`. When a User leaves `active`, revoke all of their sessions in the same transaction (`authentication.RevokeUserSessions`, reason `user_deactivated`, audited per session), so a later reactivation never revives an old session.
   6. **Managers.** Map the snapshot's external IDs to user IDs and set `manager_user_id`. A manager ID that cannot be mapped (skipped by a conflict) or `ManagerUnresolved` sets null and records conflict `manager_unresolved`. A self-reference is set to null.
   7. **Groups.** Upsert by `(provider_key, external_id)`, setting `display_name`, `description`, `last_observed_at = observed_at` and `deleted_observed_at = NULL` (`first_observed_at` is set only on insert). Groups missing from the snapshot get `deleted_observed_at = observed_at` if it is null.
   8. **Memberships and nesting**, for each observed group:
      - set `last_observed_at` on open rows still in the member set;
      - close open rows that are no longer members (`observed_until = observed_at`);
      - insert new open rows (`observed_from = last_observed_at = observed_at`).
      Open rows of groups marked deleted are closed. Member user IDs not mapped to a User (skipped accounts) count as `unresolvedMembers`.
   9. Emit outbox `UserSynchronized` v1 for each created or changed User. The payload is `{userId, providerKey, created, changedFields[], statusChanged}`, with field names only and no values.
   10. Finish the run as `succeeded` with counts and conflicts, in the same transaction.

**Status rule.** A User with at least one enabled, non-deleted external identity is "directory-enabled".

| Current status | Directory-enabled | Not directory-enabled |
| --- | --- | --- |
| `active` | unchanged | `inactive`, `status_source = directory` |
| `inactive` with `status_source = directory` | `active` | unchanged |
| `inactive` with `status_source = platform`, `departed`, `external`, `unknown` | unchanged | unchanged |

Sync never sets `departed`: leaving the company is a business fact, not a directory fact.

**Counts** (`counts` jsonb): `usersObserved`, `usersCreated`, `usersUpdated`, `usersUnchanged`, `usersNotObserved`, `usersActivated`, `usersDeactivated`, `groupsObserved`, `groupsCreated`, `groupsUpdated`, `groupsNotObserved`, `membershipsOpened`, `membershipsClosed`, `nestingOpened`, `nestingClosed`, `unresolvedMembers`, `sessionsRevoked`.

**Conflicts** (`conflicts` jsonb, at most 100 stored; `conflict_count` is the total): `{kind, externalId, username}`, where `kind` is one of `email_in_use` or `manager_unresolved`. Never emails or other attribute values.

**Idempotency.** Applying the same snapshot twice changes only `last_seen_at`/`last_observed_at` and emits no audit or outbox rows. A retried job starts a new run.

## 6. Data model

- Migration `000008_platform_job_runner`: `platform.jobs.dedupe_key` with a partial unique index over pending/processing jobs, a revised claim index and constraints.
- Migration `000009_directory_sync`:
  - `external_identities` gains `deleted_observed_at` and `attributes_hash`;
  - `users` gains `status_source`;
  - `directory_groups` gains the constraints deferred from 000006;
  - `directory_group_memberships` becomes interval history (`id` PK, `observed_from`, `observed_until`, partial unique index on open pairs);
  - new tables `directory_group_nesting` and `directory_sync_runs` (partial unique index: one running run per provider).

## 7. API

- `GET /api/v1/directory-sync-runs?providerKey=&limit=&cursor=` and `GET /api/v1/directory-sync-runs/{id}`: require `organization.directory.view` and `organization.view` (conflicts carry usernames).
- `POST /api/v1/directory-sync-runs`: requires `organization.directory.sync`; enqueues a manual run job (deduplicated per provider) and audits `organization.directory_sync.requested` with the actor. Returns `202 {jobId, created}`, or `409 organization.directory_sync_not_configured` when LDAP is not configured on the API. Because sessions carry no permissions until slice 5, this endpoint returns 403 to all callers until then.
- `GET /api/v1/users/{id}` adds `externalIdentities[]`: `{providerKey, username, enabled, lastSeenAt, deletedObservedAt}`. The subject and DN are not exposed.
- `GET /api/v1/directory-groups/{id}/members` returns only currently observed members (`observed_until IS NULL`) and adds `observedFrom`.

## 8. Permissions

`organization.directory.view` and `organization.view` for run history; new `organization.directory.sync` (elevated) to request a run.

## 9. Audit

Actor `null` with metadata `{"actor":"directory-sync","providerKey":…,"runId":…}` for sync-originated actions; correlation ID = run ID.

- `organization.user.created_from_directory` (target user)
- `organization.user.status_changed` (target user, before/after)
- `organization.directory_sync.aborted` (target run)
- `organization.directory_sync.requested` (human actor; target provider key)

Attribute refreshes and membership observations are observations with their own freshness, not audit events.

## 10. Jobs

- Job type `organization.directory_sync`, payload `{providerKey, trigger}`, dedupe key `organization.directory_sync:<providerKey>`, max attempts 3.
- Runner: `FOR UPDATE SKIP LOCKED` claim of `pending` jobs due now, or `processing` jobs locked longer than the lock timeout. Retryable errors return to `pending` with exponential backoff (30s·2ⁿ, capped at 30m). Permanent errors or exhausted attempts set `failed`. Completion updates are guarded by `locked_by`.
- Scheduler: every tick, enqueue the job if there is no active job for the dedupe key and the latest job for that key was created more than `LDAP_SYNC_INTERVAL` ago.

## 11. Search, relationships, timeline, My Work, notifications

None yet; those platform services do not exist. Failed or aborted runs become IT Briefing items in F8.

## 12. Directory mapping and source of truth

| User field | Active Directory | OpenLDAP | Owner |
| --- | --- | --- | --- |
| external subject | `objectGUID` (binary, mixed-endian → canonical UUID) | `entryUUID` | directory |
| username | `sAMAccountName` | `uid` | directory |
| display_name | `displayName`, else `cn` | `displayName`, else `cn` | directory |
| given_name / family_name | `givenName` / `sn` | `givenName` / `sn` | directory |
| primary_email | `mail` | `mail` | directory |
| employee_number | `employeeID`, else `employeeNumber` | `employeeNumber` | directory |
| manager_user_id | `manager` DN → snapshot user | `manager` DN → snapshot user | directory |
| enabled | `userAccountControl` bit 0x2 clear | always true | directory |
| status | derived (status rule) | derived | shared, see status rule |
| department, location, cost center | — | — | platform (later mapping slice) |

Group: `objectGUID`/`entryUUID`, `cn`/`displayName`, `description`, `member` (AD range retrieval `member;range=…` for large groups).

LDAP requirements:

- LDAPS, or StartTLS (plain LDAP only with `APP_ENV=development`); certificate verification always on, with an optional additional CA file.
- Simple bind with a read-only service account; paged search (page size 500).
- Explicit attribute allowlist, so `userPassword`, `unicodePwd`, `ms-Mcs-AdmPwd` and similar are never requested.
- Errors never contain attribute values; base DNs and filters come only from configuration.

## 13. Security and privacy

- Bind password only in a deployment secret file, read at worker start; never logged, persisted or returned.
- Account takeover via email is prevented by D2.
- The mass-deactivation safeguard protects against a wrong base DN or filter, or a partially visible directory.
- Personal data is minimized: the attribute allowlist, conflicts without values, and DN/subject are not exposed by the API.
- Sync does not grant permissions; group-to-role mapping is slice 5.

## 14. Tests

- **Integration package:** mapping (GUID decoding, UAC flags, DN resolution, range retrieval, allowlist) against a fake `ldap.Client`; TLS configuration.
- **Organization (PostgreSQL, required in CI and cloud):** create, update, unchanged/idempotency, disable/enable status rule, not-observed sweep, safeguard abort, email conflicts, managers, group upsert/deletion, membership and nesting intervals, audit and outbox rows, run lifecycle including abandoned runs and concurrency, read-API filtering.
- **Jobs (PostgreSQL):** enqueue dedupe, claim with SKIP LOCKED under concurrency, retry/backoff, permanent failure, stale-lock reclaim, scheduler.
- **Live directory:** not automated in this slice; run manually against a test AD/OpenLDAP (see [LDAP/AD integration](ldap-ad.md)).

## 15. ADR

ADR-0022 (go-ldap). The job runner implements ADR-0006 and needs no new ADR.
