# ADR-0022: go-ldap as LDAP Client Library

- Status: Accepted (2026-10-01, with the F1 slice 3 directory sync design)

## Context

F1 slice 3 synchronizes Users and Directory Groups from Active Directory/LDAP (see [design](../integrations/ldap-ad-sync-design.md)); slice 3b/4 will add LDAP bind authentication. The Go standard library has no LDAP client. Implementing LDAP/BER, paging (RFC 2696), StartTLS and AD range retrieval ourselves would be security-relevant protocol code with no product value.

## Decision

Use `github.com/go-ldap/ldap/v3` for all LDAP protocol access.

- Pure Go (no cgo), MIT licensed, maintained, widely used (Grafana, Gitea, HashiCorp Vault, Dex).
- Supports LDAPS, StartTLS with a caller-provided `tls.Config`, simple bind, paged search and filter escaping.
- Used only inside `backend/internal/integrations/ldap`; its types never cross into module contracts (`organization/public.DirectorySnapshot`).

## Consequences

- One new direct dependency (plus its small transitive dependencies) is subject to the supply-chain controls in `docs/security/supply-chain.md`.
- TLS verification policy stays in Turaco code: the library is always given a `tls.Config` with verification enabled.
- Replacing the library later affects only the integration package.
- Transitive modules pulled in: `github.com/go-asn1-ber/asn1-ber`, `github.com/Azure/go-ntlmssp`, `github.com/google/uuid`, `golang.org/x/crypto` (≥ v0.55.0 for CVE-2026-56854; NTLM support compiled in but unused, Turaco uses simple bind only), and newer `golang.org/x/sync`/`golang.org/x/text`.
- Exception to the `%w` error-wrapping rule: go-ldap error text includes server diagnostic messages that can contain DNs and attribute values. The adapter reports operation and LDAP result code only and does not wrap `*ldap.Error`; context cancellation/deadline errors are still wrapped.
