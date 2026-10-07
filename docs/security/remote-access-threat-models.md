# Remote Access Provider Threat Models

**Status:** Written (F10 slice R-C, design-level, 2026-10-07). Each provider has its own section; connectors stay launch-only until an operator has recorded the decisions below ([ADR-0026](../decisions/ADR-0026-remote-access-providers.md), [F10 design](../product/f10-remote-access-design.md)). Nothing here was tested against real provider clients, except that a RustDesk `hbbs`/`hbbr` container starts. Launch-link verification state is listed per provider.

## Template per provider

1. Components and trust boundaries: provider client on the endpoint (privileges, auto-update, signing), relay and ID servers (vendor-hosted or self-hosted), provider account model, Turaco connector (credentials, API scope).
2. Assets and attackers: employee endpoint, technician workstation, provider account, relay.
3. Threats and mitigations: stolen peer ids, unattended standing access left enabled, client impersonation, relay interception, API credential theft, launch-link tampering, silent connections without consent, session records tampered or missing.
4. What Turaco enforces regardless: attended mode only, user consent, ticket context, one-time launch handle, audit, notification to the device's holder.
5. Residual risks and the deployment decisions an operator must record.

## Common to all providers

- **Turaco stores:** the provider key, the Device-to-peer mapping (peer id, history), the session record (initiator, Ticket, consent, times, reason codes) and, only if an API connector exists, validated provider records with source and observation time. A launch handle is stored as a SHA-256 of a one-time 60 s token.
- **Turaco never stores or logs:** the peer password, any provider credential in a link, or the provider launch link itself. The link exists only in the exchange response to the initiating technician.
- **Launch-link injection:** peer ids are validated by a per-provider allow-list (ASCII letters, digits and a few separators; no whitespace, quotes, slashes, colons or query characters) and placed into a fixed template, so an id cannot add parameters, paths or another scheme. The UI additionally refuses links without a custom scheme.
- **Attended only:** unattended or standing access is outside F10. Turaco cannot see or disable it on the endpoint, so it is a deployment decision (below), not an enforced control.
- **Consent:** the technician records the end user's consent; the Device's holder is notified when a session starts. Turaco cannot verify that the on-screen consent prompt of the provider client was answered by the user.
- **Observation limits:** Observed provider records are advisory. They never create, end or authorize a session and are matched only by peer id and launch window. Where no API exists there is no observation at all.

## Providers

### RustDesk (self-hosted `hbbs`/`hbbr`)

- **Boundaries:** RustDesk client on the endpoint (user or service context, own updater) and on the technician workstation; operator-run ID/rendezvous (`hbbs`) and relay (`hbbr`) servers; Turaco only builds a link. The open-source server has no API, so Turaco has no provider credential and no observation. Server Pro (console, audit log) is not integrated.
- **Link:** `rustdesk://connection/new/<id>`, verified in the client source (a legacy form kept for compatibility; `rustdesk://<id>` is the current one). The client also accepts a `password` query parameter; Turaco never sets it.
- **Threats:** peer ids are enumerable on a reachable server; custom ids are guessable; a client configured with a permanent password and unattended access accepts a technician without consent; an unpinned server key lets a client talk to a rogue server; relay traffic is end-to-end encrypted but metadata is visible to the operator.
- **Operator decisions:** pin the server address and public key in managed clients; disable permanent passwords and unattended access by client policy; restrict the server to the corporate network or VPN; record the pinned client version and update channel.
- **Residual:** no session record in Turaco beyond the technician's own entries; consent is not machine-verified; rotation of the server key requires redeploying client configuration.

### AnyDesk (vendor cloud or On-Premises, REST API)

- **Boundaries:** AnyDesk client on both sides; vendor cloud or On-Premises relay; AnyDesk account/license; a possible Turaco API connector holds the License ID and API password as a deployment secret (read from file), scoped to read session history and, later, close sessions.
- **Link:** `anydesk:<id>` is **not verified**. The official CLI documents only `anydesk.exe <ID/Alias>`; the URL handler is undocumented there. The connector is launch-only and must be checked against a real client before enabling.
- **Threats:** alias hijack or typosquatting inside a namespace; an "unattended access" password or an ACL that allows connections without consent; vendor account takeover exposing session history and the API credential; session history that lags or is edited by account admins; link handler registration by other software on the technician workstation.
- **Operator decisions:** disable unattended access and the permanent password by client policy; require interactive accept; limit AnyDesk account admins and enable the vendor's own audit; store the API credential per ADR-0014 with least scope; decide whether history is imported (sessions are never created from it).
- **Residual:** vendor trust (cloud) or operator trust (On-Premises); history is advisory; close-session via API is not used.

### HopToDesk

- **Boundaries:** HopToDesk client (open-source, RustDesk-derived) on both sides; vendor cloud or self-hosted relay; no public REST API was found, so Turaco has no provider credential and no observation.
- **Link:** `hoptodesk://<id>` is **not verified**; no public documentation or reachable source confirmed it. Treat the connector as experimental until checked against a real client.
- **Threats:** as RustDesk (id enumeration, permanent password, unattended access, rogue relay), plus a smaller project and unclear update/signing practice, which makes client provenance an operator responsibility.
- **Operator decisions:** pin and sign-check the client version, disable unattended access and permanent passwords, decide cloud versus self-hosted relay, and record that no provider-side audit is available to Turaco.
- **Residual:** the weakest integration surface of the three; no observation, unverified link format, consent not machine-verified.
