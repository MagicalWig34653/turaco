# ADR-0005: S3-Compatible Object Storage Abstraction

**Status:** Accepted

Binary attachments are stored outside PostgreSQL through an application abstraction compatible with S3 semantics. Production provider is replaceable; application encrypts sensitive attachment content before storage. Local development uses Adobe S3Mock, not a production object store.
