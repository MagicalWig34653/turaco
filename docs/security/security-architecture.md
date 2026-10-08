# Security Architecture

**Status:** Baseline design requirements

## Trust boundaries

1. Browser ↔ Platform API over HTTPS.
2. Platform ↔ PostgreSQL/Object Storage over protected internal transport.
3. Hosted Platform ↔ Connector Agent using outbound authenticated encrypted channel (mTLS preferred).
4. Hosted Platform ↔ Endpoint Agent using a separate authenticated management protocol.
5. Tenant A data/security material is isolated from Tenant B.

Planned boundaries (not implemented):

6. Remote Access Provider client and relay on managed devices ([ADR-0026](../decisions/ADR-0026-remote-access-providers.md)).
7. Software Management Provider (IntuneGet) and Turaco's separate Intune write credential, which together can run software on every managed device ([ADR-0027](../decisions/ADR-0027-software-management-providers.md)).
8. AI Providers receiving data selected by AI Tools; data sent to an external provider leaves the customer's data plane ([ADR-0029](../decisions/ADR-0029-turaco-ai.md)). The read-only backend (F12 A-A) is implemented; see the AI threat addendum below.
9. Presence sources (Microsoft 365, HR) supplying personal availability data ([ADR-0028](../decisions/ADR-0028-workforce-presence.md)).

## Authorization

Backend authorization uses Permission + Scope. Frontend hiding is never authorization. Implementation status: the `authorization` platform package enforces Permission checks per route and is default-deny when no valid session exists (`DenyAll` remains the fallback). Permissions come from roles assigned to the User directly or to its Directory Groups (including nested groups), evaluated on every request so revocations and membership changes apply immediately; only `global` scope exists so far. `platform.roles.manage` is administrator-equivalent. Whoever controls membership of a Directory Group with a role assignment controls that role in Turaco. The built-in `platform-administrator` role is immutable, and revoking the last User assignment of it is refused through the API (the CLI can recover access). See [identity and access design](identity-access-design.md). Listing Directory Group members requires both `organization.directory.view` and `organization.view` because members are user identities. High-impact actions (wipe, broad deployment and ring promotion beyond pilot, remote access, enabling Intune write access, AI Provider configuration, access to other Users' presence details, provider credentials, privileged config) require dedicated permissions and policy gates. Turaco AI never executes high-impact actions; it acts only through typed AI Tools as the requesting User ([ADR-0029](../decisions/ADR-0029-turaco-ai.md)).

## Sessions

Browser sessions are server-side. The cookie carries a 256-bit random opaque token; only its SHA-256 hash is stored, so database access alone does not yield usable sessions. The cookie is HttpOnly, SameSite=Lax and Secure unless explicitly disabled for local HTTP development (`SESSION_COOKIE_SECURE`); secure deployments use the `__Host-` cookie name prefix and requests carrying several session cookies are treated as unauthenticated (cookie tossing). Sessions expire after an idle and an absolute timeout, are always issued with a fresh token (no fixation), and are revoked explicitly on logout; creation and revocation are audited without token material. Every unsafe request (anything except GET/HEAD/OPTIONS) passes a same-origin guard on the whole API server (`Sec-Fetch-Site`/`Origin` host check) and fails closed when neither header is present; the origin check compares hosts only, not scheme. Request/correlation IDs are always generated server-side (they become audit correlation IDs); a client-supplied `X-Request-ID` is only logged. Directory sync revokes (and audits) all sessions of a user in the same transaction that ends their `active` status, so reactivation never revives an old session; other future operations that end `active` must do the same through `authentication.RevokeUserSessions`. A session is only honoured while the Organization User is `active`; session creation at login locks the User row so it serializes with status changes.

## Login

Password login binds to the directory as the synced account's DN over LDAPS/StartTLS; `turaco-api` holds no directory service account. Empty passwords are rejected before any directory call (an empty simple bind would be an unauthenticated bind). Unknown, disabled, inactive and wrong-password attempts answer the same 401 after at least 400 ms; failures are audited without the password. Failed attempts are counted per identifier and per client address in PostgreSQL and lock for 15 minutes after 5 (identifier) or 30 (client) failures within 15 minutes, before the directory is contacted, so attackers cannot use Turaco to trigger AD account lockouts. The client address only honours `X-Forwarded-For` from `HTTP_TRUSTED_PROXIES`. Kerberos/SPNEGO login validates tickets with a keytab mounted only into `turaco-api`, accepts only the configured service principal and realm, maps principals to synced accounts and rejects cross-site requests to its session-creating GET endpoint. The local emergency account (argon2id hash, disabled by default, managed only by `turaco-admin`, one-hour sessions, audited and logged at error level) keeps administrators in when the directory is unavailable.

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
- passwords are not reversibly stored; the local emergency credential uses argon2id (64 MiB, t=3, p=2).

## Turaco AI (threat addendum, F12 A-A)
- **Prompt injection.** Record text is untrusted data. Controls are structural, not the prompt: the tool set is fixed per conversation from the User's permissions and the provider's allowed data classes; arguments are validated against a closed schema; every call re-checks the permission and runs the module's own authorization as the requesting User; a call may only target records the User named or consented to (tool output never extends the scope); results are field-allowlisted, size-capped and wrapped in an `<untrusted_data>` block whose closing tag cannot be forged out of the data; there is no write, URL-fetch, SQL or file tool. Remaining risk: a model can still be misled within the records it may read.
- **Forged or replayed context.** The API accepts only plain user text and an opaque conversation id; roles, tool results and earlier messages cannot be supplied. The id (256-bit, stored hashed) is valid only for the same User, tenant and sign-in session and expires after 30 minutes idle or at logout.
- **Egress.** Deny by default per provider and data class; eligibility derives from the output fields a tool declares, and the runtime refuses fields it did not declare. Secrets, Audit records, Presence details and Remote Access data are not sendable classes (prohibited-name scan at registration). Prompt, answer and tool content never enter audit or logs.
- **SSRF through the provider URL.** Administrator-only setting, validated on save, and enforced again at dial time on every resolved IP (DNS rebinding cannot change the destination), no redirects, no proxy, host pinned to the provider, size and time caps; external providers cannot reach loopback, private, link-local or metadata addresses, local providers can reach loopback and private ranges but never link-local or metadata addresses.
- **Cost and abuse.** Atomic worst-case token and request reservations in PostgreSQL per User and installation; one turn at a time per conversation; bounded tool iterations and malformed-call retries.

## Audit/logging

Audit records are separate from debug logs and protected against normal mutation. Logs use IDs/structured context and redact secrets/auth headers/content not needed for operation.

## Supply chain

GitHub Actions builds from reviewed commits, dependencies are monitored, images are published to GHCR with provenance/SBOM where supported, and production deployments record immutable image digests.
