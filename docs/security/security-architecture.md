# Security Architecture

**Status:** Baseline design requirements

## Trust boundaries

1. Browser ↔ Platform API over HTTPS.
2. Platform ↔ PostgreSQL/Object Storage over protected internal transport.
3. Hosted Platform ↔ Connector Agent using outbound authenticated encrypted channel (mTLS preferred).
4. Hosted Platform ↔ Endpoint Agent using a separate authenticated management protocol.
5. Tenant A data/security material is isolated from Tenant B.

## Authorization

Backend authorization uses Permission + Scope. Frontend hiding is never authorization. Implementation status: the `authorization` platform package enforces Permission checks per route and is default-deny when no valid session exists (`DenyAll` remains the fallback); Scope evaluation is not implemented yet, so current permissions are global. Listing Directory Group members requires both `organization.directory.view` and `organization.view` because members are user identities. High-impact actions (wipe, broad deployment, remote support, privileged config) require dedicated permissions and policy gates.

## Sessions

Browser sessions are server-side. The cookie carries a 256-bit random opaque token; only its SHA-256 hash is stored, so database access alone does not yield usable sessions. The cookie is HttpOnly, SameSite=Lax and Secure unless explicitly disabled for local HTTP development (`SESSION_COOKIE_SECURE`); secure deployments use the `__Host-` cookie name prefix and requests carrying several session cookies are treated as unauthenticated (cookie tossing). Sessions expire after an idle and an absolute timeout, are always issued with a fresh token (no fixation), and are revoked explicitly on logout; creation and revocation are audited without token material. Every unsafe request (anything except GET/HEAD/OPTIONS) passes a same-origin guard on the whole API server (`Sec-Fetch-Site`/`Origin` host check) and fails closed when neither header is present; the origin check compares hosts only, not scheme. Request/correlation IDs are generated server-side unless a short, plain client value is supplied. Directory sync revokes (and audits) all sessions of a user in the same transaction that ends their `active` status, so reactivation never revives an old session; other future operations that end `active` must do the same through `authentication.RevokeUserSessions`. A session is only honoured while the Organization User is `active`. Session permissions are empty until F1 slice 5.

## Identity

Platform User is separate from External Identity. Initial AD/LDAP and transparent Windows auth can coexist with future OIDC/Entra without re-keying business history.

Directory synchronization matches accounts only by immutable directory IDs and never links a directory account to an existing User by email (account-takeover prevention). It binds with a read-only service account whose password is read from a deployment secret file, requires LDAPS/StartTLS with certificate verification, requests an explicit attribute allowlist, resolves DN references exactly (ambiguous references stay unresolved), bounds resources a hostile directory could exhaust, degrades malformed entries individually instead of stopping deactivations, and withholds the "not observed" sweep when an unexpectedly large share of users or groups is missing. See [LDAP/AD integration](../integrations/ldap-ad.md).

## Agents

Agents use unique cryptographic identities and explicit local/cloud capabilities. The ordinary protocol is typed operations, not arbitrary shell. Commands are expiring, idempotent and audited.

## Data protection

- TLS for network transport.
- application-level encryption for stored secrets.
- application encryption of attachments before object storage.
- disk/provider encryption is defense in depth, not the only control.
- passwords are not reversibly stored; local emergency credentials use modern password hashing.

## Audit/logging

Audit records are separate from debug logs and protected against normal mutation. Logs use IDs/structured context and redact secrets/auth headers/content not needed for operation.

## Supply chain

GitHub Actions builds from reviewed commits, dependencies are monitored, images are published to GHCR with provenance/SBOM where supported, and production deployments record immutable image digests.
