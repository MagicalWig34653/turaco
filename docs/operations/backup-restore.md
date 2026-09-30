# Backup and Restore

A successful backup job is not enough; restore testing is a product/operations requirement.

## Backup set

- PostgreSQL base backup/dump strategy and WAL as appropriate;
- S3/object data;
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
