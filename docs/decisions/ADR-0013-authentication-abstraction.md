# ADR-0013: Authentication Provider Abstraction

- Status: Accepted

## Decision

The platform User is independent from authentication providers. Initial environments may use LDAP/AD and Kerberos/SPNEGO; hosted environments may use OIDC/Entra. External identities map provider subjects to the canonical User.

## Consequences

Authentication methods can change without replacing ticket, task, asset or audit ownership identities.

## Follow-up note (2026-10-10)

The product owner decided that OIDC sign-in with Microsoft Entra ID is supported in every deployment variant, SaaS-hosted and on-prem/self-hosted, not only in hosted environments, coexisting with LDAP/AD, Kerberos and local accounts. The decision text above is kept as history; see [ADR-0035](ADR-0035-entra-oidc-login.md) (Proposed, not implemented).
