---
paths:
  - "agents/**"
  - "backend/internal/platform/auth*"
  - "backend/internal/platform/authorization/**"
  - "backend/internal/platform/secrets/**"
  - "backend/internal/modules/endpoint/**"
---
# Security-sensitive code rules

- Default deny. Validate authorization server-side.
- Never log credentials, tokens, authentication headers or decrypted secrets.
- Connector Agent and Endpoint Agent are distinct trust boundaries.
- Agent commands are typed, capability-scoped, expiring and idempotent.
- Arbitrary remote shell execution is not an implicit capability.
- High-impact actions require explicit audit context and, where policy requires, approval/maintenance windows.
