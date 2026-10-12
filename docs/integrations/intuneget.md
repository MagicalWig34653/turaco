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

## Verified from source (2026-10-12)

Read from a shallow clone of `ugurkocde/IntuneGet` at commit `1a29a4c2cf4f` (2026-10-12; Next.js app, SQLite or Supabase). The code was read, not run: every authenticated path needs a real Entra app registration, which no available environment has.

- **API authentication is delegated-user only.** Every Bearer endpoint (`POST /api/package`, `GET /api/intune/apps`, ...) calls `parseAccessToken`, which validates the token by calling Microsoft Graph `GET /me`. An application-only token (client credentials) has no `/me`, so it is rejected. A service such as Turaco cannot call the API with an application identity; it would need a signed-in user's delegated token.
- **Webhooks do not carry packaging results.** User webhooks (`/api/webhooks`, HMAC `X-Webhook-Signature`, types slack, teams, discord, custom) deliver only the event `app_updates_available` (apps with newer winget versions). There is no outgoing event for a finished packaging job.
- **Job status is pull-only:** `GET /api/package?jobId=...` and `?userId=...` are unauthenticated reads by id. Their documented fields do not include the installer SHA-256 or publisher signature as a stable contract.
- **Packaging** runs through GitHub Actions or a local Windows packager that claims jobs with a packager key (`/api/packager/jobs`); the callback is HMAC-signed and meant for that pipeline, not for third parties.
- **Rate limits** are in-process per IP or user; error bodies are JSON with `error` or `message`.

Consequence for Turaco: the provider port in `softwaremgmt` cannot be backed by IntuneGet's HTTP API without either (a) storing a user's delegated refresh token in Turaco (rejected: breaks least privilege and MFA), (b) a service-token mode added to IntuneGet upstream, or (c) orchestrating through the Intune state Turaco already observes (the ADR-0027 fallback): operators package with IntuneGet, the published app arrives as a Management Artifact through the normal Intune sync, and Turaco links it to the approved package by name, version and hash. The decision between these is recorded in the product backlog; the Fake provider remains the default.

## Still unverified

- Whether the packaged app's installer hash and publisher signature can be read from a job record in a stable way (the job table schema was not run).
- Entra permissions IntuneGet requests and who holds its credentials in a customer deployment (self-hosted preferred, [ADR-0014](../decisions/ADR-0014-application-level-secret-and-file-encryption.md) for Turaco's own secrets).

## Configuration

`SOFTWARE_PROVIDER_SYNC` (default `false`) allows `POST /api/v1/software/packages/sync` and schedules the worker job `endpoints.software_package_sync` every 15 minutes. Permissions: `software.view`, `software.approve`, `software.package` ([reference](../reference/permissions.md)).
