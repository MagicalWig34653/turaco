# Security Architecture

**Status:** Baseline design requirements

## Trust boundaries

1. Browser ↔ Platform API over HTTPS.
2. Platform ↔ PostgreSQL/Object Storage over protected internal transport.
3. Hosted Platform ↔ Connector Agent using outbound authenticated encrypted channel (mTLS preferred).
4. Hosted Platform ↔ Endpoint Agent using a separate authenticated management protocol.
5. Tenant A data/security material is isolated from Tenant B.

## Authorization

Backend authorization uses Permission + Scope. Frontend hiding is never authorization. Implementation status: the `authorization` platform package enforces Permission checks per route and is default-deny while no `Authenticator` is configured; Scope evaluation is not implemented yet, so current permissions are global. Listing Directory Group members requires both `organization.directory.view` and `organization.view` because members are user identities. High-impact actions (wipe, broad deployment, remote support, privileged config) require dedicated permissions and policy gates.

## Identity

Platform User is separate from External Identity. Initial AD/LDAP and transparent Windows auth can coexist with future OIDC/Entra without re-keying business history.

## Agents

Agents use unique cryptographic identities and explicit local/cloud capabilities. The ordinary protocol is typed operations, not arbitrary shell. Commands are expiring, idempotent and audited.

## Data protection

- TLS for network transport.
- application-level encryption for stored secrets.
- application encryption of attachments before object storage.
- disk/provider encryption is defense in depth, not the only control.
- passwords are not reversibly stored; local emergency credentials use modern password hashing.

## Audit/logging

Audit records are separate from debug logs and protected against normal mutation. Logs use IDs/structured context and redact secrets/auth headers/content not needed for operation.

## Supply chain

GitHub Actions builds from reviewed commits, dependencies are monitored, images are published to GHCR with provenance/SBOM where supported, and production deployments record immutable image digests.
