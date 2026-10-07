# Remote Access Provider Threat Models

**Status:** Planned (F10 slice R-C). Each provider gets its own section before its connector goes beyond building launch links ([ADR-0026](../decisions/ADR-0026-remote-access-providers.md), [F10 design](../product/f10-remote-access-design.md)). Nothing in this file is verified yet.

## Template per provider

1. Components and trust boundaries: provider client on the endpoint (privileges, auto-update, signing), relay and ID servers (vendor-hosted or self-hosted), provider account model, Turaco connector (credentials, API scope).
2. Assets and attackers: employee endpoint, technician workstation, provider account, relay.
3. Threats and mitigations: stolen peer ids, unattended standing access left enabled, client impersonation, relay interception, API credential theft, launch-link tampering, silent connections without consent, session records tampered or missing.
4. What Turaco enforces regardless: attended mode only, user consent, ticket context, one-time launch handle, audit, notification to the device's holder.
5. Residual risks and the deployment decisions an operator must record.

## Providers

- RustDesk (self-hosted `hbbs`/`hbbr`): to be written.
- AnyDesk (vendor cloud or On-Premises, REST API): to be written.
- HopToDesk: to be written.
