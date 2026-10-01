# ADR-0023: gokrb5 for Kerberos/SPNEGO Authentication

- Status: Accepted (2026-10-01, with the F1 completion design, decision E2)

## Context

F1 slice 4 provides transparent Windows sign-in for on-prem domains: browsers send a SPNEGO/Negotiate token that `turaco-api` must validate against the service's keytab (see [identity and access design](../security/identity-access-design.md) §8). The Go standard library has no Kerberos support. The alternative, a reverse proxy that authenticates and forwards the user in a trusted header, was rejected because header trust is a common misconfiguration and moves an authentication decision outside Turaco's audit.

## Decision

Use `github.com/jcmturner/gokrb5/v8` (pure Go, Apache-2.0, already in the module graph through go-ldap) for SPNEGO token validation with a keytab:

- used only in `backend/internal/integrations/kerberos`, which returns the authenticated principal name; no gokrb5 types leave that package;
- the keytab is a deployment secret file (`KERBEROS_KEYTAB_FILE`) mounted only into `turaco-api`;
- accepted realm and service principal are explicit configuration; the principal is mapped to a synced directory account like a password login, so Kerberos never creates Users.

## Consequences

- One more direct dependency subject to `docs/security/supply-chain.md`.
- gokrb5's replay cache and clock-skew checks apply per API instance. Its cache checks and records an authenticator in two unlocked steps, so Turaco serializes verification per process; replays across instances within the skew window remain possible and are bounded by the short token lifetime and TLS.
- gokrb5 selects the decryption key by the ticket's unencrypted realm and checks only client names, not realms, between ticket and authenticator. Turaco therefore loads only AES keys of the configured principal and realm and takes the identity from the KDC-encrypted ticket part, checking all realms itself (regression tests in `integrations/kerberos`).
- End-to-end behaviour was verified against an MIT KDC in the development environment; that run is manual and not yet part of CI.
