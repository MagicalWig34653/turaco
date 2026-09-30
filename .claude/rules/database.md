---
paths:
  - "backend/migrations/**/*.sql"
  - "backend/internal/**/*repository*"
---
# Database rules

- PostgreSQL 18 is the canonical DB.
- Released migrations are immutable; corrections use new forward migrations.
- Prefer explicit SQL and database constraints for hard invariants.
- Use `uuidv7()` for new entity IDs unless a domain-specific reason exists.
- Cross-module DB access is prohibited even when tables share one database.
- Transactions include the domain mutation and matching outbox event when atomic delivery is required.
- Do not add derived/materialized data without defining reconciliation semantics.
