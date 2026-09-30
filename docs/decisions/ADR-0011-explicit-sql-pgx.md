# ADR-0011: Explicit SQL with pgx

- Status: Accepted

## Decision

Use PostgreSQL through `pgx` and explicit SQL. A small generated query layer may be introduced by a later ADR if it materially improves safety without hiding SQL behavior. Do not introduce a heavy ORM by default.

## Consequences

Queries remain inspectable, migrations remain explicit, and performance/debugging behavior is easier for both humans and coding agents to reason about.
