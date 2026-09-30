---
paths:
  - "**/*.go"
---
# Go rules

- Prefer the standard library and small focused dependencies.
- Keep packages cohesive; avoid generic `utils` packages.
- Domain packages do not import HTTP, PostgreSQL or vendor SDK packages.
- Return contextual errors with `%w`; do not log and return the same error at multiple layers.
- `context.Context` is the first parameter for I/O/application operations and is never stored in structs.
- Prefer concrete types; introduce interfaces at real boundaries, usually where consumed.
- State transitions are explicit methods/use cases, never arbitrary status writes.
- Tests use table-driven style where it improves clarity, not mechanically.
