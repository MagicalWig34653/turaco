# Release Process

1. Merge only changes that pass repository quality and security policy.
2. Ensure generated references are current.
3. Choose a semantic version and create a Git tag `vX.Y.Z`.
4. GitHub Actions builds API, worker and web images.
5. Builds publish to GHCR with semver and commit-derived tags, provenance and SBOM metadata.
6. Record immutable image digests in deployment configuration.
7. Apply database migrations through a deliberate deployment step before/with compatible application rollout.
8. Never edit released migration files or recreate a released tag with different source.

Agent releases will use independent compatibility rules once agent transport becomes implemented.
