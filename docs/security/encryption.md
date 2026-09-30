# Encryption and Key Model

## Goals

Protect data from stolen disks/backups/object-store access and reduce blast radius between tenants while keeping searchable operational fields usable.

## Transport

- Browser/API: TLS.
- API/DB: TLS in production where network boundary warrants it.
- API/S3: TLS in production.
- Agents: mutually authenticated encrypted channel; mTLS is the preferred initial design.

## Database

Do not encrypt every searchable operational column at application level: hostname, OS, model, IP and software inventory must remain queryable under normal DB/disk/tenant controls.

Application-level encryption is mandatory for secrets such as:
- LDAP bind credentials;
- API/OAuth tokens;
- SMTP/Autotask credentials;
- webhook secrets;
- private keys;
- recovery secrets where later stored.

Encryption happens before PostgreSQL. DB cryptographic functions are not the primary secrets boundary.

## Attachments

Preferred envelope encryption:
1. generate random per-object 256-bit Data Encryption Key (DEK);
2. encrypt object with an authenticated cipher such as AES-256-GCM;
3. store only ciphertext under an opaque object key;
4. wrap DEK with tenant Key Encryption Key (KEK);
5. store wrapped DEK + nonce/algorithm/version in metadata; filename/mime business metadata remain in protected DB.

Tenant KEKs are separated from encrypted data through a KeyProvider abstraction. Rotation should re-wrap DEKs without re-encrypting large objects where possible.

## Key providers

Development may use a local provider with clearly non-production keys. Production may use OpenBao/Vault/HSM/cloud KMS or another approved provider. The application depends on a small wrapping/unwrapping interface, not vendor SDK semantics throughout the codebase.

## Backups

Database/WAL/object backups must be encrypted and keys backed up through a separate protected process. Restore tests must include key recovery; an encrypted backup without recoverable keys is not a backup.
