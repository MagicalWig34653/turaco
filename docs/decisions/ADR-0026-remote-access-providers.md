# ADR-0026: Remote Access Through Remote Access Providers

- Status: Accepted (2026-10-03). Planned capability; nothing is implemented.
- Related: [ADR-0008](ADR-0008-separate-agents.md) (separate agents, still valid), [agent boundaries](../security/agent-boundaries.md), [security architecture](../security/security-architecture.md).

## Context

Technicians need to see and control an employee's device from a ticket. Earlier planning (implementation plan F10 "Remote Support", agent boundaries) assumed Turaco would eventually build its own control plane, relay and media path. A remote-desktop transport (screen/input streaming, NAT traversal, relays, terminal and file-transfer channels) is a large, security-critical product of its own, and mature providers exist. Building it would delay the parts that create Turaco's value: authorization, audit, ticket and device context.

## Decision

1. Turaco does **not** build its own remote-desktop transport initially. Remote access is delivered through a **Remote Access Provider** (an external system) whose Connector implements a provider-agnostic port. **HopToDesk** is the intended first provider; the design stays open to RustDesk, RDP through a gateway, TeamViewer or a future native provider.
2. **Turaco owns:** authorization (who may start which kind of session on which Device), the identity of the initiating User, attended/unattended policy, Ticket and Device context, the **Remote Access Session** record and its audit trail, workflows around a session (link to a Ticket, notes, follow-up Tasks) and the high-level remote actions offered in that context.
3. **The provider owns:** screen/input transport, terminal and file-transfer transport, NAT traversal, relays and its endpoint client.
4. The port models the capabilities Turaco needs, not a generic plugin interface:
   - map a Turaco Device to the provider's device identity — stored as a platform external reference (`platform/externalrefs`, the mapping already used for Autotask) of the Device obtained through the Endpoint module's public contract, matched only by immutable provider/management IDs, never by hostname; mapping changes are audited;
   - start a session for one Device in attended or unattended mode and return a one-time launch handle for the authorized User;
   - end a session where the provider supports it;
   - read provider session records (actual connect/disconnect, provider account, features used) as observations with source and observed time;
   - declare supported capabilities (attended, unattended, file transfer, terminal, recording).
   There is no "execute command" or other action operation on this port.
5. A Remote Access Session keeps **Turaco-authorized facts** (initiating User, Device, Ticket, policy decision, approval) separate from **consent** (`granted | declined | not_required | unknown`) and from **provider-observed facts** (what the provider reports actually happened). Missing provider records are shown as unknown, never inferred. Lifecycle: [state machines](../domain/state-machines.md#remote-access-session-planned).
6. **High-level remote actions** (for example restart, collect diagnostics) offered in a session are typed Endpoint operations: the Remote Access module calls the Endpoint module's public contract, which executes them through a Management Provider action or a future Endpoint Agent capability, with their own permission and audit. The Remote Access module never calls a management adapter directly, and free-form command text is never accepted.
7. Neither the Connector Agent nor the Endpoint Agent becomes a remote-access transport. The provider's endpoint client and relay are a **third-party trust boundary**; installing the client is an ordinary software deployment, not an agent capability.
8. A native Turaco provider remains possible and requires its own ADR and threat model.

## Security constraints (non-negotiable for the implementation)

- Starting a session requires the initiating User's identity, a dedicated high-risk permission (`remote_access.start_attended` or `remote_access.start_unattended`), recent MFA/step-up where the identity provider supports it, and policy evaluation. **Attended with user consent is the default.** Terminal and file transfer are separate permissions (`remote_access.terminal`, `remote_access.file_transfer`), off by default.
- **Unattended** mode applies only to Devices that an explicit, typed unattended-access policy record names (by Device attributes the Endpoint module exposes, such as ownership or a device category) — never a tenant-wide default — and requires a linked Ticket. The policy is a permission plus that record, not a separate permission system. It is supported only if the provider issues a per-session, expiring, single-use credential through Turaco and the provider's native standing unattended access (permanent passwords, console access) can be disabled. Otherwise only attended mode is offered. Where provider-side access outside Turaco remains possible, Turaco's audit is documented as incomplete.
- Launch handles are single-use, short-lived, bound to the authorizing User, never persisted or logged and never placed in URLs that end up in logs. Turaco stores no reusable session credentials. Provider API credentials are deployment secrets ([ADR-0014](ADR-0014-application-level-secret-and-file-encryption.md)): encrypted, rotatable, never sent to the browser.
- The provider client is version-pinned, signature-verified and distributed through the Management Provider. Using the vendor's relay or a self-hosted relay is a recorded decision.
- The end user always sees that a session is active. Session recording, if enabled, falls under the same privacy and works-council constraints as Workforce Presence ([ADR-0028](ADR-0028-workforce-presence.md)); recordings stay with the provider until a later decision covers storage, retention and access.
- Every request, authorization, rejection, launch and close is audited with initiating User, Device, Ticket and policy decision.
- A threat model of the chosen provider (client privileges, relay hosting, encryption, account model, logging) is written before the adapter is implemented.

## Consequences

- Implementation plan F10 becomes "Remote Access (provider-based)"; roadmap and vision are updated.
- "Remote Access" is the canonical term. The permission namespace is reserved (registered, checked by no route yet): `remote_access.view` (availability on a Device/Ticket), `remote_access.start_attended`, `remote_access.start_unattended`, `remote_access.terminal`, `remote_access.file_transfer`, `remote_access.view_sessions` (all session records and their audit trail) and `remote_access.admin` (providers, credentials, unattended-access policy records). The earlier reserved `remote_support.start` was renamed to `remote_access.start_attended` by forward migration 000039; existing grants carry over to attended start only. No role receives the other permissions automatically; the built-in `platform-administrator` role holds every registered permission by definition. Only the `global` scope exists today, so the unattended-access policy record is required before the feature ships.
- HopToDesk's integration surface (API, deep links, session logs, per-session credentials), licensing and relay hosting must be verified before the feature design is accepted. If it cannot satisfy the unattended constraints, it is used for attended sessions only.
- Remote Access becomes its own business module; it reads Devices, Tickets and Users only through their public contracts.
