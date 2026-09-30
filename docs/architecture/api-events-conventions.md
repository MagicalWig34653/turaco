# API and Event Conventions

## HTTP API

- Prefix public application APIs with `/api/v1`.
- Use REST resources for ordinary CRUD/read behavior.
- Use explicit action endpoints for meaningful domain transitions such as `/assets/{id}/assign`.
- Never expose persistence structs as the API contract.
- All list endpoints are bounded and paginated.
- Errors use a stable machine-readable code and a request/correlation identifier.
- Authorization is enforced on the server; frontend visibility is not a security boundary.

## Identifiers

- Persistent internal IDs use UUIDv7.
- Human references (`INC-...`, `AST-...`) are separate from primary keys.

## Domain events

- Event names are past-tense facts.
- State changes and outbox insertion commit in the same database transaction.
- Persisted/external event contracts carry a version.
- Consumers are idempotent.
- Domain events do not automatically become public integration events.

## Correlation

A correlation ID follows an operation through HTTP, domain logic, outbox, background jobs, external integrations, audit and logs.
