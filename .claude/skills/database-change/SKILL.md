---
name: database-change
description: Design and review a Turaco PostgreSQL schema/migration/transaction change with data integrity and deployment safety first.
argument-hint: <database change>
model: claude-opus-5-5
effort: high
---
# Database Change

Analyze `$ARGUMENTS` before writing/releasing migrations.

Check:

1. module/schema ownership;
2. invariants enforced by constraints/indexes;
3. forward migration and existing-data transformation;
4. locking/downtime characteristics;
5. transaction boundaries and concurrency;
6. idempotency/uniqueness;
7. query/index impact;
8. cross-module foreign-key coupling;
9. backup/restore/rollback-operational implications;
10. tests against real PostgreSQL semantics.

Released migrations are immutable; corrections use new migrations.
