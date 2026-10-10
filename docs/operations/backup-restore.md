# Backup and Restore

A successful backup job is not enough; restore testing is a product/operations requirement.

## Backup set

- PostgreSQL base backup/dump strategy and WAL as appropriate;
- attachment storage (`STORAGE_PATH` directory or the S3 bucket, [ADR-0037](../decisions/ADR-0037-file-storage-and-attachments.md)), taken together with the PostgreSQL backup because the database holds the attachment metadata;
- the attachment master key (`STORAGE_MASTER_KEY_FILE`), stored separately from the data: without it attachments cannot be decrypted;
- encrypted key metadata and recoverable tenant KEKs via separate protected procedure;
- deployment/configuration required to reconstruct the environment.

## Requirements

- encryption before/off-site storage as appropriate;
- retention by data class;
- cross-failure-domain/off-site copy for production;
- monitored success/failure;
- periodic restore into an isolated environment;
- recorded last successful restore test.

## Restore order

Document and test: key provider/keys → PostgreSQL → object storage → application/worker → integrations/agents. Never assume encrypted attachments are recoverable until the key path has been restored and validated.
