package ldap

import (
	"fmt"
	"sort"
	"strings"

	goldap "github.com/go-ldap/ldap/v3"
)

// normalizeDN returns a comparison key for a distinguished name. Two DNs that
// differ only in letter case, in whitespace around separators, in the order of
// the attributes of a multi-valued RDN, or in LDAP escaping produce the same
// key. A string that is not a parseable DN falls back to a case- and
// whitespace-folded form of itself, so identical strings still match each
// other.
func normalizeDN(s string) string {
	dn, err := goldap.ParseDN(s)
	if err != nil || len(dn.RDNs) == 0 {
		return "raw:" + foldSpace(s)
	}
	rdns := make([]string, 0, len(dn.RDNs))
	for _, rdn := range dn.RDNs {
		parts := make([]string, 0, len(rdn.Attributes))
		for _, a := range rdn.Attributes {
			parts = append(parts, fmt.Sprintf("%s=%q", strings.ToLower(strings.TrimSpace(a.Type)), foldSpace(a.Value)))
		}
		sort.Strings(parts)
		rdns = append(rdns, strings.Join(parts, "+"))
	}
	return "dn:" + strings.Join(rdns, ",")
}

// foldSpace lowercases s, trims it and collapses internal whitespace runs.
func foldSpace(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}
