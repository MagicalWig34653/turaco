---
name: integration-change
description: Implement or extend a Turaco external-system adapter after domain ownership and source-of-truth rules are clear.
argument-hint: <integration change>
model: claude-sonnet-5-5
effort: high
---
# Integration Change

Implement `$ARGUMENTS` as an adapter around Turaco's domain model.

- Keep vendor types/SDKs at the integration boundary.
- Preserve external IDs, source, observed time and last successful sync.
- Respect field source-of-truth/conflict rules.
- Make retryable processing idempotent.
- Keep slow external calls out of interactive writes unless required.
- Provide fakes/contract tests where practical.
- Surface sync health/failures instead of silently dropping work.
For management-provider integrations (Intune first):

- keep configured Assignment, derived Expected Applicability and provider Observed state separate;
- preserve include/exclude, target type, intent, filters, provider IDs and freshness;
- create explainable reason codes/paths for applicability decisions;
- return `unknown` rather than emulating undocumented provider behavior;
- distinguish provider-reported problems from Turaco-derived findings;
- ensure reverse lookup and count drill-down can be supported without live provider calls.
