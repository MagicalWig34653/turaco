# ADR-0035: Microsoft Entra ID Sign-in Through OpenID Connect for Every Deployment Variant

- Status: Proposed (2026-10-10). Nothing in this ADR is implemented; [current status](../product/current-status.md) is authoritative. Design and threat model: [F15 Microsoft integration, Entra login](../product/f15-microsoft-integration-design.md#entra-login).

## Context

[ADR-0013](ADR-0013-authentication-abstraction.md) separated the platform User from authentication providers and expected OIDC/Entra only for hosted environments. Implemented today: LDAP/AD password login, Kerberos/SPNEGO, the break-glass emergency account and local accounts ([ADR-0034](ADR-0034-local-accounts-and-external-parties.md)). Many customers, including hospitals that run Turaco on their own servers, sign their staff in to Microsoft 365 with Entra ID and want the same sign-in, Conditional Access and MFA for Turaco. The product owner decided on 2026-10-10 that Entra sign-in is supported for SaaS-hosted and on-prem/self-hosted installations alike and coexists with LDAP/AD, Kerberos and local accounts in any combination.

## Decision

1. **Protocol.** Turaco is an OpenID Connect relying party of Microsoft Entra ID using the authorization-code flow with PKCE (S256), `state`, `nonce`, `response_mode=query` and a confidential-client credential (certificate client assertion preferred, client secret allowed). The implicit and hybrid flows, `response_mode=form_post`, ROPC and device code are not used. One Entra configuration per installation.
2. **Tenant modes.** `single` (authority is the tenant id) or `multi_restricted` (authority `organizations`, the token's `tid` must be in an explicit allow-list). Consumer Microsoft accounts and the `common` authority are never accepted. The issuer must equal the v2.0 issuer of the token's own `tid`.
3. **Identity key.** An Entra identity is an External Identity with provider key `entra:<tid>` and subject `oid`. Email, UPN, `preferred_username` and `sub` are never used to find or link a User. Linking happens only by (a) an existing link, (b) the hybrid source-anchor match against a User synced from the on-prem directory that the tenant is explicitly bound to, or (c) an administrator operation. Automatic provisioning is off by default (`link_only`); when enabled it creates only internal employees without roles and refuses on any email collision.
4. **No token-claim authorization.** `groups` and `roles` claims are not used for authorization (group overage, staleness, size). Group-based roles keep working through Directory Groups observed by directory synchronization (LDAP for hybrid; a Graph group sync for cloud-only tenants, security groups only). High-risk permissions are never granted through an Entra-observed group.
5. **MFA source.** Entra Conditional Access is the MFA source for Entra sessions. A session records its assurance; high-risk permissions are effective in an Entra session only when the ID token proves MFA (Conditional Access authentication context in `acrs`, or `amr` containing `mfa`). Step-up re-authenticates at Entra and issues a new session. ADR-0034 R10 stays unchanged for local accounts.
6. **Sessions and tokens.** Entra sign-in creates an ordinary Turaco server-side session (`auth_method = entra`) with a configurable absolute cap. Refresh tokens and access tokens are not stored; a delegated `User.Read` token may be used once in memory during the callback for the source-anchor lookup. No front-channel or back-channel logout (Entra offers no OIDC back-channel logout; front-channel needs third-party cookies). Logout revokes the Turaco session and may redirect to the Entra sign-out endpoint.
7. **Deprovisioning.** Disabled or deleted Entra users lose Turaco access through directory synchronization (hybrid), a Graph reconciliation job (cloud-only, outbound only) and the Entra session cap; SCIM inbound provisioning is deferred.
8. **On-prem.** The same code path for every variant. Requirements: outbound HTTPS from `turaco-api` (and `turaco-worker` for reconciliation) to the Entra endpoints of the configured cloud, optional explicit HTTP proxy, a fixed configured redirect URL (never derived from request headers), credentials as deployment secret files with expiry monitoring. Air-gapped installations cannot use Entra sign-in. Workload identity federation is not offered on-prem.
9. **Dependencies.** JWT/JWS validation is not hand-written. The implementation slice adds `github.com/coreos/go-oidc/v3` (Apache-2.0) with its `github.com/go-jose/go-jose/v4` and `golang.org/x/oauth2` (BSD-3-Clause), after the usual supply-chain review; this ADR is the required dependency decision. Microsoft's MSAL for Go is not used (larger surface, token cache we do not want).

## Alternatives considered

- **SAML 2.0 against Entra:** equally supported by Entra, but XML signature validation has a poor security history and SAML gives nothing OIDC lacks here. Rejected.
- **Email or UPN as the linking key:** convenient, but mutable and reassignable, the classic account-takeover path. Rejected (same rule as directory sync).
- **Authorization from `groups`/`roles` claims at login:** no sync needed, but overage above 200 groups, stale until re-login and a snapshot per session. Rejected; Directory Groups remain the single group model.
- **Entra only for SaaS (ADR-0013 text):** rejected by the product owner; on-prem customers use Entra for Microsoft 365 already.
- **Store refresh tokens to call Graph as the user later:** widens the impact of a database breach; no planned feature needs it. Rejected; a feature that needs delegated Graph access asks for incremental consent at use time and discards the token.

## Consequences

- ADR-0013's "hosted environments may use OIDC/Entra" becomes "every deployment variant" (follow-up note added there).
- `platform/authentication` gains an OIDC login port and a short-lived login-transaction table; `integrations/entra` holds the Entra adapter, a Fake IdP for tests and `NotConfigured`; organization gains admin link/unlink operations for Entra identities; sessions gain an assurance attribute.
- The evaluator gains one rule (high-risk permissions need MFA assurance in Entra sessions); `review-security` reviews login, linking and the evaluator change before merge.
- New health checks and deployment configuration keys; operators must monitor credential expiry and egress.
- Open product decisions are listed in the [F15 design](../product/f15-microsoft-integration-design.md#open-questions-for-the-product-owner).
