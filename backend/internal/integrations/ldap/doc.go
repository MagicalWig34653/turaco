// Package ldap is the LDAP/Active Directory adapter for directory
// synchronization (F1 slice 3). It speaks LDAP through go-ldap (ADR-0022) and
// returns the vendor-neutral organization/public.DirectorySnapshot; no go-ldap
// type leaves this package.
//
// Security properties, see docs/integrations/ldap-ad-sync-design.md sections
// 12 and 13:
//
//   - certificate verification is always on (LDAPS or StartTLS), with an
//     optional additional CA file;
//   - only an explicit attribute allowlist is requested, never "*" or
//     credential attributes;
//   - errors and logs never contain the bind password, distinguished names,
//     filter values, attribute values or server diagnostic messages;
//   - a snapshot is complete or the fetch fails: partial results are never
//     returned, because absence from a snapshot marks objects as not observed;
//   - resource limits (packet size, entry counts, value length, server time
//     limit) bound what a misconfigured directory can make the adapter do;
//   - DN references resolve only to a unique entry (exact match first, then
//     directory equality semantics without Unicode or whitespace folding);
//   - plain ldap:// needs an explicit opt-in, and the bind password is held in
//     a type that redacts itself when formatted or logged.
package ldap
