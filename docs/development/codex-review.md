# Optional Codex Review

Codex may be used as an independent second-model reviewer when access is available. Turaco does not require it to build, test or contribute.

## Recommended role

Prefer independent review over duplicate implementation. Give Codex the requirement/design and the resulting diff, then ask it to search for concrete issues.

Suggested review focus:

1. functional correctness and edge cases;
2. authorization/tenant boundaries/security;
3. SQL/migrations/locking/data integrity;
4. retry/idempotency/concurrency;
5. module-boundary and source-of-truth violations;
6. missing regression/adversarial tests;
7. stale documentation.

Ask for findings ordered by severity with exact file/line references and a short explanation. Avoid asking it to rewrite unrelated code merely for style.

All accepted changes still pass Turaco's normal review and `make check`.
