# ADR-0034: Local accounts with invitation tokens and restricted external accounts

- Status: Accepted (2026-10-09). Local accounts (points 1 to 3, 5 and 6) are implemented in the backend since 2026-10-09 (slices A-A and A-A2) and administered through the People and Access UI (slices A-D and A-E); external accounts, point 4, follow with slice A-G and are not implemented; accepted after the Opus security review, whose blockers are resolved below. Design: [F14 Administration](../product/f14-administration-design.md).

## Context

Turaco authenticates against a directory (LDAP/AD password bind, Kerberos) and has one local credential type, the break-glass emergency account created only with `turaco-admin` ([identity design](../security/identity-access-design.md) section 7, [ADR-0013](ADR-0013-authentication-abstraction.md)). An administrator who creates a person in Turaco without a directory account cannot give that person a way to sign in, so external vendors and installations without a directory are unsupported. The simulation worked around this by signing every persona in through the emergency endpoint. External people also need a much smaller footprint than employees: the baseline every signed-in User has (raise and read own tickets, read published employee articles, see own assets, public incident pages) is too wide for a vendor.

## Decision

1. **Local accounts** are a second credential kind in `platform.local_credentials` (`kind` `emergency` or `local`). They are meant for Users outside the directory. The emergency account keeps its CLI-only lifecycle and endpoint; nothing in the UI edits it. `FindLocalCredential` filters by kind per endpoint, and local logins use an argon2 slot pool separate from break-glass.
2. **Local sign-in is disabled by default** (`AUTH_LOCAL_LOGIN_ENABLED=false`, a global switch that turns local accounts off completely) and has its own endpoint, throttling keys, account lockout and audit actions, separate from the emergency login. A directory-linked User can never have a local credential; linking a directory identity to a local User is administrator-only and atomically deletes the local credential, open tokens and sessions, and is refused while the User holds roles.
3. **Credentials are set only through single-use tokens** (invitation, reset): 256 bits random, only a SHA-256 hash stored, expiry 7 days (invitation) or 24 hours (reset), a new token invalidates earlier ones. An administrator never sees or chooses a password. Delivery is email to the stored address through the existing SMTP channel; a link is shown once to the administrator only for the invitation of a never-activated account. Links are built only from the configured `EMAIL_BASE_URL`, carry the token in the URL fragment and are served with `Referrer-Policy: no-referrer`; redeem needs an active, unexpired, non-directory User, a same-origin guard and throttling, and never logs in. Setting a password revokes the User's sessions. Passwords are argon2id under a separate local-account policy function (minimum 12 characters, larger common-password list, checks against name, email and login).
   Destructive or takeover-capable operations on a local account (reset, invitation, email change, deactivation, departure) need a platform administrator or an actor holding all effective permissions of the target (dominance rule); an email change revokes open tokens and notifies the old address.
4. **External accounts** (`account_kind = external`, immutable and loaded with `IsActive` on every request) are restricted principals: an External Party record, a required internal sponsor, a required expiry date (maximum 365 days, extended only by an explicit audited operation), a closed permission ceiling that the evaluator enforces by intersection, and an HTTP allow-list declared per route family in the module registry. They see Tasks assigned to them or their Team and Tickets explicitly shared with them through `ticket_participants` (public comments only); grants through Team subjects (queue grants, Views shares, pin rules) exclude external accounts, and search, Views, query endpoints and AI answer 404 for them.
5. **Expiry is enforced at authentication time** (a User past `access_expires_at` is inactive for every request) and cleaned up by a job that sets the status and writes the audit trail.
6. **Step-up gap:** until a TOTP/step-up ADR exists, local accounts cannot hold high-risk permissions (assignment refused, evaluator excludes them), so a local account is never a platform administrator.
7. **No new framework or dependency**: the same Go standard library, `x/crypto/argon2` and SMTP channel already in use. No OIDC/SAML or MFA is introduced here; they are the follow-up for external parties (hook: `kind` and the allow-list stay unchanged).

## Alternatives considered

- **Require a directory account for everyone** (guest accounts in AD/Entra): the best option where available, but not possible for installations without a directory and slow for one-off vendors. Remains supported; local accounts are optional.
- **Administrator-set passwords:** simple, but the administrator learns a credential and shared initial passwords persist. Rejected.
- **A separate vendor portal application:** a second trust boundary and UI without a demonstrated need; the restricted principal on the same API gives the same isolation with fewer parts.
- **Role-only restriction for vendors** (the current `vendor-restricted` role): works for permissions but not for the baseline rights every signed-in User has, and cannot enforce expiry. Insufficient alone; the role template remains useful together with the ceiling.

## Consequences

- Authentication gains a state (credential tokens) and an endpoint family; throttling and the login path are reviewed again by `review-security`.
- The evaluator, `UserAccess` and the module registry each get a small, tested extension; a test fails when a route family does not declare how external principals are handled.
- Operators who enable local login accept password risk; the Health page flags enabled local login and the emergency login as attention items.
- Service Desk gains `ticket_participants` (owned by Service Desk).
- Revisit when MFA/SSO for external parties is designed.

## Review outcomes

The Opus security review of 2026-10-09 produced ten binding rules, all recorded in the [F14 design, Review outcomes](../product/f14-administration-design.md#review-outcomes-opus-security-review-2026-10-09): dominance rule for account-takeover operations (R1), Team-subject grants outside the ceiling and the role-holding rule (R2), credential kind separation, separate policies and lockout (R3), immutable `account_kind` (R4), atomic directory linking (R5), last-administrator and removal ceiling (R6), token handling (R7), opt-in remote-support template (R8), audit purge floor inside the function because a worker-only grant is not enforceable with one `DATABASE_URL` (R9), and the MFA/step-up gap closed by refusing high-risk permissions for local accounts (R10). All blockers are resolved in the text; the status is therefore Accepted. Implementation still needs a security review before merge of slices A-A2 and A-G.
