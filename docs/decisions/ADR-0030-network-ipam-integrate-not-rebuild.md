# ADR-0030: Network/IPAM is integrated, not rebuilt

- Status: Accepted by default (2026-10-03, with the F7 design; revisit when a NetBox instance or a concrete IPAM need exists)

## Context

The implementation plan asks to decide between a native Network/IPAM model (VRF, VLAN, Prefix, IP address, Interface) and a NetBox integration. Turaco's strength is the operational context (assets, services, changes, tickets), not address management. [ADR-0019](ADR-0019-open-source-first.md) prefers existing open-source components over rebuilding them. No NetBox instance, credentials or concrete IPAM workflow exist today.

## Decision

- Turaco does **not** build a native IPAM (no VRF, VLAN, Prefix or IP allocation model) in F7.
- Where infrastructure records need network facts today, they carry plain attributes: a VM or an infrastructure Asset may have `management_address` (text, validated as IP or host name) and a free `network_note`. These are descriptive, not an address plan.
- A future NetBox integration is a read-oriented provider port `integrations/netbox` owned by a Network module; it would map prefixes, IP addresses and interfaces as observed external data with source and freshness (like the Intune adapter). That requires its own design and, if it introduces a write path, a new ADR.
- Interfaces and cabling are out of scope until such an integration exists.

## Consequences

- No duplicate source of truth for addresses; F7 stays small.
- Impact analysis works on Assets, VMs, Services and Sites, not on subnets.
- Teams that want address management use NetBox (or any IPAM) independently; Turaco links by `management_address`.
