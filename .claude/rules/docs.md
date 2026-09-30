---
paths:
  - "docs/**/*.md"
  - "CLAUDE.md"
  - "README.md"
---
# Documentation rules

- Technical documentation is English.
- Update the authoritative document instead of copying facts into multiple places.
- ADRs record decisions and rationale; current docs describe the current architecture.
- A documentation change must not claim implementation exists when it is only planned; mark status clearly.
- Relative links must remain valid and pass `make docs-check`.
