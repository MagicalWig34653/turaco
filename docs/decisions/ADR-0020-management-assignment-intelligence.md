# ADR-0020 — Provider-Agnostic Management Assignment Intelligence

**Status:** Accepted. Extended by [ADR-0027](ADR-0027-software-management-providers.md) (Desired State and Deployment Rings for provider-published software); the three dimensions here are unchanged.

## Context

Endpoint administrators need to understand which Intune applications, configuration profiles, compliance policies, endpoint-security policies and related artifacts target a Group/User/Device, why they target it, and what Intune actually observed. A direct mirror of Microsoft Graph DTOs would couple Turaco's domain/UI to Intune and would not cleanly support future management providers.

## Decision

Turaco will normalize provider-managed assignable objects into canonical **Management Artifact, Management Assignment, Management Filter, Management Applicability and Management Observation** concepts owned primarily by the Endpoint domain.

Turaco will keep three dimensions distinct:

1. configured **Assigned** intent;
2. Turaco-computed **Expected Applicable** evaluation with confidence/reason;
3. provider **Observed** result with source/freshness.

An explainable `AssignmentPath` is a read model derived from normalized assignments, known Directory Group membership, exclusions, filters and observations. It is not authoritative business storage.

Intune remains the first Management Provider. Intune-specific fields/status values may be retained at the adapter/detail boundary; they must not become the universal domain model.

## Consequences

- Device, User, Group and Management Artifact pages can share a consistent assignment/explainability model.
- Future management providers can participate without redesigning the main UI/domain.
- Turaco must not claim that locally calculated applicability is authoritative provider execution state.
- Sync/history storage must preserve source, freshness and meaningful revisions.
- Assignment evaluation needs deterministic reason codes and explicit `unknown` handling.
- Software-specific views may project Management Assignments but must not create a second conflicting provider-assignment truth.
