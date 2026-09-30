# ADR-0007: Isolated Customer Data Planes

**Status:** Accepted direction

For managed-service hosting, prefer separate customer database and object/key namespace (and optionally compute) over a single shared operational database protected only by `tenant_id`. A future control plane manages instances/health/version/backups without routinely storing business data.
