package ldap

import (
	"sort"
	"strconv"
	"strings"

	goldap "github.com/go-ldap/ldap/v3"
)

// normalizeDN returns a comparison key for a distinguished name, following the
// equality rules of directory servers for RDN values instead of free string
// folding:
//
//   - the DN is parsed with RFC 4514 rules, so escaping and the insignificant
//     (unescaped) spaces around separators are handled by the parser;
//   - attribute types are compared case-insensitively (ASCII only);
//   - values are compared byte-wise after ASCII-only case folding (A-Z to
//     a-z). Whitespace inside a value is never collapsed, an escaped leading
//     or trailing space is significant, and non-ASCII letters are not case
//     folded (so "CN=K" with the Kelvin sign U+212A differs from "CN=k");
//   - the attributes of a multi-valued RDN are sorted.
//
// ok is false for a string that is not a parseable, non-empty DN. There is
// deliberately no fallback to a folded form of the raw string: a reference
// that cannot be parsed is unresolved.
func normalizeDN(s string) (key string, ok bool) {
	dn, err := goldap.ParseDN(s)
	if err != nil || dn == nil || len(dn.RDNs) == 0 {
		return "", false
	}
	var b strings.Builder
	for i, rdn := range dn.RDNs {
		if len(rdn.Attributes) == 0 {
			return "", false
		}
		parts := make([]string, 0, len(rdn.Attributes))
		for _, a := range rdn.Attributes {
			if a.Type == "" {
				return "", false
			}
			// strconv.Quote makes the encoding unambiguous: a value can never
			// contain a separator that is read as a structural character.
			parts = append(parts, asciiLower(a.Type)+"="+strconv.Quote(asciiLower(a.Value)))
		}
		sort.Strings(parts)
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strings.Join(parts, "+"))
	}
	return b.String(), true
}

// asciiLower maps the bytes A-Z to a-z and leaves every other byte unchanged.
// Unlike strings.ToLower it never touches multi-byte characters.
func asciiLower(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; 'A' <= c && c <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if 'A' <= b[j] && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

// dnTarget is the snapshot object a DN reference resolves to.
type dnTarget struct {
	id      string
	isGroup bool
}

// dnIndex resolves DN references (manager and member values) to snapshot
// objects. Users and groups share one index because a reference may name
// either. Resolution tries the exact DN string first and then the normalized
// key. A key that more than one entry maps to is ambiguous and never
// resolves: a wrong match could attach a person to a foreign manager or group.
type dnIndex struct {
	exact map[string][]dnTarget
	norm  map[string][]dnTarget
}

func newDNIndex(capacity int) *dnIndex {
	return &dnIndex{
		exact: make(map[string][]dnTarget, capacity),
		norm:  make(map[string][]dnTarget, capacity),
	}
}

// add registers an entry. An entry whose own DN cannot be parsed is reachable
// by its exact DN string only.
func (x *dnIndex) add(dn string, t dnTarget) {
	x.exact[dn] = append(x.exact[dn], t)
	if key, ok := normalizeDN(dn); ok {
		x.norm[key] = append(x.norm[key], t)
	}
}

// ambiguousKeys returns the number of normalized keys shared by more than one
// entry, and the number of exact DN strings shared by more than one entry.
func (x *dnIndex) ambiguousKeys() int {
	n := 0
	for _, targets := range x.norm {
		if len(targets) > 1 {
			n++
		}
	}
	for dn, targets := range x.exact {
		if len(targets) > 1 {
			// An exact duplicate is normally also a normalized duplicate; only
			// count it separately when its DN is not parseable.
			if _, ok := normalizeDN(dn); !ok {
				n++
			}
		}
	}
	return n
}

// resolve returns the unique entry a reference names. It reports false for an
// unknown, unparseable or ambiguous reference.
func (x *dnIndex) resolve(ref string) (dnTarget, bool) {
	if invalidText(ref) {
		return dnTarget{}, false
	}
	if targets, found := x.exact[ref]; found {
		if len(targets) == 1 {
			return targets[0], true
		}
		return dnTarget{}, false
	}
	key, ok := normalizeDN(ref)
	if !ok {
		return dnTarget{}, false
	}
	if targets := x.norm[key]; len(targets) == 1 {
		return targets[0], true
	}
	return dnTarget{}, false
}

// referenceKey identifies a reference for counting distinct unresolved
// values: the normalized key, or the raw string when it is not a DN.
func referenceKey(ref string) string {
	if invalidText(ref) {
		return "raw:" + ref
	}
	if key, ok := normalizeDN(ref); ok {
		return "dn:" + key
	}
	return "raw:" + ref
}
