# ADR-0018: Claude Code Model Routing

**Status:** Accepted

## Decision

Use Claude Opus 5.5 for architecture, domain design, security, database/concurrency judgment and final critical review. Use Claude Sonnet 5.5 for bounded implementation, frontend work, integrations, tests, documentation and repository exploration.

Project subagents and skills pin these model IDs where appropriate rather than inheriting the lead model accidentally.

## Rationale

The split protects architectural coherence while allowing routine implementation to run faster and at lower cost. Independent reviewer contexts reduce anchoring and confirmation bias.

## Constraints

- Sonnet implementers must not redefine architecture without a reviewed design/ADR.
- Opus should not be used merely because it is available when work is routine and bounded.
- Model IDs may be upgraded intentionally as a repository-wide maintenance change.
