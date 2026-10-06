# IntuneGet (Software Management Provider)

**Status:** Internal side only (F9 G1). `backend/internal/integrations/softwaremgmt` defines the provider-agnostic Software Management Provider port ([ADR-0027](../decisions/ADR-0027-software-management-providers.md), [F9 design](../product/f9-software-lifecycle-design.md) decision P1) with normalized records, a deterministic in-memory `Fake` (tests only) and a `NotConfigured` placeholder. No IntuneGet client exists: `turaco-api` and `turaco-worker` wire `NotConfigured`, so catalog search, packaging and publishing answer 409 `endpoints.software_provider_not_configured`, and a package synchronization with `SOFTWARE_PROVIDER_SYNC=true` is audited as `endpoints.software_package_sync.failed` with `provider_not_configured`.

## Port

- `SearchCatalog(ctx, query)` returns `CatalogEntry{ProviderID, Name, Publisher, LatestVersion, SourceURL}`; Turaco drops entries with invalid values and keeps at most 50.
- `Package(ctx, PackageRequest, opKey)` packages exactly the approved binding (product key, name, publisher, version, installer URL and SHA-256, install command, detection rule). Idempotent per operation key (`versionId:installerSha256`).
- `Publish(ctx, providerPackageID, ProviderTarget{ManagementProvider}, opKey)` publishes or updates the package into the Management Provider (Intune). Idempotent per operation key (`packageId:publish:installerSha256`).
- `PackageStatus(ctx, ids)` reports `PackageRecord{ProviderPackageID, ProductKey, Version, InstallerSHA256, InstallerURL, Publisher, InstallCommandSHA256, DetectionRuleSHA256, Status (building|packaged|published|failed), ManagementArtifactExternalID, ObservedAt}`; unknown ids are left out. Product key, version, publisher and the two definition hashes are optional; when reported, Turaco compares them with the approved binding.
- `Publish(ctx, PublishRequest{ProviderPackageID, Target, ExpectedInstallerSHA256}, opKey)` must refuse (`ErrHashDiffers`) when the packaged installer's hash differs from the expected approved hash.

Every provider value is untrusted: Turaco validates ids, hashes (64 lower-case hex, otherwise unknown), statuses (unknown becomes `failed`) and timestamps (future or implausible times become the receive time) before storing them with source and freshness.

## What Turaco relies on

- The provider reports the SHA-256 of the installer it actually packaged; Turaco compares it with the approved hash (again right before publishing) and raises `package_hash_mismatch` when they differ. The provider checks the expected hash once more when publishing.
- Operation keys carry an attempt number (`...:attemptN`); the provider treats a repeated key as the same operation and a new key as a new one.
- Reports carry a meaningful `ObservedAt`; reports older than Turaco's stored observation are ignored.
- A published package becomes an ordinary Management Artifact (an Intune app) that the normal management sync ingests; the package records the artifact's external id and Turaco links it once the artifact exists. Publication never means assignment, installation or deployment success.

## Unverified (needs a real IntuneGet instance)

- Whether IntuneGet exposes an API or webhooks at all, or whether Turaco must orchestrate only through observed Intune state (ADR-0027 consequence). The port shape is Turaco's need, not a verified IntuneGet contract.
- Whether IntuneGet reports the installer hash, publisher signature and the resulting Intune app id, and how package ids are formed.
- Authentication, Entra permissions IntuneGet needs and who controls its credentials (self-hosted preferred; a hosted multi-tenant instance requires a documented customer risk acceptance). Turaco stores only its own credentials as secrets ([ADR-0014](../decisions/ADR-0014-application-level-secret-and-file-encryption.md)).
- Rate limits, paging and error semantics.

## Configuration

`SOFTWARE_PROVIDER_SYNC` (default `false`) allows `POST /api/v1/software/packages/sync` and schedules the worker job `endpoints.software_package_sync` every 15 minutes. Permissions: `software.view`, `software.approve`, `software.package` ([reference](../reference/permissions.md)).
