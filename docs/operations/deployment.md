# Deployment

## Supported target shapes

Primary: Linux containers. Docker Swarm is a supported/preferred orchestrator for the initial on-prem design. Docker Compose is supported for small/single-node environments. Native Windows services may be provided for components where customers require Windows Server support; Windows containers are not a baseline requirement.

## Stateless compute

API/worker nodes have no required local persistent files. Persistent state is PostgreSQL, S3-compatible storage and external key/secrets material.

## Swarm guidance

Treat Swarm primarily as compute scheduling. Do not assume local Docker volumes are distributed storage. Database/object data require an explicit stateful design. Multi-site compute does not automatically provide multi-site database/storage HA.

For two-site deployments, prefer clear primary + DR semantics for state rather than pretending to have symmetrical active/active persistence. Swarm manager quorum and database/storage RPO/RTO are separate designs.

## Hosted design

Managed-service environments use isolated customer DB/object/key data planes. Compute may be shared or dedicated by tier, but authorization and data storage cannot depend solely on a forgotten `tenant_id` filter.

## Releases

GHCR images are built by GitHub Actions. Production records immutable digests. `latest` may exist for convenience but is not deployment state.
