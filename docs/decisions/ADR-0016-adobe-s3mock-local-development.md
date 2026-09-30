# ADR-0016: Adobe S3Mock for Local Object-Storage Tests

- Status: Accepted

## Decision

Use Adobe S3Mock for the default local S3-compatible development dependency. Production object storage is accessed through an S3 abstraction and is not coupled to S3Mock.

## Consequences

Local tests do not require cloud credentials and the chosen test service is not a production platform dependency.
