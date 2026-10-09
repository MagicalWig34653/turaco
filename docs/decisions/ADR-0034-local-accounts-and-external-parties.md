# ADR-0034: Local accounts with invitation tokens and restricted external accounts

- Status: Proposed (2026-10-09). Not implemented. Design: [F14 Administration](../product/f14-administration-design.md).

## Context

Turaco authenticates against a directory (LDAP/AD password bind, Kerberos) and has one local credential type, the break-glass emergency account created only with `turaco-admin` ([identity design](../security/identity-access-design.md) section 7, [ADR-0013](ADR-0013-authentication-abstraction.md)). An administrator who creates a person in Turaco without a directory account cannot give that person a way to sign in, so external vendors and installations without a directory are unsupported. The simulation worked around this by signing every persona in through the emergency endpoint. External people also need a much smaller footprint than employees: the baseline every signed-in User has (raise and read own tickets, read published employee articles, see own assets, public incident pages) is too wide for a vendor.

## Decision

1. **Local accounts** are a second credential kind in `platform.local_credentials` (`kind` `emergency` or `local`). They are meant for Users outside the directory. The emergency account keeps its CLI-only lifecycle and endpoint; nothing in the UI edits it.
2. **Local sign-in is disabled by default** (`AUTH_LOCAL_LOGIN_ENABLED=false`) and has its own endpoint, throttling keys and audit actions, separate from the emergency login. A directory-linked User can never have a local credential.
3. **Credentials are set only through single-use tokens** (invitation, reset): 256 bits random, only a SHA-256 hash stored, expiry 7 days (invitation) or 24 hours (reset). An administrator never sees or chooses a password. Delivery is email through the existing SMTP channel, or a link shown once to the administrator. Setting a password revokes the User's sessions. Passwords are argon2id with the emergency account's parameters, minimum 12 characters, rejected when on a bundled list of common passwords.
4. **External accounts** (`account_kind = external`) are restricted principals: an External Party record, a required internal sponsor, a required expiry date (maximum 365 days, extended only by an explicit audited operation), a closed permission ceiling that the evaluator enforces by intersection, and an HTTP allow-list declared per route family in the module registry. They see Tasks assigned to them or their Team and Tickets explicitly shared with them (public comments only).
5. **Expiry is enforced at authentication time** (a User past `access_expires_at` is inactive for every request) and cleaned up by a job that sets the status and writes the audit trail.
6. **No new framework or dependency**: the same Go standard library, `x/crypto/argon2` and SMTP channel already in use. No OIDC/SAML or MFA is introduced here; they are the follow-up for external parties (hook: `kind` and the allow-list stay unchanged).

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
