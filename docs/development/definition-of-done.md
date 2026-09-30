# Claude Code Definition of Done

A change is complete only when all applicable categories are satisfied.

## Before implementation
- inspect `CLAUDE.md`, relevant architecture/domain docs, ADRs, code and tests;
- map substantial work through `/feature-design`;
- prefer existing patterns over a new abstraction.

## Domain/architecture
- canonical Glossary terminology used;
- ownership/module boundary respected;
- no duplicate platform service concept;
- new framework/infrastructure/major dependency has accepted ADR;
- lifecycle/state changes use explicit operations and documented transitions.

## Data
- migrations included and forward-safe;
- released migrations untouched;
- constraints/indexes considered;
- source-of-truth and freshness defined for synced data;
- concurrency/idempotency handled for retryable or contested operations.

## Authorization/security
- backend permission/scope enforced;
- tenant/IDOR/input/file/outbound-request risks reviewed;
- no secret/sensitive logging;
- secrets use shared encrypted storage;
- agent capabilities remain typed/explicit/expiring;
- security-sensitive actions audited.

## External management/provider data
- configured assignment, expected applicability and provider-observed state remain separate concepts;
- source, provider external IDs, observed time and last successful sync are preserved;
- derived applicability/explainability includes confidence/reason and explicit `unknown` rather than guessing;
- provider-specific status/details remain available where normalization would lose troubleshooting value;
- high-volume sync does not emit event/audit noise for unchanged observations;
- Group/User/Device counts used in management views drill down to the underlying evaluated set.

## Events/async
- meaningful state transitions emit stable domain events where needed;
- domain mutation + outbox event are atomic when delivery matters;
- background jobs are restart-safe, idempotent and observable;
- external outages do not unnecessarily block core UI transactions.

## Product integration
Explicitly decide impact on:
- My Work / Tasks
- Notifications
- Search / Saved Views
- Relationships
- Timeline/history
- Reporting
- IT Briefing / Knowledge where relevant

## Quality
- unit/integration/contract/E2E tests at the right level;
- fixed defects get regression coverage where practical;
- errors and edge cases handled at system boundaries;
- list APIs bounded/paginated;
- no obvious N+1/unbounded-query regression.

## UX/i18n
- employee UX does not expose unnecessary ITSM complexity;
- known context is prefilled/correctable;
- all user strings localized (English fallback + German maintained);
- keyboard/accessibility considered.

## Documentation
- authoritative docs updated in the same change;
- generated permissions/events/config references regenerated;
- docs describe implemented reality, not aspirational behavior, unless explicitly marked planned;
- no broken relative links.

## Final validation

Run:

```bash
make check
git diff --check
git status --short
```

Review the final diff for unrelated changes, secrets, temporary files, debug code and accidental dependency churn.

Final implementation summary must state implementation, migrations, permissions, events, dependencies, docs/tests and known limitations (including explicit "none" where useful).
