# Turaco Agent Instructions

This file is for coding/review agents other than Claude Code (for example Codex). `CLAUDE.md` remains the Claude-specific entry point.

Before reviewing or changing Turaco:

1. Read `docs/architecture/constitution.md`.
2. Read `docs/domain/glossary.md`.
3. Read `docs/product/current-status.md`.
4. Read relevant ADR/domain/module docs.
5. Inspect existing implementation and tests; do not infer planned features from documentation alone.

Core rules:

- modular monolith; respect module ownership and public boundaries;
- reuse shared platform concepts before introducing parallel mechanisms;
- technical internals are English; UI text uses i18n;
- explicit SQL/PostgreSQL; no new ORM/framework/broker/search engine without ADR;
- explicit domain transitions, backend authorization, audit for important mutations;
- external observations preserve source/freshness;
- Connector and Endpoint agents are restricted trust boundaries, not generic shells;
- migrations are forward-only after release;
- documentation and tests are part of the change.

For independent review, prioritize correctness, security, data integrity, concurrency/idempotency, architectural drift and missing tests. Avoid style-only churn.

Run `make check` before declaring an implementation complete when the environment permits it.
