# Autotask Integration

Autotask is an external service-desk/provider integration, not an internal Ticket database.

## Goals
- create/update provider tickets when internal workflow requires external service;
- correlate internal and external references;
- ingest provider updates/webhooks idempotently;
- keep retry/conflict/sync history observable.

Maintain an explicit external-reference mapping `(system, entity_type, internal_id, external_id, sync timestamps/state)` rather than embedding opaque IDs ad hoc.

Email automation may remain a transitional fallback, but native REST/webhook integration is the preferred architecture when available.
