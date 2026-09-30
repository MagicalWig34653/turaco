# ADR-0014: Application-Level Secret and File Encryption

- Status: Accepted

## Decision

Persisted credentials/secrets are encrypted by the application before PostgreSQL. File contents are encrypted before object storage using per-object data keys wrapped by tenant-level key material. Transport encryption and volume encryption remain additional layers.

## Consequences

Database/object-store compromise alone does not disclose high-value plaintext. Key management remains an explicit operational dependency and must support rotation.
