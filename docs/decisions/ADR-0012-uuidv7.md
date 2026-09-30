# ADR-0012: UUIDv7 Internal Identifiers

- Status: Accepted

## Decision

Use UUIDv7 for persistent domain identifiers. Human-readable references are separate values.

## Consequences

IDs remain globally unique and time-sortable without making user-facing numbering part of referential integrity.
