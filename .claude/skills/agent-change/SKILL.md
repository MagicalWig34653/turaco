---
name: agent-change
description: Design a Connector/Endpoint Agent capability or protocol change before implementation.
argument-hint: <agent capability/change>
model: claude-opus-5-5
effort: high
---
# Agent Change

For `$ARGUMENTS`, define the trust/security model before code:

1. Connector vs Endpoint Agent ownership.
2. Typed capability and why it is needed.
3. Agent/server identities and tenant/device binding.
4. Request/command authentication and authorization.
5. Expiration, replay/idempotency and offline behavior.
6. Local capability restrictions and least privilege.
7. Audit context and observability.
8. Protocol/version compatibility.
9. Upgrade/failure/retry behavior.
10. Tests and threat review.

Never introduce arbitrary shell execution as an implementation shortcut.
