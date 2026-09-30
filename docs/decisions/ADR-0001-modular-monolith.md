# ADR-0001: Modular Monolith First

**Status:** Accepted

## Context
The platform spans many domains but initial scale does not justify distributed-service complexity.

## Decision
Implement one modular Go application with explicit domain ownership, public contracts and events. No direct cross-module repository/table access. Extract services only for measured scaling/security/availability/runtime/network reasons.

## Consequences
Simple deployment/transactions/debugging; architecture tests must defend boundaries.
