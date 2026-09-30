# Event contracts

Stable domain/integration event contracts belong here when they need an external schema. The generated event catalog lives at `docs/reference/events.md` and is driven by the code registry.

Rules:

- Events are past-tense facts.
- Persisted/external contracts are versioned.
- Internal implementation details are not event API.
- Breaking contract changes require explicit versioning and compatibility review.
