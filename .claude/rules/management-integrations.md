---
paths:
  - "backend/internal/integrations/**"
  - "backend/internal/modules/endpoint/**"
  - "backend/internal/modules/software/**"
  - "docs/integrations/intune*.md"
---
# Management integration rules

- External provider DTOs stay at the integration boundary; canonical Turaco management concepts are provider-agnostic.
- Preserve provider external IDs, source, observed time and last successful sync.
- Never collapse configured Assignment, Turaco-derived Expected Applicability and provider Observed result into one field/state.
- Applicability evaluation must be explainable with deterministic reason codes, confidence/freshness and explicit `unknown` when semantics cannot be reproduced safely.
- Include/exclude targeting, User-vs-Device source, All Users/All Devices and assignment filters remain visible; do not flatten them away.
- Provider-reported error/conflict and Turaco-derived findings must be distinguishable in data and UI.
- Interactive views read normalized local state by default; avoid live provider calls in page rendering.
- Preserve provider-specific detail when normalizing it away would harm troubleshooting.
