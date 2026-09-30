# LDAP / Active Directory Integration

## Responsibilities

Separate concerns:
- authentication / transparent SSO integration;
- directory synchronization;
- group-to-role mapping;
- computer discovery where useful.

On-prem Platform may connect directly over LDAPS. Hosted Platform can use Connector Agent or customer-approved private connectivity. Do not expose LDAP directly to the public Internet.

## User model

AD/LDAP account maps to ExternalIdentity; User is the stable platform identity. Directory changes update fields whose Source of Truth is directory-owned without overwriting platform-owned history.

## Passwords

LDAP user passwords are never stored. Bind credentials, if needed, are encrypted secrets. Interactive agent authentication must be explicitly capability-scoped and audited without recording user passwords.

## Transparent Windows experience

Kerberos/SPNEGO may provide browser SSO in suitable domain environments, either directly or through a dedicated identity layer. The platform consumes a trusted authenticated identity; it does not make anonymous identity guesses.
