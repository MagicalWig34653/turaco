---
name: review-security
description: Perform a security-focused Turaco review, especially auth, integrations, agents, files, secrets and endpoint operations.
model: claude-opus-5-5
effort: high
---
# Security Review

Check authorization, tenant isolation, IDOR, injection, secret/log exposure, file handling, SSRF, replay/idempotency, agent capability escalation, command expiry, encryption boundaries and auditability. Distinguish exploitable findings from hardening suggestions. Reference `docs/security/`.
