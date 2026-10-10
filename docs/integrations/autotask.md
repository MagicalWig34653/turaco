# Autotask Integration

Autotask is an external service-desk/provider integration, not an internal Ticket database.

## Goals
- create/update provider tickets when internal workflow requires external service;
- correlate internal and external references;
- ingest provider updates/webhooks idempotently;
- keep retry/conflict/sync history observable.

Maintain an explicit external-reference mapping `(system, entity_type, internal_id, external_id, sync timestamps/state)` rather than embedding opaque IDs ad hoc.

Email automation may remain a transitional fallback, but native REST/webhook integration is the preferred architecture when available.

## Implementation status

Implemented (F5 part 2): the internal side. `platform.external_references` maps `(system, entity_type, entity_id)` to the external id with `sync_state` (`pending`, `synced`, `failed`), the last error, `last_synced_at`, `external_updated_at` and an attempt counter; `platform.external_events` remembers inbound event ids so replays do nothing. With `AUTOTASK_SYNC=true` the worker marks a ticket pending on `TicketCreated`, `TicketAssigned` and `TicketResolved` and enqueues one deduplicated push job per ticket; the job pushes the public ticket data (never internal comments, queue or device details) through the `TicketGateway` port, records success or failure, retries transient errors with the job runner's back-off and stops on permanent ones. Every request gets a job per reference version; a change that arrives while a push runs leaves the mapping pending and gets its own job, and a job for an already pushed version does nothing. A reopen, close or cancel is pushed too (`TicketStatusChanged`). `ExternalSync.ApplyInbound` applies a reported change idempotently (an external "resolved" or "closed" resolves the ticket as the system actor `autotask`, unknown external records and replays change nothing). Staff see the state at `GET /api/v1/tickets/{id}/external-sync` and `tickets.manage` can request a retry.

Not implemented: the webhook endpoint with signature verification and polling for missed webhooks. The REST client exists but is **implemented per the documentation and not verified against a live Autotask database** (next section). `integrations/autotask` provides the `Gateway` contract, an in-memory `Fake`, the `NotConfigured` placeholder (used while the `AUTOTASK_*` API user is not configured: with the switch on every push then fails visibly with "not configured") and the `REST` client.

## REST client (implemented per documentation, unverified)

**Honest status.** `integrations/autotask` `REST` was written strictly from the Autotask developer documentation pages named in its source comments (read 2026-10-10) and tested only against a local HTTP server that reproduces the documented payload shapes. It has never talked to a real Autotask database. It reports mode `real` and status `unverified` (health error code `unverified`) until a call succeeded; the health check also counts a stored successful push of the worker (`platform.external_references.last_synced_at`) as observed, because pushes run in `turaco-worker`. Without the `AUTOTASK_*` settings the placeholder remains and health says `client_not_configured`.

- **Configuration.** `AUTOTASK_API_USERNAME`, `AUTOTASK_API_SECRET_FILE` (deployment secret file), `AUTOTASK_INTEGRATION_CODE`, `AUTOTASK_COMPANY_ID` (receiving company), optional `AUTOTASK_QUEUE_ID`, `AUTOTASK_STATUS_MAP` and `AUTOTASK_PRIORITY_MAP` (Turaco value to the tenant's picklist value; picklists are tenant specific, so there are no defaults), `AUTOTASK_RATE_LIMIT_PER_HOUR` (default 3000). The API user must be an API-only user with a tracking identifier; SSO is not supported by the API.
- **Zone discovery.** `GET https://webservices.autotask.net/atservicesrest/v1.0/zoneInformation?user=<login>` (no authentication) returns the zone `url`; it is accepted only as `https://webservices<N>.autotask.net` without a port, cached for 12 hours and refreshed once when the credentials are refused (401).
- **Authentication.** The headers `UserName`, `Secret` and `ApiIntegrationCode` plus `Content-Type: application/json` on every call, TLS 1.2 or newer, no redirects, response cap 4 MiB, 30 second timeout. The secret never appears in logs or errors; error text contains only the HTTP status, never response bodies or URLs.
- **Operations.** Create: look the ticket up by Turaco reference (`GET /Tickets/query` with the filter `externalID eq <reference>`), then `POST /Tickets` with `companyID`, `queueID`, `title`, `description`, `status`, `priority`, `externalID` (the reference) and, if present, `resolution`; the answer `{"itemId": n}` is the external id. Update: `PATCH /Tickets` with the record `id` and `title`, `description`, `status`, `priority`, `resolution`. Fields are clipped to the documented lengths (255, 8000, 32000). Turaco-owned fields (title, description, status, priority, resolution) are overwritten on every push; Autotask-owned fields (queue after create, assigned resource, due date, ...) are never sent on update. A change made to the pushed fields in Autotask is therefore reverted by the next push until inbound handling exists.
- **Idempotency.** A retried create whose answer was lost finds the existing ticket by its reference and updates it; several tickets with the same reference are a permanent error.
- **Rate limits.** Autotask allows 10,000 requests per hour per database across all integrations and adds latency above 50 percent usage. Turaco keeps its own ceiling per process (default 3000 per rolling hour, at most two requests in flight). When it is reached the push is a transient failure and the job runner retries later. The documentation names no status code or `Retry-After` for an exceeded limit, so 429 and 5xx are transient, other 4xx permanent (a 401 or 403 means the API user was refused).
- **Not implemented:** attachments, comments, time entries, the `ThresholdInformation` check, a proxy setting, inbound polling and the webhook.

### Permissions in Autotask

The API user needs access to the entity Tickets (create, edit and query) and read access to the Company; the exact security-level settings are not given by the documentation pages read and must be confirmed with a real account.

### Unverified and to check with a real account

Required-field combinations of the ticket category (`dueDateTime`, queue, ticket type, source), the picklist values to put into the two maps, whether `externalID` is filterable and writable on create, whether PATCH accepts `status` and `resolution` together, the exact success status codes (documented as 200), the rate-limit behavior and the thread limit (the page names no numbers), and the lowercase vs camel-case spelling of header names on the wire. Findings from a real run belong into this section.

### Requirements for the REST client and webhook (from the F5 part 2 reviews)

- Creating the external ticket must be idempotent: look the ticket up by its Turaco reference before creating, because a created record whose id was not stored yet (timeout, crash) is retried.
- Gateway errors are stored and shown to staff: they must not contain response bodies, credentials or URLs with secrets.
- With `AUTOTASK_SYNC=true` every ticket's title, description and resolution leaves the system. Deciding which tickets (for example security or HR matters) stay out, and the data-processing terms with the provider, is a prerequisite for switching it on.
- Inbound events are claimed by id before they are applied, but they are not ordered against local changes: an older "resolved" event delivered for the first time after a reopen still resolves the ticket. The adapter should drop events older than the last local change it knows of.
- `platform.external_events` has no retention yet; add a cleanup job together with the webhook. The worker and the API must share the `AUTOTASK_SYNC` setting.
