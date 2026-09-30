# ADR-0015: GitHub Actions and GHCR

- Status: Accepted

## Decision

Use GitHub Actions as the canonical CI/release engine and GHCR as the canonical container registry for platform builds.

## Consequences

Source, review, CI and released images share a traceable GitHub workflow. On-prem deployments remain able to mirror images into another registry.
