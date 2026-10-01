package ldap

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
)

func mustKey(t *testing.T, dn string) string {
	t.Helper()
	key, ok := normalizeDN(dn)
	if !ok {
		t.Fatalf("normalizeDN(%q) not ok", dn)
	}
	return key
}

func TestNormalizeDNEqualSemantics(t *testing.T) {
	same := [][2]string{
		// ASCII case of types and values.
		{"CN=Ann,OU=Users,DC=Example,DC=Test", "cn=ann,ou=users,dc=example,dc=test"},
		// Insignificant (unescaped) spaces around separators and "=".
		{"CN=Ann , OU=Users ,DC=Example, DC=Test", "CN=Ann,OU=Users,DC=Example,DC=Test"},
		{"CN = Ann,DC=x", "CN=Ann,DC=x"},
		// Escaping of separators.
		{"CN=Lee\\, Ann,DC=x", "cn=lee\\, ann,dc=x"},
		{"CN=Lee\\2c Ann,DC=x", "CN=Lee\\, Ann,DC=x"},
		// Order of a multi-valued RDN.
		{"cn=a+sn=b,dc=x", "SN=B+CN=A,DC=X"},
		// Escaped leading/trailing spaces are significant but identical here.
		{"CN=\\ kim,DC=x", "cn=\\ KIM,dc=x"},
	}
	for _, p := range same {
		if mustKey(t, p[0]) != mustKey(t, p[1]) {
			t.Errorf("normalizeDN(%q) != normalizeDN(%q)", p[0], p[1])
		}
	}
	different := [][2]string{
		{"CN=Ann,DC=x", "CN=Ann,DC=y"},
		{"CN=Lee\\, Ann,DC=x", "CN=Lee,CN=Ann,DC=x"},
		{"CN=Ann,OU=A,DC=x", "CN=Ann,DC=x"},
		// Internal whitespace is significant.
		{"CN=John Smith,DC=x", "CN=John  Smith,DC=x"},
		{"CN=John Smith,DC=x", "CN=JohnSmith,DC=x"},
		// Escaped leading/trailing spaces are significant.
		{"CN=kim,DC=x", "CN=kim\\ ,DC=x"},
		{"CN=kim,DC=x", "CN=\\ kim,DC=x"},
		{"CN=kim\\ ,DC=x", "CN=\\ kim,DC=x"},
		// No Unicode case folding: U+212A KELVIN SIGN is not "k".
		{"CN=kim,DC=x", "CN=Kim,DC=x"},
		{"CN=Éric,DC=x", "CN=éric,DC=x"},
		// A different attribute type.
		{"CN=a,DC=x", "UID=a,DC=x"},
	}
	for _, p := range different {
		if mustKey(t, p[0]) == mustKey(t, p[1]) {
			t.Errorf("normalizeDN(%q) == normalizeDN(%q)", p[0], p[1])
		}
	}
}

func TestNormalizeDNRejectsUnparseableReferences(t *testing.T) {
	for _, s := range []string{"", "   ", "not a dn", "NOT  A DN", "=x,DC=y", "CN=a,", "CN=a\\"} {
		if key, ok := normalizeDN(s); ok {
			t.Errorf("normalizeDN(%q) = %q, want not ok", s, key)
		}
	}
}

func TestAsciiLowerOnlyFoldsASCII(t *testing.T) {
	if got := asciiLower("ABC xyz KÉ"); got != "abc xyz KÉ" {
		t.Errorf("asciiLower = %q", got)
	}
	if got := asciiLower("already lower"); got != "already lower" {
		t.Errorf("asciiLower = %q", got)
	}
}

// resolveManager maps a directory of one boss and one report and returns the
// report's manager state.
func resolveManager(t *testing.T, bossDNs []string, ref string) (*string, bool) {
	t.Helper()
	sch := mustSchema(t, config.DirectoryTypeActiveDirectory)
	var users []*goldap.Entry
	for i, dn := range bossDNs {
		users = append(users, adUser(t, dn, testID(i+1), "boss"+string(rune('a'+i)), "512"))
	}
	users = append(users, adUserEntry(t, "CN=Report,OU=Users,DC=x", testID(99), map[string][]string{
		attrSAMAccount: {"report"}, attrAccountCtl: {"512"}, attrManager: {ref},
	}))
	snap, err := mapSnapshot(sch, users, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range snap.Users {
		if u.Username == "report" {
			return u.ManagerExternalID, u.ManagerUnresolved
		}
	}
	t.Fatal("report not in snapshot")
	return nil, false
}

func TestManagerResolutionDoesNotCrossDifferingValues(t *testing.T) {
	const boss = "CN=John Smith,OU=Users,DC=x"
	for _, tt := range []struct {
		name, ref string
	}{
		{"collapsed whitespace", "CN=John  Smith,OU=Users,DC=x"},
		{"no space", "CN=JohnSmith,OU=Users,DC=x"},
		{"escaped trailing space", "CN=John Smith\\ ,OU=Users,DC=x"},
		{"escaped leading space", "CN=\\ John Smith,OU=Users,DC=x"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id, unresolved := resolveManager(t, []string{boss}, tt.ref)
			if id != nil || !unresolved {
				t.Errorf("manager = %v unresolved=%v, want unresolved", id, unresolved)
			}
		})
	}
	t.Run("kelvin sign", func(t *testing.T) {
		id, unresolved := resolveManager(t, []string{"CN=kim,OU=Users,DC=x"}, "CN=Kim,OU=Users,DC=x")
		if id != nil || !unresolved {
			t.Errorf("manager = %v unresolved=%v, want unresolved", id, unresolved)
		}
	})
	t.Run("escaped trailing space on entry", func(t *testing.T) {
		id, unresolved := resolveManager(t, []string{"CN=kim\\ ,OU=Users,DC=x"}, "CN=kim,OU=Users,DC=x")
		if id != nil || !unresolved {
			t.Errorf("manager = %v unresolved=%v, want unresolved", id, unresolved)
		}
	})
	t.Run("escaped leading space on entry", func(t *testing.T) {
		id, unresolved := resolveManager(t, []string{"CN=\\ kim,OU=Users,DC=x"}, "CN=kim,OU=Users,DC=x")
		if id != nil || !unresolved {
			t.Errorf("manager = %v unresolved=%v, want unresolved", id, unresolved)
		}
	})
	t.Run("ascii case still matches", func(t *testing.T) {
		id, unresolved := resolveManager(t, []string{boss}, "cn=JOHN SMITH,ou=users,dc=X")
		if id == nil || *id != testID(1) || unresolved {
			t.Errorf("manager = %v unresolved=%v, want %s", id, unresolved, testID(1))
		}
	})
	t.Run("unparseable reference has no string fallback", func(t *testing.T) {
		id, unresolved := resolveManager(t, []string{"not a dn"}, "NOT  A DN")
		if id != nil || !unresolved {
			t.Errorf("manager = %v unresolved=%v, want unresolved", id, unresolved)
		}
	})
}

func TestExactDNPreferredOverNormalizedMatch(t *testing.T) {
	// Two entries whose DNs differ only by ASCII case: a reference that equals
	// one of them exactly resolves to it; a reference matching neither exactly
	// is ambiguous.
	dns := []string{"CN=kim,OU=Users,DC=x", "CN=KIM,OU=Users,DC=x"}
	id, unresolved := resolveManager(t, dns, "CN=KIM,OU=Users,DC=x")
	if id == nil || *id != testID(2) || unresolved {
		t.Errorf("exact reference: manager = %v unresolved=%v, want %s", id, unresolved, testID(2))
	}
	id, unresolved = resolveManager(t, dns, "CN=kim,OU=Users,DC=x")
	if id == nil || *id != testID(1) || unresolved {
		t.Errorf("exact reference: manager = %v unresolved=%v, want %s", id, unresolved, testID(1))
	}
	id, unresolved = resolveManager(t, dns, "cn=Kim,ou=users,dc=x")
	if id != nil || !unresolved {
		t.Errorf("ambiguous reference: manager = %v unresolved=%v, want unresolved", id, unresolved)
	}
}

func TestCollisionBetweenUserAndGroupIsAmbiguous(t *testing.T) {
	sch := mustSchema(t, config.DirectoryTypeActiveDirectory)
	users := []*goldap.Entry{
		adUser(t, "CN=Dup,OU=Users,DC=x", testID(1), "dup", "512"),
		adUserEntry(t, "CN=Report,OU=Users,DC=x", testID(2), map[string][]string{
			attrSAMAccount: {"report"}, attrAccountCtl: {"512"}, attrManager: {"cn=dup,ou=users,dc=x"},
		}),
	}
	groups := []rawGroup{
		{entry: adGroup(t, "CN=DUP,OU=Users,DC=x", testID(100), "Dup")},
		{
			entry: adGroup(t, "CN=Holder,OU=Groups,DC=x", testID(101), "Holder"),
			members: []string{
				"cn=dup,ou=users,dc=x", // ambiguous: user and group
				"CN=Dup,OU=Users,DC=x", // exact user match
				"CN=DUP,OU=Users,DC=x", // exact group match
			},
		},
	}
	var logs bytes.Buffer
	snap, err := mapSnapshot(sch, users, groups, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range snap.Users {
		if u.Username == "report" && (u.ManagerExternalID != nil || !u.ManagerUnresolved) {
			t.Errorf("ambiguous manager resolved: %v unresolved=%v", u.ManagerExternalID, u.ManagerUnresolved)
		}
	}
	var holder public.DirectoryGroup
	for _, g := range snap.Groups {
		if g.DisplayName == "Holder" {
			holder = g
		}
	}
	if len(holder.MemberUserIDs) != 1 || holder.MemberUserIDs[0] != testID(1) ||
		len(holder.MemberGroupIDs) != 1 || holder.MemberGroupIDs[0] != testID(100) {
		t.Errorf("exact members not resolved: users=%v groups=%v", holder.MemberUserIDs, holder.MemberGroupIDs)
	}
	if holder.UnresolvedMembers != 1 {
		t.Errorf("UnresolvedMembers = %d, want 1 (the ambiguous reference)", holder.UnresolvedMembers)
	}
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "ambiguousKeys=1") {
		t.Errorf("log = %q, want a warning with ambiguousKeys=1", out)
	}
	for _, leak := range []string{"Dup", "dup", "DUP", "OU=Users"} {
		if strings.Contains(out, leak) {
			t.Errorf("log contains DN fragment %q: %q", leak, out)
		}
	}
}

func TestDuplicateExactDNIsAmbiguous(t *testing.T) {
	id, unresolved := resolveManager(t, []string{"CN=Twin,OU=Users,DC=x", "CN=Twin,OU=Users,DC=x"}, "CN=Twin,OU=Users,DC=x")
	if id != nil || !unresolved {
		t.Errorf("manager = %v unresolved=%v, want unresolved", id, unresolved)
	}
}

func TestNoAmbiguityLogWhenDNsAreDistinct(t *testing.T) {
	var logs bytes.Buffer
	sch := mustSchema(t, config.DirectoryTypeActiveDirectory)
	_, err := mapSnapshot(sch, []*goldap.Entry{adUser(t, "CN=A,DC=x", testID(1), "a", "512")}, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Errorf("unexpected log output %q", logs.String())
	}
}
