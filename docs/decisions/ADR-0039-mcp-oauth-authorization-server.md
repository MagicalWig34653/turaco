# ADR-0039: Turaco as OAuth Authorization Server for the Turaco MCP Resource Only

- Status: Accepted (2026-10-12). Implemented with F12 slice A-D; see [current status](../product/current-status.md) and the [A-D design](../product/f12-turaco-ai-design.md#a-d-mcp-server-design).
- Related: [ADR-0029](ADR-0029-turaco-ai.md) decision 7 (MCP server with per-User OAuth, audience-bound tokens, no static keys, no passthrough), [ADR-0035](ADR-0035-entra-oidc-login.md) (sign-in; Turaco is an OIDC *client* there), [ADR-0007](ADR-0007-isolated-customer-data-planes.md).

## Context

ADR-0029 requires a read-only Turaco MCP server with per-User OAuth and audience-bound tokens. The MCP authorization specification expects the MCP server to be an OAuth 2.1 protected resource that names an authorization server through Protected Resource Metadata (RFC 9728); clients then use the authorization code flow with PKCE and resource indicators (RFC 8707).

Turaco has no general token service: browser sessions are cookies (`platform/authentication`), and identity comes from LDAP, Kerberos, Entra ID or local accounts. Delegating MCP authorization to an external identity provider (for example Entra ID) would require per-installation app registrations, would not work for LDAP/Kerberos/local installations, and would put the audience check and the client allowlist outside Turaco's audit. A general-purpose authorization server product (Keycloak and similar) would be a new major infrastructure dependency for one endpoint.

## Decision

1. Turaco acts as an OAuth 2.x **authorization server for exactly one protected resource**: the Turaco MCP endpoint (`<EMAIL_BASE_URL>/api/v1/mcp`). It is not a general identity provider: no OpenID Connect, no ID tokens, no user-info endpoint, no other audiences, no client credentials or password grants. The REST API keeps cookie sessions; bearer tokens are accepted by the MCP endpoint only.
2. The implementation lives in `platform/ai` (it is the AI platform's transport, ADR-0029 decision 7) and uses the standard library only; no OAuth or MCP library is added.
3. **Clients** are an administrator-managed allowlist (`ai.settings.manage`): public clients only (no client secrets, so no static credentials exist), fixed redirect URIs compared by exact string, https or http on literal `127.0.0.1` or `[::1]` with a fixed port only. `localhost` is refused because its resolution is mutable, including for previously registered clients until an administrator replaces that URI. Dynamic client registration is not offered.
4. **Grant**: authorization code with PKCE `S256` only; the `resource` parameter is required and must equal the MCP resource, which becomes the token audience. The User approves on a consent screen in the Turaco web UI under the existing session login; consent is not remembered.
5. **Tokens** are opaque 256-bit random strings stored only as SHA-256 hashes, bound to client, User, tenant and audience. Access tokens live 10 minutes; refresh tokens rotate on every use, and a replayed refresh token or authorization code revokes the whole grant. A grant ends after at most 24 hours (the default web session limit), on revocation (RFC 7009 endpoint, the User's own grant list, administrator bulk revocation, client disable), or on credential replacement and account deactivation. Session and MCP revocation run in the same transaction, so reactivation cannot revive prior grants. The administrator operation requires `ai.settings.manage` and is audited.
6. Every MCP call is authorized on the server by the same `Caller` construction as the web assistant (User and tenant from the grant, permissions reloaded by the role evaluator), and tokens are never forwarded to other services.

## Consequences

- Turaco owns a small, security-critical OAuth surface; changes to it need a security review. Its threat model is in the [A-D design](../product/f12-turaco-ai-design.md#a-d-mcp-server-design).
- MCP clients that require dynamic client registration, `plain` PKCE, client secrets or loopback redirects with a variable port (RFC 8252 section 7.3) cannot connect until an administrator registers them with a fixed redirect URI.
- A second protected resource, OpenID Connect or a token for the REST API would need a new ADR.
