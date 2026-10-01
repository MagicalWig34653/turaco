// Package kerberos validates Kerberos/SPNEGO tickets for Windows integrated
// sign-in (F1 slice 4). It uses gokrb5 (ADR-0023) with the service keytab and
// returns only the authenticated principal name; no gokrb5 type leaves this
// package. The principal is mapped to a synced directory account by
// platform/authentication, so Kerberos never creates Users.
//
// Security properties, see docs/security/identity-access-design.md section 8:
//
//   - the keytab is read once at startup and must contain a key for the
//     configured service principal in the configured realm; key material is
//     never logged or put into errors;
//   - tickets are decrypted with the key of the configured service principal
//     only, so a ticket for any other service is refused;
//   - the client realm must equal the configured realm, and the client
//     principal must be a single-component name (user/admin-style instance
//     names are refused) without realm, path or domain separators, because
//     the name is used as a directory login identifier;
//   - clock skew and gokrb5's replay cache apply; PAC data is not decoded
//     (group membership comes from the synchronized directory, not from the
//     ticket);
//   - every ticket problem is the sentinel authentication.ErrInvalidTicket
//     with a fixed reason; errors and logs never contain ticket contents,
//     principal names or gokrb5 diagnostic text;
//   - malformed input cannot crash the process: parser panics are recovered
//     and answered like any other invalid ticket.
package kerberos
