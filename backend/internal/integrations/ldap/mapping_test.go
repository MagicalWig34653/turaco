package ldap

import (
	"reflect"
	"strings"
	"testing"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
)

func mustSchema(t *testing.T, directoryType string) schema {
	t.Helper()
	s, err := schemaFor(directoryType)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func strPtr(s string) *string { return &s }

func TestDecodeGUID(t *testing.T) {
	raw := []byte{0x33, 0x22, 0x11, 0x00, 0x55, 0x44, 0x77, 0x66, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	got, ok := decodeGUID(raw)
	if !ok || got != "00112233-4455-6677-8899-aabbccddeeff" {
		t.Fatalf("decodeGUID = %q, %v", got, ok)
	}
	for _, n := range []int{0, 1, 15, 17, 36} {
		if _, ok := decodeGUID(make([]byte, n)); ok {
			t.Errorf("decodeGUID accepted %d bytes", n)
		}
	}
	if _, ok := decodeGUID(nil); ok {
		t.Error("decodeGUID accepted nil")
	}
}

func TestEncodeGUIDHelperRoundTrips(t *testing.T) {
	id := "a1b2c3d4-e5f6-4789-abcd-ef0123456789"
	got, ok := decodeGUID([]byte(encodeGUID(t, id)))
	if !ok || got != id {
		t.Fatalf("round trip = %q, %v", got, ok)
	}
}

func TestParseUUID(t *testing.T) {
	got, ok := parseUUID("  A1B2C3D4-E5F6-4789-ABCD-EF0123456789 ")
	if !ok || got != "a1b2c3d4-e5f6-4789-abcd-ef0123456789" {
		t.Fatalf("parseUUID = %q, %v", got, ok)
	}
	for _, bad := range []string{
		"", "not-a-uuid", "a1b2c3d4e5f64789abcdef0123456789",
		"a1b2c3d4-e5f6-4789-abcd-ef012345678", "a1b2c3d4-e5f6-4789-abcd-ef012345678g",
		"{a1b2c3d4-e5f6-4789-abcd-ef0123456789}", "a1b2c3d4_e5f6-4789-abcd-ef0123456789",
	} {
		if _, ok := parseUUID(bad); ok {
			t.Errorf("parseUUID accepted %q", bad)
		}
	}
}

func TestMapActiveDirectoryUserFields(t *testing.T) {
	id := testID(1)
	e := adUserEntry(t, "CN=Ada Lovelace,OU=Users,DC=example,DC=test", id, map[string][]string{
		attrSAMAccount:  {"alovelace"},
		attrDisplayName: {"  Ada Lovelace  "},
		attrCN:          {"Ada L"},
		attrGivenName:   {"Ada"},
		attrSurname:     {"Lovelace"},
		attrMail:        {"ada@example.test"},
		attrEmployeeID:  {"E-100"},
		attrEmployeeNum: {"N-200"},
		attrAccountCtl:  {"512"},
	})
	snap, err := mapSnapshot(mustSchema(t, config.DirectoryTypeActiveDirectory), []*goldap.Entry{e}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := public.DirectoryUser{
		ExternalID:        id,
		Username:          "alovelace",
		DistinguishedName: "CN=Ada Lovelace,OU=Users,DC=example,DC=test",
		DisplayName:       "Ada Lovelace",
		GivenName:         strPtr("Ada"),
		FamilyName:        strPtr("Lovelace"),
		Email:             strPtr("ada@example.test"),
		EmployeeNumber:    strPtr("E-100"), // employeeID wins over employeeNumber
		Enabled:           true,
	}
	if len(snap.Users) != 1 || !reflect.DeepEqual(snap.Users[0], want) {
		t.Fatalf("user = %+v\nwant   %+v", snap.Users, want)
	}
}

func TestMapUserAccountControl(t *testing.T) {
	sch := mustSchema(t, config.DirectoryTypeActiveDirectory)
	tests := []struct {
		name    string
		uac     []string
		enabled bool
		wantErr bool
	}{
		{"normal account", []string{"512"}, true, false},
		{"disabled", []string{"514"}, false, false},
		{"disabled with other flags", []string{"66050"}, false, false}, // 0x10202
		{"enabled with other flags", []string{"66048"}, true, false},   // 0x10200
		{"padded", []string{" 514 "}, false, false},
		{"missing", nil, false, true},
		{"empty", []string{""}, false, true},
		{"unparseable", []string{"disabled"}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := map[string][]string{attrSAMAccount: {"u"}}
			if tt.uac != nil {
				attrs[attrAccountCtl] = tt.uac
			}
			snap, err := mapSnapshot(sch, []*goldap.Entry{adUserEntry(t, "CN=u,DC=x", testID(1), attrs)}, nil)
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), attrAccountCtl) || !strings.Contains(err.Error(), "1 entries") {
					t.Fatalf("err = %v, want userAccountControl count error", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if snap.Users[0].Enabled != tt.enabled {
				t.Errorf("Enabled = %v, want %v", snap.Users[0].Enabled, tt.enabled)
			}
		})
	}
}

func TestMapOpenLDAPUser(t *testing.T) {
	sch := mustSchema(t, config.DirectoryTypeOpenLDAP)
	e := ldapUser("uid=bob,ou=people,dc=example,dc=test", "A1B2C3D4-E5F6-4789-ABCD-EF0123456789", "bob", map[string][]string{
		attrCN:          {"Bob Builder"},
		attrSurname:     {"Builder"},
		attrEmployeeNum: {"42"},
		attrEmployeeID:  {"ignored"}, // not an OpenLDAP source
		attrAccountCtl:  {"514"},     // ignored: OpenLDAP is always enabled
	})
	snap, err := mapSnapshot(sch, []*goldap.Entry{e}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := public.DirectoryUser{
		ExternalID:        "a1b2c3d4-e5f6-4789-abcd-ef0123456789",
		Username:          "bob",
		DistinguishedName: "uid=bob,ou=people,dc=example,dc=test",
		DisplayName:       "Bob Builder", // falls back to cn
		FamilyName:        strPtr("Builder"),
		EmployeeNumber:    strPtr("42"),
		Enabled:           true,
	}
	if !reflect.DeepEqual(snap.Users[0], want) {
		t.Fatalf("user = %+v\nwant   %+v", snap.Users[0], want)
	}
}

func TestMapOptionalAttributesAreNilWhenEmpty(t *testing.T) {
	sch := mustSchema(t, config.DirectoryTypeOpenLDAP)
	e := ldapUser("uid=eve,dc=x", testID(1), "eve", map[string][]string{
		attrGivenName:   {""},
		attrSurname:     {"   "},
		attrMail:        {" \t"},
		attrEmployeeNum: {""},
		attrManager:     {"  "},
	})
	snap, err := mapSnapshot(sch, []*goldap.Entry{e}, nil)
	if err != nil {
		t.Fatal(err)
	}
	u := snap.Users[0]
	if u.GivenName != nil || u.FamilyName != nil || u.Email != nil || u.EmployeeNumber != nil || u.ManagerExternalID != nil || u.ManagerUnresolved {
		t.Fatalf("empty attributes must map to nil: %+v", u)
	}
}

func TestMapDisplayNameFallbacks(t *testing.T) {
	sch := mustSchema(t, config.DirectoryTypeOpenLDAP)
	tests := []struct {
		name  string
		attrs map[string][]string
		want  string
	}{
		{"displayName", map[string][]string{attrDisplayName: {"Display"}, attrCN: {"Common"}}, "Display"},
		{"cn", map[string][]string{attrDisplayName: {" "}, attrCN: {"Common"}}, "Common"},
		{"username", map[string][]string{}, "user1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap, err := mapSnapshot(sch, []*goldap.Entry{ldapUser("uid=user1,dc=x", testID(1), "user1", tt.attrs)}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := snap.Users[0].DisplayName; got != tt.want {
				t.Errorf("DisplayName = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMapEmployeeNumberFallbackToEmployeeNumberAttribute(t *testing.T) {
	e := adUserEntry(t, "CN=u,DC=x", testID(1), map[string][]string{
		attrSAMAccount: {"u"}, attrAccountCtl: {"512"}, attrEmployeeID: {" "}, attrEmployeeNum: {"N-7"},
	})
	snap, err := mapSnapshot(mustSchema(t, config.DirectoryTypeActiveDirectory), []*goldap.Entry{e}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Users[0].EmployeeNumber; got == nil || *got != "N-7" {
		t.Fatalf("EmployeeNumber = %v", got)
	}
}

func TestMapAttributeNamesAreCaseInsensitive(t *testing.T) {
	e := goldap.NewEntry("CN=u,DC=x", map[string][]string{
		"objectguid":         {encodeGUID(t, testID(1))},
		"SAMACCOUNTNAME":     {"u"},
		"useraccountcontrol": {"514"},
	})
	snap, err := mapSnapshot(mustSchema(t, config.DirectoryTypeActiveDirectory), []*goldap.Entry{e}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if u := snap.Users[0]; u.ExternalID != testID(1) || u.Username != "u" || u.Enabled {
		t.Fatalf("user = %+v", u)
	}
}

func TestMapManagerResolution(t *testing.T) {
	sch := mustSchema(t, config.DirectoryTypeActiveDirectory)
	boss := adUser(t, "CN=Boss Person,OU=Users,DC=Example,DC=Test", testID(1), "boss", "512")
	groupDN := "CN=Staff,OU=Groups,DC=example,DC=test"
	mk := func(sam string, id int, manager string) *goldap.Entry {
		attrs := map[string][]string{attrSAMAccount: {sam}, attrAccountCtl: {"512"}}
		if manager != "" {
			attrs[attrManager] = []string{manager}
		}
		return adUserEntry(t, "CN="+sam+",OU=Users,DC=example,DC=test", testID(id), attrs)
	}
	users := []*goldap.Entry{
		boss,
		mk("exact", 2, "CN=Boss Person,OU=Users,DC=Example,DC=Test"),
		mk("case", 3, "cn=boss person,ou=users,dc=example,dc=test"),
		mk("spacing", 4, "CN=Boss Person , OU=Users ,  DC=Example, DC=Test"),
		mk("missing", 5, "CN=Ghost,OU=Users,DC=example,DC=test"),
		mk("none", 6, ""),
		mk("manager-is-group", 7, groupDN),
		mk("garbage", 8, "not a dn"),
	}
	groups := []rawGroup{{entry: adGroup(t, groupDN, testID(100), "Staff")}}
	snap, err := mapSnapshot(sch, users, groups)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]public.DirectoryUser{}
	for _, u := range snap.Users {
		byName[u.Username] = u
	}
	for _, name := range []string{"exact", "case", "spacing"} {
		u := byName[name]
		if u.ManagerExternalID == nil || *u.ManagerExternalID != testID(1) || u.ManagerUnresolved {
			t.Errorf("%s: manager = %v unresolved=%v, want %s", name, u.ManagerExternalID, u.ManagerUnresolved, testID(1))
		}
	}
	for _, name := range []string{"missing", "manager-is-group", "garbage"} {
		u := byName[name]
		if u.ManagerExternalID != nil || !u.ManagerUnresolved {
			t.Errorf("%s: manager = %v unresolved=%v, want unresolved", name, u.ManagerExternalID, u.ManagerUnresolved)
		}
	}
	if u := byName["none"]; u.ManagerExternalID != nil || u.ManagerUnresolved {
		t.Errorf("none: manager = %v unresolved=%v, want neither", u.ManagerExternalID, u.ManagerUnresolved)
	}
}

func TestMapGroupMembers(t *testing.T) {
	sch := mustSchema(t, config.DirectoryTypeActiveDirectory)
	users := []*goldap.Entry{
		adUser(t, "CN=Ann,OU=Users,DC=example,DC=test", testID(1), "ann", "512"),
		adUser(t, "CN=Ben,OU=Users,DC=example,DC=test", testID(2), "ben", "512"),
	}
	parentDN := "CN=Parent,OU=Groups,DC=example,DC=test"
	childDN := "CN=Child,OU=Groups,DC=example,DC=test"
	parent := rawGroup{
		entry: adGroup(t, parentDN, testID(100), "Parent"),
		members: []string{
			"cn=ann,ou=users,dc=example,dc=test",              // user, different case
			"CN=Ben , OU=Users, DC=example, DC=test",          // user, different spacing
			"CN=ANN,OU=Users,DC=example,DC=test",              // duplicate of the first
			"CN=Child,OU=Groups,DC=example,DC=test",           // nested group
			"cn=child,ou=groups,dc=example,dc=test",           // duplicate nested group
			parentDN,                                          // self membership
			"cn=PARENT,ou=groups,dc=example,dc=test",          // self membership, other case
			"CN=S-1-5-21-1,CN=ForeignSecurityPrincipals,DC=x", // foreign principal
			"cn=s-1-5-21-1,cn=foreignsecurityprincipals,dc=x", // duplicate foreign principal
			"CN=Outside,OU=Elsewhere,DC=example,DC=test",      // outside the snapshot
			"",    // empty value
			"   ", // blank value
		},
	}
	child := rawGroup{
		entry:   adGroup(t, childDN, testID(101), "Child"),
		members: []string{"CN=Ben,OU=Users,DC=example,DC=test"},
	}
	empty := rawGroup{entry: adGroup(t, "CN=Empty,OU=Groups,DC=example,DC=test", testID(102), "Empty")}

	snap, err := mapSnapshot(sch, users, []rawGroup{parent, child, empty})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]public.DirectoryGroup{}
	for _, g := range snap.Groups {
		byName[g.DisplayName] = g
	}

	p := byName["Parent"]
	if !reflect.DeepEqual(p.MemberUserIDs, []string{testID(1), testID(2)}) {
		t.Errorf("parent users = %v", p.MemberUserIDs)
	}
	if !reflect.DeepEqual(p.MemberGroupIDs, []string{testID(101)}) {
		t.Errorf("parent groups = %v (self membership must be ignored, duplicates collapsed)", p.MemberGroupIDs)
	}
	if p.UnresolvedMembers != 2 {
		t.Errorf("parent unresolved = %d, want 2 (distinct foreign + outside)", p.UnresolvedMembers)
	}
	if c := byName["Child"]; !reflect.DeepEqual(c.MemberUserIDs, []string{testID(2)}) || c.MemberGroupIDs != nil || c.UnresolvedMembers != 0 {
		t.Errorf("child = %+v", c)
	}
	if e := byName["Empty"]; e.MemberUserIDs != nil || e.MemberGroupIDs != nil || e.UnresolvedMembers != 0 {
		t.Errorf("empty = %+v", e)
	}
}

func TestMapGroupFields(t *testing.T) {
	sch := mustSchema(t, config.DirectoryTypeOpenLDAP)
	g := goldap.NewEntry("cn=ops,ou=groups,dc=x", map[string][]string{
		attrEntryUUID:   {testID(5)},
		attrCN:          {"ops"},
		attrDisplayName: {"Operations"},
		attrDescription: {" Ops team "},
	})
	named := goldap.NewEntry("cn=n,ou=groups,dc=x", map[string][]string{attrEntryUUID: {testID(6)}, attrDisplayName: {"Only Display"}})
	anonymous := goldap.NewEntry("cn=a,ou=groups,dc=x", map[string][]string{attrEntryUUID: {testID(7)}})
	snap, err := mapSnapshot(sch, nil, []rawGroup{{entry: g}, {entry: named}, {entry: anonymous}})
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Groups[0]; got.DisplayName != "ops" || got.Description == nil || *got.Description != "Ops team" {
		t.Errorf("group 0 = %+v", got)
	}
	if got := snap.Groups[1]; got.DisplayName != "Only Display" || got.Description != nil {
		t.Errorf("group 1 = %+v", got)
	}
	if got := snap.Groups[2]; got.DisplayName != testID(7) {
		t.Errorf("group 2 display name = %q, want ID fallback", got.DisplayName)
	}
}

func TestMapSortsByExternalID(t *testing.T) {
	sch := mustSchema(t, config.DirectoryTypeOpenLDAP)
	users := []*goldap.Entry{
		ldapUser("uid=c,dc=x", testID(3), "c", nil),
		ldapUser("uid=a,dc=x", testID(1), "a", nil),
		ldapUser("uid=b,dc=x", testID(2), "b", nil),
	}
	snap, err := mapSnapshot(sch, users, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{testID(1), testID(2), testID(3)} {
		if snap.Users[i].ExternalID != want {
			t.Fatalf("users not sorted: %+v", snap.Users)
		}
	}
}

func TestMapEmptyDirectoryIsAValidSnapshot(t *testing.T) {
	snap, err := mapSnapshot(mustSchema(t, config.DirectoryTypeActiveDirectory), nil, nil)
	if err != nil || len(snap.Users) != 0 || len(snap.Groups) != 0 {
		t.Fatalf("snapshot = %+v, err = %v", snap, err)
	}
}

func TestMapInvalidIDsFailTheWholeSnapshot(t *testing.T) {
	const secretDN = "CN=Secret Person,OU=Users,DC=example,DC=test"
	const secretName = "secret.person"
	ad := mustSchema(t, config.DirectoryTypeActiveDirectory)
	ol := mustSchema(t, config.DirectoryTypeOpenLDAP)

	good := adUser(t, "CN=Good,DC=x", testID(1), "good", "512")
	noGUID := goldap.NewEntry(secretDN, map[string][]string{attrSAMAccount: {secretName}, attrAccountCtl: {"512"}})
	shortGUID := goldap.NewEntry(secretDN, map[string][]string{attrObjectGUID: {"\x01\x02"}, attrSAMAccount: {secretName}, attrAccountCtl: {"512"}})
	noUsername := adUserEntry(t, secretDN, testID(9), map[string][]string{attrAccountCtl: {"512"}})
	dupA := adUser(t, "CN=A,DC=x", testID(4), "a", "512")
	dupB := adUser(t, "CN=B,DC=x", testID(4), "b", "512")
	groupSameIDAsUser := adGroup(t, "CN=G,DC=x", testID(1), "g")
	badGroup := goldap.NewEntry("CN=Secret Group,DC=x", map[string][]string{attrCN: {"secret group"}})

	tests := []struct {
		name    string
		sch     schema
		users   []*goldap.Entry
		groups  []rawGroup
		wantAll []string
	}{
		{"missing guid", ad, []*goldap.Entry{good, noGUID}, nil, []string{"users: 1 entries with missing or invalid objectGUID"}},
		{"short guid", ad, []*goldap.Entry{good, shortGUID, noGUID}, nil, []string{"users: 2 entries with missing or invalid objectGUID"}},
		{"missing username", ad, []*goldap.Entry{good, noUsername}, nil, []string{"users: 1 entries with missing sAMAccountName"}},
		{"duplicate user id", ad, []*goldap.Entry{dupA, dupB}, nil, []string{"1 entries with a duplicate objectGUID"}},
		{"user and group share id", ad, []*goldap.Entry{good}, []rawGroup{{entry: groupSameIDAsUser}}, []string{"1 entries with a duplicate objectGUID"}},
		{"group without id", ad, []*goldap.Entry{good}, []rawGroup{{entry: badGroup}}, []string{"groups: 1 entries with missing or invalid objectGUID"}},
		{"invalid entryUUID", ol, []*goldap.Entry{ldapUser("uid=x,dc=x", "not-a-uuid", secretName, nil)}, nil, []string{"users: 1 entries with missing or invalid entryUUID"}},
		{"several problems", ad, []*goldap.Entry{good, noGUID, noUsername}, []rawGroup{{entry: badGroup}}, []string{"users: 1 entries with missing or invalid objectGUID", "groups: 1 entries"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap, err := mapSnapshot(tt.sch, tt.users, tt.groups)
			if err == nil {
				t.Fatal("expected error")
			}
			if len(snap.Users) != 0 || len(snap.Groups) != 0 {
				t.Errorf("partial snapshot returned: %+v", snap)
			}
			for _, want := range tt.wantAll {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			for _, leak := range []string{secretDN, secretName, "Secret", "secret", "CN=", "DC=", testID(1), testID(4), "not-a-uuid"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("error %q leaks %q", err, leak)
				}
			}
		})
	}
}

func TestNormalizeDN(t *testing.T) {
	same := [][2]string{
		{"CN=Ann,OU=Users,DC=Example,DC=Test", "cn=ann,ou=users,dc=example,dc=test"},
		{"CN=Ann , OU=Users ,DC=Example, DC=Test", "CN=Ann,OU=Users,DC=Example,DC=Test"},
		{"CN=Ann   Lee,DC=x", "cn=ann lee,dc=x"},
		{"CN=Lee\\, Ann,DC=x", "cn=lee\\, ann,dc=x"},
		{"cn=a+sn=b,dc=x", "SN=B+CN=A,DC=X"},
		{"not a dn", "NOT  A DN"},
	}
	for _, p := range same {
		if normalizeDN(p[0]) != normalizeDN(p[1]) {
			t.Errorf("normalizeDN(%q) != normalizeDN(%q)", p[0], p[1])
		}
	}
	different := [][2]string{
		{"CN=Ann,DC=x", "CN=Ann,DC=y"},
		{"CN=Lee\\, Ann,DC=x", "CN=Lee,CN=Ann,DC=x"},
		{"CN=Ann,OU=A,DC=x", "CN=Ann,DC=x"},
	}
	for _, p := range different {
		if normalizeDN(p[0]) == normalizeDN(p[1]) {
			t.Errorf("normalizeDN(%q) == normalizeDN(%q)", p[0], p[1])
		}
	}
}

func TestAttributeAllowlists(t *testing.T) {
	forbidden := []string{"*", "+", "userPassword", "unicodePwd", "ms-Mcs-AdmPwd", "msLAPS-Password", "ntSecurityDescriptor", "pwdLastSet"}
	for _, dt := range []string{config.DirectoryTypeActiveDirectory, config.DirectoryTypeOpenLDAP} {
		sch := mustSchema(t, dt)
		for _, list := range [][]string{sch.userAttrs, sch.groupAttrs} {
			for _, a := range list {
				for _, f := range forbidden {
					if strings.EqualFold(a, f) {
						t.Errorf("%s requests forbidden attribute %q", dt, a)
					}
				}
			}
		}
	}
	ad := mustSchema(t, config.DirectoryTypeActiveDirectory)
	if !reflect.DeepEqual(ad.userAttrs, []string{"objectGUID", "sAMAccountName", "displayName", "cn", "givenName", "sn", "mail", "employeeID", "employeeNumber", "manager", "userAccountControl"}) {
		t.Errorf("AD user attributes = %v", ad.userAttrs)
	}
	if !reflect.DeepEqual(ad.groupAttrs, []string{"objectGUID", "cn", "displayName", "description", "member"}) {
		t.Errorf("AD group attributes = %v", ad.groupAttrs)
	}
	ol := mustSchema(t, config.DirectoryTypeOpenLDAP)
	if !reflect.DeepEqual(ol.userAttrs, []string{"entryUUID", "uid", "displayName", "cn", "givenName", "sn", "mail", "employeeNumber", "manager"}) {
		t.Errorf("OpenLDAP user attributes = %v", ol.userAttrs)
	}
}
