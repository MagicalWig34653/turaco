# Domain Module Template

A substantial backend module should normally contain only the layers it needs:

```text
backend/internal/modules/<module>/
  domain/          pure business concepts and invariants
  application/     use cases and transaction orchestration
  repository/      PostgreSQL implementation (private to the module)
  transport/       HTTP/other inbound transport adapters
  public/          deliberately small contracts usable by other modules
```

Do not create empty layers for ceremony.

Before adding a module, document:

- owner and purpose,
- existing platform concepts reused,
- genuinely new entities,
- public interfaces,
- persistence ownership,
- permissions,
- events,
- search/timeline/My Work behavior,
- security implications.

A new navigation page is not sufficient reason for a new module.
