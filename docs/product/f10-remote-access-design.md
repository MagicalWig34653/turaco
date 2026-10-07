# F10 Remote Access — Feature Design

**Status:** Draft 2026-10-07; decisions R1–R7 adopted by default (autonomous progress; revisit with real provider accounts). Target design; [current status](current-status.md) is authoritative for what is implemented. Related: [ADR-0026](../decisions/ADR-0026-remote-access-providers.md) (decision and security constraints), [state machines](../domain/state-machines.md#remote-access-session-planned), [agent boundaries](../security/agent-boundaries.md), [F6 design](f6-endpoint-intelligence-design.md), [F5 design](f5-service-desk-design.md), [integration sandboxes](../development/integration-sandboxes.md).

## What the providers offer (researched 2026-10-07, *verify before building a connector*)

| Provider | Client launch | Server-side API for Turaco | Hosting | Notes |
| --- | --- | --- | --- | --- |
| RustDesk | `rustdesk://` link / command line with the peer ID | No API in the open-source server (`hbbs`/`hbbr`). The commercial Server Pro adds a web console, API access, address book and audit logs (connection, file, console, alarm) | Self-hosted (open source), fits ADR-0019 | Community consoles exist for the OSS server (unverified, not used) |
| AnyDesk | `anydesk:<id>` link / command line | REST API (License ID + API password, request via support) on Standard/Advanced/Ultimate/On-Premises licenses: sessions (history, detail, comment, close), clients (list, details, online state, remove), address books (list) | Vendor cloud or On-Premises (paid) | Proprietary; use is a recorded deployment decision |
| HopToDesk | `hoptodesk://` link / command line | No public REST API found; Dashboard Pro offers device management in its web UI | Vendor cloud or self-hosted relay | Integration surface to be verified with the vendor |

Consequence for the port: **launching** (a link or command that opens the provider client on the technician's machine for a known peer id) works for all three; **provider records** (what actually happened) and **device lists** only where an API exists (AnyDesk REST; RustDesk Pro). Unattended mode with a single-use, expiring credential issued by Turaco is not offered by any of them today, so **F10 ships attended mode only**; unattended stays blocked by the ADR-0026 constraints until a provider can meet them.

## Decisions

- **R1 Attended only.** Sessions are attended with user consent in the provider client. No unattended mode, no terminal/file-transfer permissions wired (permissions stay reserved), no typed remote actions in F10.
- **R2 New module `remoteaccess`** (schema `remoteaccess`) owns Remote Access Sessions, provider configuration records and Device↔provider peer mappings (stored as platform external references of the Device obtained through `endpoints/public`; matched only by immutable provider/management ids or an explicit, audited manual mapping — never by hostname). Providers are connectors in `integrations/remoteaccess/<provider>` behind one port.
- **R3 Port (capabilities, not a plugin framework):** `LaunchTarget(ctx, peer ProviderPeer, mode) (Launch, error)` (builds a provider link/command for the authorized technician; **no secrets**, the handle is a Turaco one-time token resolved server-side into the provider launch URI at the moment of use), `Capabilities()`, optional `Peers(ctx, ...)` and `Sessions(ctx, since)` (provider observations with source and observed time) and optional `Close(ctx, providerSessionID)`. Providers without an API implement only launch and report `observations: unsupported`; Turaco then shows provider facts as unknown, never inferred.
- **R4 Launch handle.** `POST /remote-access/sessions/{id}/launch` returns a one-time, short-lived (60 s), user-bound handle exactly once; Turaco never stores or logs the provider URI or any credential; a second call returns 409. The browser is given the provider URI (custom scheme) in the response body only, never in a redirect or URL that logs capture.
- **R5 Policy and consent.** Starting a session needs `remote_access.start_attended`, a linked Ticket (required in F10; the Ticket's affected user must be the Device's current holder, or the technician states a reason code), the Device must have a mapped peer and a fresh observation, and recent re-authentication (step-up) where the authentication layer supports it (if not, documented gap). Consent is a separate field `granted | declined | not_required | unknown`; attended default is `unknown` until the technician records the user's consent or a provider record shows it; declining ends the session. High-risk devices (policy attribute) can require a second approver through an Approval (`pending_approval`).
- **R6 Observed ≠ authorized.** Provider-observed connect/disconnect/duration are separate fields with source and observed time; a session is closed by the technician, by expiry (no launch within 15 min), or by a provider record; Turaco never infers an end without data.
- **R7 Slicing.** R-A core (module, lifecycle, policy, launch handles, Fake + launch-URI connectors for the three providers, audit, events, permissions) → R-B UI and Ticket/Device integration (button, session list, consent recording, notes, follow-up Tasks, briefing entry) → R-C threat models, observation import (Fake and, where an API exists, connector code written against the documented API but unverified), RustDesk self-host in the lab with a real launch test, reviews.

## State machine (implemented states)

`requested → pending_approval? → authorized → launched → closed`, or `rejected`, `cancelled`, `expired` (not launched in time), `failed`. Operations: `Request`, `Approve`/`Reject` (Approval consumer), `Authorize` (policy evaluation, automatic when no approval needed), `Launch` (one-time handle), `RecordConsent`, `Close(reason)`, `Cancel(reason)`, `ImportObservation` (system). Every transition audited with ids and reason codes only, append-only transitions table.

## Permissions

`remote_access.view`, `remote_access.start_attended` (elevated), `remote_access.view_sessions`, `remote_access.admin` (providers, mappings; elevated). Others stay reserved.

## Security

Threat model per provider before its connector goes beyond launch-URI building ([threat models](../security/remote-access-threat-models.md), written in slice R-C). Provider API credentials are deployment secrets (ADR-0014), read from files, never sent to the browser. Peer ids and provider URIs are untrusted input (strict validation per provider format; no shell metacharacters; URIs built from validated components only). Session notes are plain text (safetext), never in audit. Employees see only their own sessions as a notice ("a technician connected on <date>", via notification) and can never start sessions.

## Not in F10

Unattended access, terminal, file transfer, session recording, typed remote actions (restart, diagnostics), a native transport, provider-side provisioning of peers, Teams chat links.
