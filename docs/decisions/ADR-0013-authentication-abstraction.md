# ADR-0013: Authentication Provider Abstraction

- Status: Accepted

## Decision

The platform User is independent from authentication providers. Initial environments may use LDAP/AD and Kerberos/SPNEGO; hosted environments may use OIDC/Entra. External identities map provider subjects to the canonical User.

## Consequences

Authentication methods can change without replacing ticket, task, asset or audit ownership identities.
