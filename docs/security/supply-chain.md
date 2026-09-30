# Software Supply Chain

## Objectives

Every released artifact must be traceable to source and built through reviewable automation.

## Baseline

- GitHub is the canonical source repository.
- Pull requests must pass CI before merge.
- Containers are built by GitHub Actions and published to GHCR.
- Production deployment records immutable image digests rather than relying on `latest`.
- Container builds request BuildKit provenance and SBOM attestations.
- CodeQL, Dependabot and filesystem vulnerability scanning are enabled by repository workflows.
- Go and npm lockfiles are committed after the first connected bootstrap.

## GitHub Actions dependencies

The bootstrap references immutable upstream *release lines* (`@v7`, `@v4`, etc.) so Dependabot can maintain them. Before a production-security baseline is declared, third-party Actions should be pinned to reviewed full commit SHAs and updated through automated pull requests. This is intentionally a release-hardening task rather than hidden bootstrap complexity.

## Dependency rule

A new runtime dependency requires justification under `docs/development/definition-of-done.md`. Prefer the standard library and existing dependencies.

## Release inputs

A release is derived from:

1. an immutable Git commit,
2. a signed/reviewed Git tag according to organization policy,
3. a successful CI/security state,
4. the GitHub Actions container build.

Do not rebuild an existing version tag with different source.
