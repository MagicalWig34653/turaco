# F15 Microsoft Integration — Feature Design

**Status:** Draft 2026-10-10, planned, nothing implemented. Decisions: [ADR-0035](../decisions/ADR-0035-entra-oidc-login.md) (Entra sign-in, Proposed) and [ADR-0036](../decisions/ADR-0036-microsoft-teams-integration.md) (Teams, Proposed). [Current status](current-status.md) is authoritative for what exists. Microsoft API details (claim names, permission names, limits) are stated as understood at design time and must be verified against current Microsoft documentation in each slice's spike; a lab tenant is required for real verification ([integration sandboxes](../development/integration-sandboxes.md)). Related: [ADR-0013](../decisions/ADR-0013-authentication-abstraction.md), [ADR-0014](../decisions/ADR-0014-application-level-secret-and-file-encryption.md), [ADR-0024](../decisions/ADR-0024-outbox-dispatch-and-notifications.md), [ADR-0028](../decisions/ADR-0028-workforce-presence.md), [ADR-0032](../decisions/ADR-0032-module-switches.md), [ADR-0034](../decisions/ADR-0034-local-accounts-and-external-parties.md), [identity and access design](../security/identity-access-design.md), [security architecture](../security/security-architecture.md), [F14 design](f14-administration-design.md), [Intune](../integrations/intune.md), [Teams and email](../integrations/teams-email.md).

## Product decisions (2026-10-10)

1. Microsoft Entra ID sign-in (OpenID Connect) is supported in **every deployment variant**: SaaS-hosted and on-prem/self-hosted. It coexists with LDAP/AD, Kerberos, local accounts and the emergency account; an installation enables any combination.
2. A Microsoft Teams integration is added.

## Scope

In scope: Entra sign-in, linking Entra identities to Users, Entra-based deprovisioning, MFA assurance from Entra, the Teams channel (phases T-A to T-D), the shared Microsoft HTTP client and credential handling, health and setup entries. Out of scope: SAML, SCIM inbound provisioning (deferred), Entra B2C/External ID for customers, Teams chat presence, a Teams tab that embeds Turaco, chat bots that answer questions (Turaco AI stays in Turaco), meeting or calendar features in Turaco.

## Reused concepts

User, External Identity (`organization.external_identities`, unique `(provider_key, external_subject)`), `account_kind`, origin and directory-owned attributes, Directory Group and role assignment to groups, sessions (`platform/authentication`), login throttle, the dominance rule and the high-risk rules of [F14](f14-administration-design.md#review-outcomes-opus-security-review-2026-10-09), Notification, Notification Delivery and Notification Preference, outbox and jobs, the notification category registry, external references and the inbound replay store (`platform/externalrefs`), Approvals' public contract, module switches, `platform/health` checks and the setup checklist, audit, the provider-port pattern (port + `Fake` + `NotConfigured` + `Mode()`), the egress guard of `platform/ai/safehttp`.

## New concepts

- **OIDC Login Transaction** (platform-owned, technical): a short-lived, single-use record of one sign-in attempt (state, nonce, PKCE verifier, browser binding). Not a domain noun.
- **Session assurance**: an attribute of a session (`password|kerberos|mfa`), not a new entity.
- **Teams Channel Destination**: a named, administrator-configured Teams channel endpoint that channel posts are sent to. New glossary term when T-A is implemented.
- **Teams Card Action Token**: an opaque single-use token behind an approve/reject button (T-C). Technical, not a domain noun.

No new domain module: Entra sign-in is part of `platform/authentication` and Organization; Teams is a channel of `platform/notifications` with an adapter in `integrations/teams`.

## Entra login

Planned, not implemented ([ADR-0035](../decisions/ADR-0035-entra-oidc-login.md)).

### Flow

1. `GET /api/v1/auth/entra/start?returnTo=<relative path>`: no session needed. Turaco creates an OIDC Login Transaction: `state` (256 bit), `nonce` (256 bit), PKCE `code_verifier` (256 bit, challenge S256), a browser-binding value set as the cookie `__Host-turaco_oidc` (HttpOnly, Secure, SameSite=Lax, Path=/, 10 minutes). Only SHA-256 hashes of `state` and the binding are stored; the verifier and nonce are stored for 10 minutes (no long-lived secret). `returnTo` must be a relative in-app path (open-redirect guard). Redirect to the authorization endpoint with `response_type=code`, `response_mode=query`, `scope=openid profile` (plus `User.Read` only when hybrid linking needs the source-anchor lookup), `prompt=select_account`, `client_id`, the configured redirect URL, `state`, `nonce`, `code_challenge`.
2. `GET /api/v1/auth/entra/callback?code&state` (top-level GET navigation, so the Lax cookie is sent; `form_post` would be a cross-site POST that the same-origin guard and SameSite=Lax correctly refuse): look up the transaction by state hash, require the binding cookie to match, consume it (single use, also on failure), reject when older than 10 minutes. An `error` parameter from Entra ends the attempt with the uniform failure.
3. Exchange the code at the token endpoint with `code_verifier` and the client credential (certificate client assertion or secret). Only the token endpoint of the configured cloud is called; no redirects followed.
4. Validate the ID token (below). Discard the access token unless the source-anchor lookup is needed, then call Graph `/me?$select=onPremisesImmutableId,onPremisesSyncEnabled,accountEnabled` once and discard it. Refresh tokens are never requested (`offline_access` is not in the scope) and never stored.
5. Resolve the User (identity linking below), create the session exactly like the other login paths (lock the User row `FOR SHARE`, revoke a session the request carried, fresh token), clear the binding cookie, redirect to `returnTo`.
6. Every failure answers with a redirect to the login page with a generic code (`entra_failed`, `entra_not_linked`, `entra_unavailable`) and is audited `auth.login.failed` with method `entra` and a reason; no claim values, codes or tokens are logged.

Throttling: the callback reserves a client attempt (`ip:entra/<client>`, own budget like Kerberos) before the token exchange. `GET /auth/methods` reports `entra: true` and the UI shows "Sign in with Microsoft"; when Kerberos is configured too, Kerberos is still tried first.

### ID token validation

- Signature: RS256 only (no `none`, no HMAC), key by `kid` from the JWKS of the discovery document of the configured cloud and tenant authority.
- JWKS caching and rotation: keys cached in memory per process for at most 24 hours; an unknown `kid` triggers one refresh, at most once per 5 minutes per process (a forged `kid` cannot be used to hammer Entra); a failed refresh keeps the cached keys and fails only tokens with unknown keys (`entra_unavailable`). Discovery and JWKS are fetched only from the allow-listed Entra host of the configured cloud, never from a URL inside a token.
- `iss` must equal `https://login.microsoftonline.com/<tid>/v2.0` (or the sovereign-cloud equivalent) for the token's own `tid`; `tid` must be the configured tenant (`single`) or in `ENTRA_ALLOWED_TENANT_IDS` (`multi_restricted`); the Microsoft consumer tenant is always refused.
- `aud` equals the client id; `azp`, when present, equals the client id.
- `exp`, `nbf`, `iat` with clock skew `ENTRA_MAX_CLOCK_SKEW` (default 2 minutes, at most 5); `iat` not older than 10 minutes.
- `nonce` equals the transaction's nonce.
- `oid` and `tid` present and valid UUIDs; `ver` is `2.0`.
- ID token versus userinfo: the validated ID token is the only identity source. The userinfo endpoint is not called (on Entra it is a Graph-backed endpoint with fewer claims and adds nothing). `name`, `preferred_username` and `email` are display or profile hints only.
- Groups and roles: the app registration emits no `groups` claim (`groupMembershipClaims` none); `groups`, `hasgroups`, `_claim_names` and `roles` are ignored. Reason: above 200 groups the token carries an overage marker instead of groups and Graph must be called; claims are a snapshot until the next sign-in. Group-based authorization uses Directory Groups (below).

### Identity linking

The stable key is **`tid` + `oid`**: External Identity `provider_key = entra:<tid>`, `external_subject = <oid>`. `sub` is pairwise per application and not usable for Teams or Graph; email and UPN change and can be reassigned. Resolution order at sign-in:

1. **Existing link** `(entra:<tid>, oid)` that is enabled: that User. The User must be active; `account_kind` is never changed.
2. **Hybrid match** (only when `ENTRA_LINK_DIRECTORY_PROVIDER_KEY` binds this tenant to the synced on-prem directory, and only for tenant `ENTRA_TENANT_ID`): Graph `/me` returns `onPremisesSyncEnabled = true` and `onPremisesImmutableId`; Turaco decodes it (base64 of the source anchor, objectGUID or `mS-DS-ConsistencyGuid`, which equals objectGUID unless the customer changed the anchor) and looks up the External Identity of that directory provider whose `external_subject` is the same GUID. Exactly one active User: link atomically (insert the Entra identity, audit `organization.external_identity.linked` with `via: source_anchor`). Cloud-only Entra users, users of other tenants, or a sync-disabled attribute never match. This avoids a second User for a person who already exists through LDAP sync. Matching by `onprem_sid` would need the SID in directory sync, which Turaco does not read today; it is not used.
3. **Automatic provisioning** (only when the runtime setting `auth.entra_provisioning` is `auto_employee`): only members of `ENTRA_TENANT_ID` (guest or `acct = 1` users and other tenants never), only if no User has the same primary email (collision refuses with `entra_not_linked` and an attention entry for administrators; never a link by email), creates a User with `account_kind = employee`, origin `directory` (attributes owned by the provider `entra:<tid>`), no roles, no Team memberships, audited `organization.user.provisioned` with `via: entra`.
4. Otherwise `entra_not_linked` (default `link_only`).

**Administrator linking** (`POST /users/{id}/external-identities/entra`, `DELETE /users/{id}/external-identities/{identityId}`): platform administrator only (takeover-capable, as directory linking in ADR-0034), never on oneself, `expectedVersion`, target must be active, audited, revokes the target's sessions and sends a notice to the target's email. The administrator enters the object id and tenant (copied from the Entra portal); Turaco does not search Entra by name or email. Linking a local account deletes its local credential, open tokens and sessions atomically and is refused while the User holds roles (ADR-0034 R5 applies unchanged). The emergency account can never be linked.

**account_kind, dominance and externals.** Entra never changes `account_kind`. B2B guests in the home tenant and users of other allowed tenants can only sign in to a User an administrator created and linked as `external` (needs F14 A-G: sponsor, expiry, ceiling and allow-list apply unchanged); until A-G exists, guests are refused (`ENTRA_GUESTS=refuse`). Guest detection uses the `acct` optional claim and, as a second signal, an `iss`/`idp` mismatch. The dominance rule covers the new link and unlink operations.

**Directory-owned attributes.** For Users provisioned from Entra, name and email are refreshed from the ID token at sign-in and by reconciliation, with source `entra:<tid>` and freshness; they are read-only in Turaco like LDAP-owned fields. For hybrid Users, LDAP stays the attribute owner and Entra claims are ignored.

### Groups, Teams and roles

- Hybrid: AD groups are already Directory Groups through LDAP sync; nothing changes.
- Cloud-only: slice E-C adds a Graph Directory Group sync (provider `entra:<tid>`, security groups only, transitive membership through Graph, same interval history, mass-removal safeguard and freshness as LDAP sync). Microsoft 365 groups (owners and members can add members, including guests) are not synced.
- Roles are assigned to these Directory Groups with the existing role-assignment operations. **High-risk permissions are never granted through an Entra-observed group**: assigning a role that contains one to such a group is refused (409 `access.high_risk_group_not_allowed`); high-risk roles go to Users directly, with expiry.
- Turaco Teams (operational) are not mirrored from Entra groups; an optional "suggest members from a Directory Group" helper is a later idea.

### MFA and step-up (review of ADR-0034 R10)

- An Entra session records `assurance = mfa` when the ID token proves MFA: preferred is the Conditional Access authentication context (`ENTRA_STEP_UP_AUTH_CONTEXT`, for example `c1`, requested through the `claims` parameter and checked in `acrs`); otherwise `amr` containing `mfa` if Entra issues it in the ID token (to be verified in E-D). Anything else is `assurance = password`.
- In an Entra session without MFA assurance the evaluator excludes high-risk permissions (the same second-line mechanism R10 uses). The UI shows "Confirm with Microsoft" which starts a step-up sign-in (`prompt=login`, `max_age=0`, the authentication-context claim request); success issues a new session with `mfa` assurance, so a stolen low-assurance cookie never gains privileges.
- R10 is unchanged: local accounts never hold high-risk permissions. An Entra identity linked to a former local account removes the local credential, so that User is an Entra User and can hold high-risk permissions with MFA assurance. LDAP and Kerberos sessions keep the directory's assurance (unchanged from F14).
- Step-up assurance has a lifetime: `mfa` assurance older than `ENTRA_STEP_UP_MAX_AGE` (default 12 hours, bounded by the session cap) is treated as `password`.

### Sessions and logout

- The session is an ordinary `platform.sessions` row (`auth_method = entra`) with the normal idle timeout and an absolute cap `ENTRA_SESSION_MAX_AGE` (default 8 hours; one hospital shift) that forces a new Entra sign-in; Conditional Access policies (device compliance, location) then apply again.
- Logout revokes the Turaco session. With `ENTRA_SIGN_OUT_REDIRECT=true` the browser is then sent to the Entra end-session endpoint with `post_logout_redirect_uri`; recommended for shared workstations, where otherwise the next person could select the previous person's Entra session (mitigated in addition by `prompt=select_account`).
- Back-channel logout: not available from Entra. Front-channel logout: not implemented (iframe with third-party cookies; `SameSite=Lax` cookies are not sent). Revocation relies on reconciliation and the session cap.

### Deprovisioning: how fast a disabled Entra user loses access

| Path | New sign-ins | Existing Turaco sessions |
| --- | --- | --- |
| Disabled in Entra | refused by Entra immediately | until the next of: reconciliation run, session cap |
| Hybrid: disabled in AD | refused by Entra after Entra Connect sync | LDAP sync deactivates the User and revokes sessions (`LDAP_SYNC_INTERVAL`) |
| Graph reconciliation (E-C, `ENTRA_RECONCILE_INTERVAL`, default 15 minutes, delta query) | n/a | an Entra identity with `accountEnabled = false` or deleted is disabled; if the User has no other enabled identity or credential, the User is deactivated (`status_source` `entra`) and sessions are revoked in the same transaction; otherwise only `entra` sessions are revoked |
| No reconciliation configured | refused by Entra | up to `ENTRA_SESSION_MAX_AGE`; health shows attention |

SCIM provisioning from Entra is deferred: it needs an inbound endpoint reachable from Microsoft (on-prem: reverse publishing or the on-prem provisioning agent) and duplicates the reconciliation pull. Revisit when a customer requires push latency.

### On-prem installations

- **Egress:** `turaco-api` needs HTTPS to the Entra authority host of the configured cloud (`login.microsoftonline.com` in the global cloud) for discovery, JWKS and the token endpoint, and to `graph.microsoft.com` only for the hybrid source-anchor lookup; `turaco-worker` needs both for reconciliation. Browsers need Entra too (they always do for Microsoft 365).
- **Proxy:** an explicit `MICROSOFT_HTTP_PROXY` (HTTP CONNECT) for all Microsoft calls; environment proxy variables are not honoured implicitly. TLS is end-to-end to the Microsoft host; a TLS-inspecting proxy requires its CA in `MICROSOFT_CA_FILE` (an operator decision, documented as a weakening). The host allow-list is checked before CONNECT.
- **Air-gap:** not supported. Validation needs current JWKS and the token endpoint; installations without egress use LDAP/AD, Kerberos or local accounts. The UI does not offer Entra when it is not configured.
- **Redirect URI behind reverse proxies:** `ENTRA_REDIRECT_URL` is configured exactly as registered in Entra (https; `http://localhost` only in development) and is never derived from `Host`, `X-Forwarded-Host` or `X-Forwarded-Proto`. Startup refuses a non-https redirect URL outside development. The reverse proxy must forward `/api/v1/auth/entra/callback` unchanged.
- **Credentials:** certificate (private key + certificate files, `ENTRA_CLIENT_CERTIFICATE_FILE`, `ENTRA_CLIENT_PRIVATE_KEY_FILE`) preferred, client secret file (`ENTRA_CLIENT_SECRET_FILE`) allowed; expiry monitoring see [Shared](#credentials-storage-and-rotation-adr-0014). Workload identity federation and managed identities exist only in Azure-hosted or Kubernetes-with-issuer setups; they are a later SaaS option, not available on-prem.
- **SaaS-hosted:** same code; the SaaS operator may use a publisher-owned multi-tenant app registration (`multi_restricted` with exactly the customer's tenant id) with admin consent by the customer, or the customer's own single-tenant registration. Each installation remains its own data plane ([ADR-0007](../decisions/ADR-0007-isolated-customer-data-planes.md)); a token from tenant B is never accepted by tenant A's installation.

### Configuration keys (names only; planned)

`AUTH_ENTRA_LOGIN_ENABLED`, `ENTRA_CLOUD` (`global|usgov|china`), `ENTRA_TENANT_MODE` (`single|multi_restricted`), `ENTRA_TENANT_ID`, `ENTRA_ALLOWED_TENANT_IDS`, `ENTRA_CLIENT_ID`, `ENTRA_CLIENT_CERTIFICATE_FILE`, `ENTRA_CLIENT_PRIVATE_KEY_FILE`, `ENTRA_CLIENT_SECRET_FILE`, `ENTRA_CLIENT_SECRET_EXPIRES_AT`, `ENTRA_REDIRECT_URL`, `ENTRA_POST_LOGOUT_REDIRECT_URL`, `ENTRA_SIGN_OUT_REDIRECT`, `ENTRA_PROVISIONING` (superseded: provisioning is the runtime setting `auth.entra_provisioning`), `ENTRA_GUESTS` (`refuse|external`), `ENTRA_LINK_DIRECTORY_PROVIDER_KEY`, `ENTRA_MAX_CLOCK_SKEW`, `ENTRA_SESSION_MAX_AGE`, `ENTRA_STEP_UP_AUTH_CONTEXT`, `ENTRA_STEP_UP_MAX_AGE`, `ENTRA_RECONCILE`, `ENTRA_RECONCILE_INTERVAL`, `MICROSOFT_HTTP_PROXY`, `MICROSOFT_CA_FILE`. Graph read credentials: `MICROSOFT_GRAPH_TENANT_ID`, `MICROSOFT_GRAPH_CLIENT_ID`, `MICROSOFT_GRAPH_CLIENT_CERTIFICATE_FILE`, `MICROSOFT_GRAPH_CLIENT_PRIVATE_KEY_FILE`, `MICROSOFT_GRAPH_CLIENT_SECRET_FILE`. The Graph read keys, the separate `MICROSOFT_GRAPH_WRITE_*` keys of the Intune assignment writer and `INTUNE_GRAPH_BETA` are in the generated [configuration reference](../reference/configuration.md).

### Data and code (planned)

- Migration: `platform.oidc_login_transactions(state_hash pk, binding_hash, nonce, code_verifier, return_to, created_at, consumed_at)` with a 10-minute expiry and pruning; `platform.sessions.assurance` and `assurance_at`; `users.status_source` gains `entra`; no change to `external_identities` (the key fits).
- `platform/authentication`: port `OIDCProvider{AuthCodeURL, Exchange(ctx, code, verifier) (VerifiedIdentity, error)}`, handler, transaction store. `VerifiedIdentity{TenantID, ObjectID, Guest, Assurance, AssuranceAt, DisplayName, Email, SourceAnchor}`; claim parsing stays in the adapter.
- `integrations/entra`: the real adapter (go-oidc, oauth2), `Fake` (an in-process IdP that issues signed tokens with configurable claims, keys and rotation) and `NotConfigured`.
- `integrations/microsoft`: the shared HTTP client, credential loader and token acquisition (see [Shared](#shared-microsoft-foundation)).
- Organization: link/unlink operations, source-anchor lookup and provisioning through `organization/public`.

### Health and setup

- Checks `entra_login` (configured, mode, last successful sign-in age, failures in 24 hours by reason, JWKS last refresh), `entra_reconcile` (`disabled`, `not_configured`, last run, disabled identities found, stale), `microsoft_credentials` (each configured credential: kind, days to expiry; `failing` after expiry, attention 30 days before; secret expiry unknown when `*_SECRET_EXPIRES_AT` is unset). Shown on `/admin/health` and `/admin/integrations`; no values, only key names.
- Setup checklist: item 2 "Directory" counts as done when the directory is configured **or** Entra sign-in is configured and has at least one successful sign-in (no new item; the count stays ten). Item 1 additionally warns when no administrator can sign in with MFA assurance while Entra is the only sign-in method.

### Permissions and audit

- No new permission: configuration is deployment configuration; linking is platform-administrator only; group role assignment uses `platform.roles.manage`.
- Audit: `auth.session.created` (authMethod `entra`, assurance), `auth.login.failed` (method `entra`, reason `invalid_token|tenant_not_allowed|guest_refused|not_linked|email_conflict|user_inactive|state_mismatch|provider_unavailable`), `auth.entra.step_up_succeeded|step_up_failed`, `organization.external_identity.linked|unlinked` (`via: source_anchor|administrator|provisioning`), `organization.user.provisioned`, `organization.directory.entra_reconcile_*` (run, identity disabled, user deactivated), `access.role.assignment_refused` is not needed (the 409 is enough).

### Failure modes

| Failure | Behaviour |
| --- | --- |
| Entra or proxy unreachable | `entra_unavailable`; other login methods unaffected; health `failing` |
| JWKS rotated, new `kid` | one refresh, then success |
| Credential expired | token exchange fails `invalid_client`; health `failing` before users report it (expiry monitor) |
| Clock drift beyond skew | tokens refused; health shows the server time check |
| Reconciliation failing | sessions bounded by the session cap; health `failing`/`stale` |
| User deleted and recreated in Entra (new `oid`) | not linked automatically (except the hybrid anchor match); administrator links again |
| Tenant migration/merger | new `tid` means new identities; administrator relinks; no automatic tenant switch |

### Testing strategy

- Fake IdP (`integrations/entra.Fake`): signs tokens with test keys; tests cover every validation rule (wrong `iss`, `aud`, `tid`, consumer tenant, expired, future `nbf`, skew bounds, missing `nonce`, replayed `state`, missing binding cookie, `alg` none and HS256 confusion, unknown `kid` refresh and rate limit, key rotation).
- PostgreSQL tests: linking order, hybrid anchor match (single, none, ambiguous), email collision refusal, guest refusal, auto-provisioning creates no roles, administrator link rules (dominance, self, roles held, local credential removal), concurrent sign-in against deactivation, reconciliation deactivation and session revocation in one transaction, assurance and high-risk exclusion, step-up issues a new session.
- Handler tests: open-redirect guard, cookie flags, uniform failure redirects, throttling, no secrets or claims in logs (log capture assertion).
- Development: Keycloak lab realm for the generic OIDC path ([lab services](../development/lab-services.md)); Entra specifics (`tid`, `oid`, `acrs`, `onPremisesImmutableId`, guests) only against a lab tenant; recorded in current status as manual verification until automated.

### Threat model (Entra login)

| Threat | Control |
| --- | --- |
| Account takeover through email/UPN linking | key is `tid`+`oid`; email collision refuses provisioning; administrator linking only, dominance rule, session revocation, notice to the target |
| Token from another tenant (multi-tenant app) | `iss` bound to the token's `tid`; `tid` allow-list; consumer tenant refused |
| Malicious tenant sets `onPremisesImmutableId` to match a victim's GUID | anchor matching only for the one tenant bound to the directory provider and only with `onPremisesSyncEnabled = true` |
| Login CSRF / session swapping | `state` hashed and single use, browser-binding cookie, `nonce`, top-level GET only |
| Authorization-code interception | PKCE S256, confidential client, exact redirect URL |
| Open redirect after login | `returnTo` limited to relative in-app paths |
| Signature bypass (`alg` none, HMAC with public key) | RS256 only, keys only from the allow-listed JWKS |
| JWKS fetch abuse via forged `kid` | refresh rate limit, fixed JWKS URL |
| Replay of an ID token | nonce bound to a consumed transaction, short `iat` window |
| Redirect URI manipulation behind proxies | fixed configured URL, never from headers |
| Disabled user keeps access | reconciliation, LDAP sync, session cap; health warns without reconciliation |
| Privilege through Entra group membership (M365 group owners, guests) | security groups only, no high-risk via groups, Directory Group freshness |
| Low-assurance session uses admin rights | high-risk needs `mfa` assurance, step-up issues a new session |
| Shared workstation reuses previous Entra session | `prompt=select_account`, optional Entra sign-out redirect, session cap |
| Guest or partner-tenant user lands as employee | guests refused until A-G, then only to pre-created `external` Users |
| Credential theft from the API host | certificate preferred, secret files readable only by `turaco-api`, no Graph application permissions on the sign-in registration |
| Database breach | no refresh or access tokens stored; transaction rows hold nothing reusable after 10 minutes |

## Microsoft Teams

Slice T-A (channel posts) is implemented, see the [implementation record](#slice-t-a-2026-10-10); T-B to T-E are planned ([ADR-0036](../decisions/ADR-0036-microsoft-teams-integration.md)). Teams is a delivery and interaction channel; Turaco records stay authoritative.

### Phases

| Phase | What | Inbound needed | Depends on |
| --- | --- | --- | --- |
| T-A | Channel posts to Teams Channel Destinations (Workflows webhook) for routed categories, for example Major Incident declared/updated, change scheduled | no | M-0 |
| T-B | Personal Adaptive Cards for `ticket.assigned`, `approval.requested`, `majorincident.update`, with deep links | no (proactive send) | M-0, Entra identity links (E-A or E-C), Teams app package |
| T-C | Approve/reject buttons on approval cards (Universal Actions `Action.Execute`) | yes (bot messaging endpoint) | T-B |
| T-D | Link a Teams meeting or channel to a Major Incident or a Turaco Team; optional online-meeting creation | no (creation: delegated consent at use) | M-0 |
| T-E | Microsoft 365 presence (ADR-0028 P-C) on the shared Graph read registration | no | M-0, P-C design and data protection review |

### Delivery through the notification platform

- No parallel system: personal Teams messages are Notification Deliveries with `channel = teams` created in the same claim transaction that creates the in-app notification (like email), when the module is on, the recipient has a linked Entra identity and has not opted out. Job `notifications.teams.send` claims the delivery, re-checks that the recipient is active, still holds the category's read condition (the producer's rule, as for email) and still has the identity, renders the card from the category registry and sends it. Statuses and retries are the email ones.
- Channel posts are not per recipient. A **Channel Route** maps a category to a Teams Channel Destination (`platform.notification_channel_routes`, administrator-managed). An outbox consumer of the notification platform creates one delivery per (event, destination) with `channel = teams_channel` (deliveries gain a nullable `notification_id`, a `destination_key` and a dedupe key; forward migration). Only categories marked `broadcastable` by their owning module may be routed (Major Incident, change scheduled); personal categories such as approvals never.
- Preferences: users opt out per category and channel (`teams` added to the preference channels). Default for `teams`: on for the three T-B categories, off for others (open question).
- Categories gain a Teams card template (English and German) next to the email texts; the registry stays owned by the producing module.

### Identity mapping

A Teams recipient is the Entra External Identity `entra:<tid>` + `oid` of the User; inbound activities map `channelData.tenant.id` + `from.aadObjectId` to it. The link comes from Entra sign-in (E-A) or Graph reconciliation (E-C, which can create hybrid links through the source anchor without anyone signing in). No UPN, email or display-name matching; a recipient without a link gets no Teams delivery (`cancelled`, reason `no_teams_identity`). External accounts get Teams personal cards only if an administrator links them and only for categories the ceiling allows; never channel routes.

### Data minimisation (patient data)

- Default content mode `TEAMS_CARD_CONTENT=reference_only`: category wording ("A ticket was assigned to you"), the reference number visible to the recipient (`TKT-000123`, Queue prefix rules of F13 apply), priority, and a deep link. No titles, descriptions, comments, names of affected persons, device names or locations.
- `with_titles` is an administrator opt-in for personal cards only, requires a recorded DPIA date (like Presence), is audited and shown on the Integrations page; titles pass `platform/safetext` and are length-capped. Channel posts are always `reference_only` because channel membership is outside Turaco authorization.
- Deep links are built from the configured public base URL (`EMAIL_BASE_URL` today) and a known target type and validated id; opening them needs a Turaco session and normal authorization.
- Teams message retention and eDiscovery are the customer's Microsoft 365 configuration; minimised content keeps that copy harmless.

### App registration, consent and endpoints

- One Teams app package (manifest with the bot, `webApplicationInfo`, no tabs) published to the organization's app catalog by the customer's Teams administrator; the bot is an Azure Bot resource (single-tenant) whose app id is the Teams bot app registration.
- T-B proactive delivery requires the app to be installed for the recipient: administrators pre-install it through a Teams app setup policy, or Turaco installs it per user through Graph (`TeamsAppInstallation.ReadWriteForUser.All`, application). Proactive send uses the Bot Connector REST API with a token for the bot's app id (outbound only).
- T-A uses Workflows webhook URLs (the "post to a channel when a webhook request is received" flow); the URL is a bearer secret.
- T-C messaging endpoint `POST /api/v1/integrations/teams/activities`: outside the session and same-origin guard, authenticated only by the Bot Framework JWT. Validation: RS256, keys from the Bot Framework OpenID metadata (cached with the JWKS rules of the Entra section), `iss` the Bot Framework issuer, `aud` the bot app id, `exp`/`nbf` with skew, the key's endorsements include `msteams`, the `serviceUrl` claim equals the activity's `serviceUrl`, and that host is on the Bot Connector allow-list (Turaco later sends replies there; an unvalidated `serviceUrl` would be an SSRF and token-exfiltration path). The activity tenant must be an allowed tenant. Activity ids are recorded in the inbound replay store (`platform/externalrefs`) for 24 hours. Body size cap 64 KiB, rate limit per tenant.
- On-prem: T-A, T-B and T-D need outbound egress only. T-C needs Microsoft to reach the messaging endpoint over the internet (reverse proxy publishing only that path); without it T-C stays off and approval cards carry only "Open in Turaco".

### Actionable cards (T-C)

- An approval card gets buttons only when the Approval is not high-impact (Change emergency approvals, Deployment plans, high-risk role grants and anything the owning module marks `highImpact` are open-in-Turaco only) and the recipient is not an external account.
- Each button carries only an opaque **Teams Card Action Token** (256 bit, SHA-256 hash stored, bound to approval id, step, recipient User and decision, expires when the step ends or after 24 hours, single use).
- On invoke: JWT validated, activity not replayed, `from.aadObjectId` + tenant map to the token's recipient User (mismatch: refused, audited `teams.card_action_refused`), the User is active and the identity enabled, then the Approvals public contract decides **as that User** with a freshly evaluated principal (the same checks as `POST /approvals/{id}/approve|reject`: assignment, requester exclusion, first decision wins). The card payload is never trusted for ids, decision context or permissions. The decision is audited by Approvals with `via: teams`. The card is refreshed to a reference-only result.
- A forwarded card or screenshot is useless to anyone else; a compromised Teams account can decide only what that person could decide in Turaco, without MFA assurance, which is why high-impact approvals are excluded.

### Meetings and channels (T-D)

- A Major Incident (and a Turaco Team, for the daily and weekly operations meetings) can hold Teams links: a validated `https://teams.microsoft.com/...` meeting join URL or channel link, stored as an external reference (`platform/externalrefs`, system `teams`), shown with "Join"; adding needs `majorincidents.manage` (Major Incident) or `organization.teams.manage` (Team); audited `servicedesk.major_incident.teams_link_added|removed`, `organization.team.teams_link_added|removed`. No meeting or calendar entity in Turaco.
- Optional creation of an online meeting for a Major Incident: delegated `OnlineMeetings.ReadWrite` requested by incremental consent at the moment the incident manager clicks "Create Teams meeting", used once, not stored. Application-permission meeting creation (needs a Teams application access policy per organizer) is not used. Subject is the reference number only.

### Port and code (planned)

`backend/internal/integrations/teams`:

```go
type Sender interface {
    Mode() health.Mode                                   // real | fake | not_configured
    PostToChannel(ctx context.Context, destinationKey string, card Card) error
    SendPersonal(ctx context.Context, tenantID, objectID string, card Card) error
}
// Card is a Turaco-owned typed message (kind, category, reference, link, locale,
// optional title, optional action tokens); Adaptive Card JSON is rendered in the adapter.
// Errors wrap ErrTransient (retry, honours RetryAfter), ErrPermanent or ErrRecipientUnreachable
// (app not installed, user unknown).
```

`Fake` records sent cards and can inject failures and 429s; `NotConfigured` returns `ErrNotConfigured`. The inbound activity verifier is a separate small type in the same package with its own `Fake` signer for tests. Notifications depend on the port, not on Microsoft types.

### Module switch, permissions and audit

- Optional module `teams` (default off; preconditions: adapter configured, for `with_titles` a DPIA date). Off: no deliveries are created, the inbound endpoint answers 404, links stay stored.
- Permission `integrations.teams.manage` (high): Channel Destinations' routes, content mode, per-user app installation, test send. Users manage their own Teams preferences without a permission.
- Audit: `notifications.teams.route_created|route_deleted`, `notifications.teams.content_mode_changed`, `notifications.teams.test_sent`, `teams.app_installed_for_user`, `teams.card_action_refused`, approval decisions as today with `via: teams`, the T-D link actions above. Deliveries themselves are operational state, not audit.

### Rate limiting and 429

Graph, the Bot Connector and Workflows return 429 with `Retry-After`: the job is retried no earlier than `Retry-After` (capped at 1 hour) without counting against the five attempts more than once; a per-destination and a global token bucket in the worker smooth bursts (a Major Incident fan-out to many recipients is queued, not dropped); 5xx and timeouts are transient; 4xx other than 429 permanent. Limit values are verified in T-B.

### Health and setup

Check `teams` (module state, mode, per-phase configuration, deliveries failed in 24 hours by reason, oldest pending, recipients without identity link count, inbound endpoint last valid activity for T-C) on `/admin/health` and `/admin/integrations`. Setup item 9 "Integrations" covers it; no new item.

### Testing

Port contract tests against the Fake (retry classification, 429 handling, recipient unreachable); delivery creation and cancellation in PostgreSQL tests (no identity, opted out, permission lost, module off); content tests asserting that `reference_only` cards contain no title and no user free text for every category; JWT verifier tests with a fake Bot Framework signer (wrong audience, issuer, endorsement, `serviceUrl` mismatch, foreign tenant, replay, expired); card-action tests (wrong user, used token, decided approval, high-impact approval has no buttons, external account); manual verification in a lab tenant.

### Threat model (Teams)

| Threat | Control |
| --- | --- |
| Patient data leaks into Teams (and its retention/eDiscovery) | `reference_only` default, titles opt-in with DPIA for personal cards only, channel posts always reference-only |
| Channel members without Turaco access learn about records | broadcastable categories only, reference-only content, link requires authorization |
| Forged inbound activity | Bot Framework JWT validation (issuer, audience, keys, endorsement), tenant allow-list |
| SSRF/token exfiltration through `serviceUrl` | `serviceUrl` must match the token claim and the host allow-list |
| Card replay or forwarding decides an approval | single-use action token bound to recipient and step, re-authorization as the mapped User, replay store |
| Teams account compromise approves high-impact work | high-impact approvals have no buttons; normal Turaco rules apply |
| Webhook URL leak lets others post into the channel | URL in a deployment secret file, never in the database, logs or API responses; rotation by replacing the flow |
| SSRF through a configured webhook URL | https only, host allow-list for Workflows hosts, no redirects, dial-time IP checks (reuse `platform/ai/safehttp` rules) |
| Mis-mapped recipient | mapping only by `tid`+`oid`, no email/UPN fallback |
| Notification flood / Microsoft throttling | dedupe as email, token buckets, `Retry-After` respected |
| Over-privileged app registration | separate bot registration, permissions per consent matrix, no mail, chat-read or file permissions |

## Shared Microsoft foundation

### One app registration or several

Decision (proposed): several, separated by trust level and by the process that holds the credential.

| Registration | Used by | Process holding the credential | Permissions |
| --- | --- | --- | --- |
| Turaco Sign-in | Entra login | `turaco-api` | delegated `openid`, `profile`, `User.Read` (hybrid lookup only); no application permissions |
| Turaco Graph read | Entra reconciliation and group sync (E-C), Intune read sync (F6), presence (P-C, T-E) | `turaco-worker` | application, read-only (matrix below) |
| Turaco Intune write | Deployment ring writer (F9) | `turaco-worker` | as decided in [Intune](../integrations/intune.md); disabled by default |
| Turaco Teams bot | T-A to T-D | `turaco-worker` (send), `turaco-api` (inbound validation needs only the public app id) | bot plus listed Teams permissions |

Rationale: a stolen sign-in secret cannot read the directory; a Graph read secret cannot sign anyone in or post to Teams; the write registration stays isolated as already decided; the API process holds no application permissions. Small installations may run without the Graph read registration (then no reconciliation and no group sync; health shows the gap). Merging registrations is not supported in the first slices (open question).

### Consent matrix

All application permissions need tenant admin consent; names to be verified in each spike.

| Feature | Registration | Permission | Type |
| --- | --- | --- | --- |
| Sign-in | Sign-in | `openid`, `profile` | delegated |
| Hybrid source-anchor lookup at sign-in | Sign-in | `User.Read` | delegated |
| Reconciliation (enabled/deleted users, anchors) | Graph read | `User.Read.All` | application |
| Cloud-only Directory Group sync | Graph read | `GroupMember.Read.All` | application |
| Intune read sync | Graph read | `DeviceManagementManagedDevices.Read.All`, `DeviceManagementApps.Read.All`, `DeviceManagementConfiguration.Read.All` | application |
| Presence (P-C) | Graph read | `MailboxSettings.Read`, `Calendars.ReadBasic`-class (free/busy and work location only, ADR-0028) | application |
| T-A channel posts | none | Workflows URL (no Graph permission) | n/a |
| T-B proactive cards | Teams bot | Bot Framework channel; `TeamsAppInstallation.ReadWriteForUser.All` only if Turaco installs the app per user | application |
| T-C actions | Teams bot | none beyond the bot | n/a |
| T-D link | none | none | n/a |
| T-D create meeting | Sign-in | `OnlineMeetings.ReadWrite` (incremental consent, used once) | delegated |

Never requested: mail, chat or channel message read, files, `Directory.ReadWrite.All`, `Application.*`, `RoleManagement.*`.

### Credentials storage and rotation (ADR-0014)

- Credentials are deployment secret files (`*_FILE`), mounted only into the process that needs them; the database never holds them (ADR-0014 key management for database-held secrets is not implemented, and F14 keeps integration secrets out of the UI). The same applies to Workflows webhook URLs: `TEAMS_CHANNEL_DESTINATIONS_FILE` maps destination keys to URLs; the UI and API show keys only.
- Certificates (client assertion) are preferred over secrets; Turaco reads the certificate's `NotAfter` and reports days to expiry; for secrets the operator sets `*_SECRET_EXPIRES_AT`. Health: attention at 30 days, `failing` after expiry.
- Rotation without downtime: add the new credential in Entra, replace the file, Turaco re-reads files on each token acquisition when the modification time changes, then remove the old credential in Entra. Tokens acquired with client credentials are cached in memory only until shortly before expiry.

### Shared HTTP client (`integrations/microsoft`, slice M-0)

Cloud endpoint table (`global|usgov|china`), host allow-list per feature, `MICROSOFT_HTTP_PROXY` and `MICROSOFT_CA_FILE`, https only, no redirects, response size and time caps, dial-time IP checks when no proxy is used, client-credential token acquisition with certificate or secret, 429/`Retry-After` and transient-error classification, request ids in logs without tokens or bodies. The Intune Graph clients are built on it as well: `microsoft.TokenSource` (client credentials with caching) and `microsoft.Graph` (retry with `Retry-After`, `@odata.nextLink` restricted to the Graph host, caps) carry `integrations/intune` `GraphProvider` and `GraphWriter`, implemented per documentation and unverified against a live tenant ([Intune](../integrations/intune.md#microsoft-graph-clients-implemented-per-documentation-unverified)); the write registration uses `MICROSOFT_GRAPH_WRITE_*`.

## Implementation slicing

| Slice | Content | Acceptance criteria |
| --- | --- | --- |
| M-0 | `integrations/microsoft` client, credential loader, expiry health check, configuration keys | Fake HTTP server tests: proxy CONNECT, allow-list refusal, no redirects, 429 classification, file rotation re-read; health reports days to expiry without values |
| E-A | Entra sign-in core: start/callback, transactions, validation, link-only, sessions, `GET /auth/methods`, UI button, audit, health | every validation rule tested against the Fake IdP; failures uniform and audited; Keycloak lab sign-in works; `make check` green; docs and configuration reference updated |
| E-B | Linking: administrator link/unlink, hybrid source-anchor match, auto-provisioning policy, guest refusal | no link by email in any path (tests); collision refusal; ADR-0034 R5 rules hold; dominance tests |
| E-C | Graph reconciliation and cloud-only Directory Group sync on the Graph read registration | disabled Entra user loses sessions within one interval (test with Fake); mass-removal safeguard; no high-risk via Entra groups (409) |
| E-D | Assurance and step-up; evaluator rule; R10 text update in F14 | low-assurance Entra session has no high-risk permission; step-up issues a new session; local accounts unchanged |
| E-E | `multi_restricted` tenants and guests as `external` Users (after F14 A-G) | foreign tenant refused unless allowed; guest maps only to pre-created external User; ceiling applies |
| T-A | `teams` module, channel routes, `teams_channel` deliveries, Workflows adapter | broadcastable-only routing; reference-only content test per category; 429 retry; module off creates nothing |
| T-B | Teams app package, bot send, personal deliveries, preferences | deliveries only for linked, active, permitted recipients; cards contain no titles by default; deep links valid |
| T-C | Inbound endpoint, JWT verifier, action tokens, Approvals decision as User | all verifier negatives refused; token single use and recipient-bound; high-impact has no buttons; security review passed |
| T-D | Teams links on Major Incidents and Teams; optional delegated meeting creation | URL validation; audit; no token stored |
| T-E | Presence P-C Microsoft 365 source on the Graph read registration | per ADR-0028 and F11 P-C after the data protection review |

Every slice: OpenAPI, generated references, current status, this design's implementation record, `review-security` for E-A, E-B, E-D and T-C before merge.

## Implementation record

### Slices M-0 and E-A (2026-10-10)

Built: `integrations/microsoft` (https only, per-client host allow-list, no redirects, response size and time caps, optional explicit proxy and CA file, dial-time public-address check when no proxy is set, client credential from a secret file that is re-read when its modification time changes or from a certificate with a signed client assertion, expiry status), `integrations/entra` (authorization URL with PKCE S256, `prompt=select_account` and no `offline_access`; code exchange; ID token validation: RS256 only, key by `kid` from the JWKS of the configured authority with a 24-hour cache and at most one refresh per five minutes, issuer bound to the token's own tenant, tenant allow-list, consumer tenant refused, audience and `azp`, `exp`/`nbf`/`iat` with skew, nonce in constant time, `ver` 2.0, GUID checks, guest detection by `acct` and `idp`), the port and browser flow in `platform/authentication` (`GET /api/v1/auth/entra/start|callback`: server-side transaction with hashed state and browser binding, single use also on failure, own throttle budget, uniform audited failures as redirects to `/login?error=<code>`, open-redirect guard on `returnTo`, session `auth_method = entra` limited by `ENTRA_SESSION_MAX_AGE` and the setting `auth.session_absolute_timeout`), identity lookup by tenant id + object id (`organization.external_identities`, provider key `entra:<tid>`), the CLI `turaco-admin entra link|unlink` (audited, revokes Entra sessions on unlink; users with a local credential are refused until the atomic replacement of the people administration is used), `GET /auth/methods` reports `entra`, configuration keys and health check `entra_login` (credential expiry: attention at 30 days, failing after expiry, unknown for a secret without `ENTRA_CLIENT_SECRET_EXPIRES_AT`), the login button "Sign in with Microsoft Entra", migration `000075`.

Deviations from the plan, chosen on purpose: the ID token is validated by a small RS256 verifier in `integrations/entra` instead of the libraries approved in ADR-0035 (go-oidc, go-jose, x/oauth2): the endpoints are fixed paths of the allow-listed authority, so no discovery document is fetched and no new dependency enters the build; the libraries remain approved should the verifier need to grow.

Not built yet: Graph reconciliation and cloud-only group sync (E-C), step-up and assurance (E-D), guests and `multi_restricted` guest handling (E-E). Verified only against a fake identity provider and unit tests; no real Entra tenant was available.

### Slice E-B (2026-10-10)

Built: the administrator API (`POST /users/{id}/external-identities/entra`, `DELETE /users/{id}/external-identities/{identityId}`, `GET /users/entra-linking`) with `application.EntraLinking` (input validation: GUIDs, tenant must be in `ENTRA_ALLOWED_TENANT_IDS`, the consumer tenant never; best-effort notice mail through `IdentityNotifier`) and the repository operations `LinkEntraIdentity`/`UnlinkEntraIdentity` (one transaction each: platform administrator, not oneself, emergency account refused, active employee, `expectedVersion`, dominance R1, local credential replaced atomically as ADR-0034 R5 and refused while roles are held, open credential tokens end, one identity per tenant, version bump, audit). The existing people operation `link-directory-identity` could not be reused: it is bound to a directory synchronization conflict and turns the origin into `directory`, whereas an Entra link keeps the origin; the R5 steps (credential remover, token and session revocation, role check) are reused through the same collaborators. A User with an Entra identity cannot be issued an invitation or reset (a new password would reopen the pre-provisioning path). The user detail lists identities with id, provider key, the last four characters of the subject, linked-at and `via` (migration `000078`, column `external_identities.linked_via`: `administrator|source_anchor|provisioning|cli`); the UI section "Microsoft Entra" (link dialog with GUID validation, tenant default from `GET /users/entra-linking`, takeover confirmation; unlink dialog).

Hybrid match: with `ENTRA_LINK_DIRECTORY_PROVIDER_KEY` the authorization and token requests add the delegated scope `User.Read`; the adapter calls Graph `GET /v1.0/me?$select=onPremisesImmutableId,onPremisesSyncEnabled` once, in memory, only for a non-guest member of `ENTRA_TENANT_ID` (the key requires `ENTRA_TENANT_ID` in `ENTRA_ALLOWED_TENANT_IDS`), decodes the base64 anchor (16 bytes, Active Directory mixed-endian objectGUID, the same layout as LDAP sync) and sets `VerifiedIdentity.SourceAnchor` only when `onPremisesSyncEnabled` is true; any Graph failure or non-GUID anchor leaves it empty, which can only make the match fail. The login handler calls the match only for an unlinked home-tenant identity; the repository locks the directory identity's User and links only when exactly one enabled directory identity of that provider has the GUID and its User is an active directory-owned employee without an identity of this tenant (audit `organization.external_identity.linked`, `via: source_anchor`). An anchor that names an unusable User ends in `entra_not_linked` without falling through to provisioning; no match falls through. Ambiguity cannot occur in the database (unique provider key + subject) and is covered with a fake at the login layer. OpenLDAP (`entryUUID`) installations do not match, as designed (AD only).

Provisioning: the runtime setting `auth.entra_provisioning` (`link_only` default, no longer `notYetActive`) is read per sign-in; `auto_employee` creates an employee (origin `directory`, active, no roles, no Team) for a first sign-in of a home-tenant member who is not a guest. Name and primary email come from the token and must pass the normal profile validation; a missing or invalid email refuses provisioning (the collision check needs it). A primary email that another User has (case-insensitive) refuses with `entra_not_linked`. Deviation: the "attention entry" for administrators is the audit trail (`auth.login.failed` reason `email_conflict` and `organization.user.provisioning_refused` on the existing User) rather than a new notification or dashboard concept; a health or attention surface can read these events later. A per-identity advisory lock and the unique indexes make concurrent first sign-ins converge on one User. Unlinking an inactive User's identity is allowed on purpose (only the link needs an active target).

Not built: Graph reconciliation, step-up, guests, a bulk or CSV link.

## Decisions of the product owner (2026-10-10)

The fifteen open questions are answered. They bind the implementation; the numbering is that of the earlier question list.

| # | Decision |
| --- | --- |
| 1 | Default provisioning is `link_only`; an administrator may switch to `auto_employee` in the settings. |
| 2 | Entra is one sign-in method among several. LDAP/AD, Kerberos, local accounts and Entra can be enabled in any combination; the sign-in page shows a "Sign in with Microsoft Entra" button next to the others. Nothing forces Entra to be the only method. |
| 3 | The Entra session cap is 8 hours by default and is a runtime setting in the administration settings (`/admin/settings`), not only an environment variable. |
| 4 | Sign-out from Entra on Turaco logout is a setting with the values `never`, `shared_only` (default) and `always`. With `shared_only` the sign-in page offers a "shared computer" choice; logging out of such a session also ends the Entra session. |
| 5 | Entra ID P1 (Conditional Access authentication context) is a supported prerequisite for MFA step-up. |
| 6 | Both layouts are supported: separate registrations (recommended) and one combined registration for small installations (the same client id in several configuration keys is valid). |
| 7 | Global cloud only (EU tenants and data boundary); sovereign clouds are not built. |
| 8 | Guests (B2B) wait for F14 A-G (External Parties). |
| 9 | No SCIM. The 15-minute reconciliation pull is sufficient. |
| 10 | Phases T-A, then T-B, then T-C. On-premises installations have no inbound access from outside, so they get T-A (outbound only); T-B and T-C require a reachable bot endpoint and are available for hosted installations or on-premises installations that publish one deliberately. |
| 11 | Personal Teams notifications are opt-in per user. An administrator can also set the default or switch the channel off for everybody in the settings. |
| 12 | Ticket titles in personal cards (`with_titles`) stay off. An administrator can enable them only with an explicit acknowledgement in the settings and after the data protection review. |
| 13 | Channel routing may carry Major Incident declared/updated and Change scheduled. The hospital's "Infrastruktur Ausfälle" chat is the first destination for Major Incidents. |
| 14 | Teams links can be attached to Major Incidents and to Turaco Teams (for the daily and weekly meetings). |
| 15 | The customer's Teams administrator publishes a Turaco-provided app package; Teams is already installed on the workstations. |

## Superseded: open questions (kept for the rationale)

1. Default provisioning: `link_only` (proposed) or `auto_employee` for cloud-only customers?
2. Should Entra be allowed as the **only** sign-in method in production (no LDAP, no local accounts), given the emergency account remains the break-glass path?
3. Entra session cap: 8 hours (proposed, one shift) or longer for office staff?
4. Sign-out from Entra on Turaco logout by default for hospitals with shared workstations?
5. Is Entra ID P1 (Conditional Access authentication context) a supported prerequisite for MFA step-up, or must `amr`-only customers be supported?
6. Is a separate Graph read registration acceptable to customers, or must Turaco offer one combined registration for small installations?
7. Sovereign clouds (US Government, China): needed, or global cloud only?
8. Guests (B2B) as external accounts: needed before F14 A-G, or wait?
9. SCIM inbound provisioning: required by any customer, or is the 15-minute reconciliation pull acceptable?
10. Teams: which phases first (proposed T-A, T-B, then T-C)? Is the inbound bot endpoint acceptable for on-prem hospitals at all?
11. Teams personal notifications on by default for the three categories, or opt-in per user?
12. May any customer enable ticket titles in personal cards (`with_titles`), and is the DPIA date the right gate?
13. Which categories may be routed to channels (proposed: Major Incident declared/updated, change scheduled)?
14. Should Teams links be attachable to Turaco Teams for the daily and weekly meetings, or only to Major Incidents?
15. Who publishes the Teams app package: the customer's Teams administrator from a Turaco-provided package (proposed) or a store listing?

### Slice T-A (2026-10-10)

Built: `integrations/teams` (the `Sender` port with `Mode()`, `DestinationKeys()` and `PostToChannel`; the `Workflows` adapter over the shared `integrations/microsoft` client with an exact-host allow-list derived from the destinations file; `Fake` and `NotConfigured` adapters; a contract test suite that runs all three), migration `000080` (`platform.notification_channel_routes`; `notification_deliveries` gains nullable `notification_id`, `destination_key`, `dedupe_key`, `payload` and the channel `teams_channel`, unique per channel, destination and dedupe key), `platform/notifications` (`Category.Broadcast` marks a category broadcastable and carries the English and German reference-only wording; `Service.PostToChannels` creates one delivery and one job per route inside the producer's outbox transaction; `ChannelSender` is the job handler `teams.channel_post`; route management with audit), the producers' consumers (`servicedesk.post-major-declared`, `servicedesk.notify-major` for updates, `changes.post-scheduled`), the optional module `teams` (route prefix `integrations`, administration routes open while it is off), the health check `teams`, the screen `/admin/teams-channels` and the permission `integrations.teams.manage`.

Decisions taken in the slice:

- Broadcastable categories are `majorincident.update` (kinds declared and updated) and `change.scheduled` (kind scheduled). Major Incident exercises are never posted. The continuation events of the notification fan-out never post, so one declaration or update is one post per destination; the outbox event id is the dedupe key.
- A post carries the category wording, the reference number (validated to a title-free pattern) and a link built from `EMAIL_BASE_URL`; the card type has no field for a title, description or name. Content tests assert this at the Card, the delivery payload and the worker flow.
- Webhook URLs come only from `TEAMS_CHANNEL_DESTINATIONS_FILE` (JSON object of destination key to URL; hosts `*.logic.azure.com` and `*.api.powerplatform.com`, https, port 443). Both processes read it: `turaco-worker` to post and `turaco-api` only for the key list. The API, UI, logs, audit and stored errors carry keys and short machine codes, never URLs or response bodies.
- Module off: no delivery is created and the send job stays pending (durable job gate); a post that is still unsent after six hours is cancelled as `stale`, so enabling the module later does not announce old incidents. A deleted route cancels its waiting posts (`route_removed`).
- 429 and 5xx are transient (the job is retried no earlier than `Retry-After`, at most one hour, with the normal back-off; every attempt counts, at most eight); other 4xx and redirects are permanent. The runner gained `jobs.RetryAfter` for this.
- Not built in T-A: a per-destination token bucket (a Major Incident produces few posts), test send, per-user Teams preferences, `with_titles`, the personal channel and the bot (T-B onward). The Workflows request format and host names are from documentation and must be verified in a lab tenant before production use.

