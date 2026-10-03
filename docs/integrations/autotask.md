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

Not implemented: the Autotask REST client (authentication, ticket mapping, field and status translation, comment sync), the webhook endpoint with signature verification, and polling for missed webhooks. They need a tenant, API credentials and a sandbox to be built and verified; `integrations/autotask` provides the `Gateway` contract, an in-memory `Fake` and the `NotConfigured` placeholder the worker uses today, so with the switch on every push fails visibly with "not configured".

### Requirements for the REST client and webhook (from the F5 part 2 reviews)

- Creating the external ticket must be idempotent: look the ticket up by its Turaco reference before creating, because a created record whose id was not stored yet (timeout, crash) is retried.
- Gateway errors are stored and shown to staff: they must not contain response bodies, credentials or URLs with secrets.
- With `AUTOTASK_SYNC=true` every ticket's title, description and resolution leaves the system. Deciding which tickets (for example security or HR matters) stay out, and the data-processing terms with the provider, is a prerequisite for switching it on.
- Inbound events are claimed by id before they are applied, but they are not ordered against local changes: an older "resolved" event delivered for the first time after a reopen still resolves the ticket. The adapter should drop events older than the last local change it knows of.
- `platform.external_events` has no retention yet; add a cleanup job together with the webhook. The worker and the API must share the `AUTOTASK_SYNC` setting.
