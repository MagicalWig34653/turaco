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

Implemented (F5 part 2): the internal side. `platform.external_references` maps `(system, entity_type, entity_id)` to the external id with `sync_state` (`pending`, `synced`, `failed`), the last error, `last_synced_at`, `external_updated_at` and an attempt counter; `platform.external_events` remembers inbound event ids so replays do nothing. With `AUTOTASK_SYNC=true` the worker marks a ticket pending on `TicketCreated`, `TicketAssigned` and `TicketResolved` and enqueues one deduplicated push job per ticket; the job pushes the public ticket data (never internal comments, queue or device details) through the `TicketGateway` port, records success or failure, retries transient errors with the job runner's back-off and stops on permanent ones. A change that arrives while a push runs leaves the mapping pending, so the next attempt pushes the newer state. `ExternalSync.ApplyInbound` applies a reported change idempotently (an external "resolved" or "closed" resolves the ticket as the system actor `autotask`, unknown external records and replays change nothing). Staff see the state at `GET /api/v1/tickets/{id}/external-sync` and `tickets.manage` can request a retry.

Not implemented: the Autotask REST client (authentication, ticket mapping, field and status translation, comment sync), the webhook endpoint with signature verification, and polling for missed webhooks. They need a tenant, API credentials and a sandbox to be built and verified; `integrations/autotask` provides the `Gateway` contract, an in-memory `Fake` and the `NotConfigured` placeholder the worker uses today, so with the switch on every push fails visibly with "not configured".
