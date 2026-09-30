# Security Policy

## Reporting

Do not open public issues containing exploitable vulnerability details, credentials or customer data. Until a dedicated security contact is configured, use the repository owner's private security-reporting channel.

## Design baseline

The architecture follows least privilege, explicit authorization, immutable audit trails, tenant isolation, encrypted transport, application-level protection for secrets, encrypted attachments, supply-chain traceability and separated Connector/Endpoint Agent trust boundaries.

Read:

- `docs/security/security-architecture.md`
- `docs/security/encryption.md`
- `docs/security/agent-boundaries.md`

## Secret handling

Never commit `.env`, credentials, private keys, API tokens, LDAP passwords or customer exports. Use `.env.example` only for non-secret examples.
