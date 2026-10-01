package ldap

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
)

const leakMarker = "LEAKMARKER"

func TestSanitizeText(t *testing.T) {
	setLimit(t, &maxValueBytes, 10)
	for _, tt := range []struct {
		name, in, want string
		changed        bool
	}{
		{"clean", "Ann Admin", "Ann Admin", false},
		{"trimmed only", "  Ann  ", "Ann", false},
		{"invalid utf-8", "a\xffb", "a�b", true},
		{"run of invalid bytes", "a\xff\xfe\xfdb", "a�b", true},
		{"nul", "a\x00b", "a b", true},
		{"newline and tab", "a\nb\tc", "a b c", true},
		{"DEL and C1", "a\x7fb\u0085c", "a b c", true},
		{"control at the edges is trimmed", "\x00ab\n", "ab", true},
		{"truncated at rune boundary", "abcdefghéé", "abcdefghé", true},
		{"truncated keeps whole runes", "abcdefgéxyz", "abcdefgéx", true},
		{"replacement expansion stays bounded", strings.Repeat("\xff", 20), "�", true},
		{"expansion bounded with text", "ab\xffcd\xffef\xffgh\xffij", "ab�cd�", true},
		{"empty", "", "", false},
	} {
		got, changed := sanitizeText(tt.in)
		if got != tt.want || changed != tt.changed {
			t.Errorf("%s: sanitizeText(%q) = %q, %v; want %q, %v", tt.name, tt.in, got, changed, tt.want, tt.changed)
		}
		if len(got) > maxValueBytes {
			t.Errorf("%s: result longer than the limit: %d bytes", tt.name, len(got))
		}
	}
}

func TestMapSanitizesDisplayAttributes(t *testing.T) {
	sch := mustSchema(t, config.DirectoryTypeActiveDirectory)
	user := adUserEntry(t, "CN=Ann,DC=x", testID(1), map[string][]string{
		attrSAMAccount:  {"ann"},
		attrAccountCtl:  {"514"},
		attrDisplayName: {"Ann\x00 \"Admin\"\n" + leakMarker + "\xff"},
		attrCN:          {"cn"},
		attrGivenName:   {"An\tn"},
		attrSurname:     {"Ad\xc3"},
	})
	group := goldap.NewEntry("CN=G,DC=x", map[string][]string{
		attrObjectGUID:  {encodeGUID(t, testID(100))},
		attrCN:          {"Gro\x00up"},
		attrDescription: {"line1\nline2\xff"},
	})
	var logs bytes.Buffer
	snap, err := mapSnapshot(sch, []*goldap.Entry{user}, []rawGroup{{entry: group}}, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	u := snap.Users[0]
	if u.Invalid || u.Username != "ann" || u.Enabled {
		t.Errorf("sanitizing display text must not invalidate the user or change enabled: %+v", u)
	}
	if u.DisplayName != "Ann  \"Admin\" "+leakMarker+"�"[:0]+"" && u.DisplayName != "Ann  \"Admin\" "+leakMarker+"�" {
		t.Errorf("DisplayName = %q", u.DisplayName)
	}
	if u.GivenName == nil || *u.GivenName != "An n" || u.FamilyName == nil || *u.FamilyName != "Ad�" {
		t.Errorf("names = %v / %v", u.GivenName, u.FamilyName)
	}
	g := snap.Groups[0]
	if g.DisplayName != "Gro up" || g.Description == nil || *g.Description != "line1 line2�" {
		t.Errorf("group = %+v", g)
	}
	out := logs.String()
	if strings.Count(out, "level=WARN") != 1 || !strings.Contains(out, "sanitizedValues=5") {
		t.Errorf("want one warning with sanitizedValues=5, got %q", out)
	}
	if strings.Contains(out, leakMarker) || strings.Contains(out, "Admin") || strings.Contains(out, "Gro") {
		t.Errorf("log leaks values: %q", out)
	}
}

func TestMapTruncatesOverlongDisplayText(t *testing.T) {
	setLimit(t, &maxValueBytes, 8)
	sch := mustSchema(t, config.DirectoryTypeOpenLDAP)
	e := ldapUser("uid=z,dc=x", testID(1), "z", map[string][]string{attrDisplayName: {"abcdefgééé"}})
	snap, err := mapSnapshot(sch, []*goldap.Entry{e}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Users[0].DisplayName; got != "abcdefg" {
		t.Errorf("DisplayName = %q, want truncation at a rune boundary", got)
	}
}

func TestMapMarksUsersWithBadIdentityAttributesInvalid(t *testing.T) {
	setLimit(t, &maxValueBytes, 32)
	ad := mustSchema(t, config.DirectoryTypeActiveDirectory)
	ol := mustSchema(t, config.DirectoryTypeOpenLDAP)
	long := strings.Repeat("x", 33)
	adUserWith := func(attrs map[string][]string) *goldap.Entry {
		m := map[string][]string{attrSAMAccount: {"ann"}, attrAccountCtl: {"514"}, attrMail: {"ann@example.test"}, attrEmployeeID: {"E1"}}
		for k, v := range attrs {
			m[k] = v
		}
		return adUserEntry(t, "CN=Ann,DC=x", testID(1), m)
	}
	tests := []struct {
		name      string
		sch       schema
		entry     *goldap.Entry
		wantUser  string
		wantMail  *string
		wantEmp   *string
		wantValid bool
	}{
		{"valid", ad, adUserWith(nil), "ann", strPtr("ann@example.test"), strPtr("E1"), true},
		{"username invalid utf-8", ad, adUserWith(map[string][]string{attrSAMAccount: {leakMarker + "\xff"}}), "", strPtr("ann@example.test"), strPtr("E1"), false},
		{"username control", ad, adUserWith(map[string][]string{attrSAMAccount: {"an\x00n"}}), "", strPtr("ann@example.test"), strPtr("E1"), false},
		{"username newline", ad, adUserWith(map[string][]string{attrSAMAccount: {"ann\n"}}), "", strPtr("ann@example.test"), strPtr("E1"), false},
		{"username too long", ad, adUserWith(map[string][]string{attrSAMAccount: {long}}), "", strPtr("ann@example.test"), strPtr("E1"), false},
		{"username missing", ad, adUserWith(map[string][]string{attrSAMAccount: nil}), "", strPtr("ann@example.test"), strPtr("E1"), false},
		{"username blank", ad, adUserWith(map[string][]string{attrSAMAccount: {"   "}}), "", strPtr("ann@example.test"), strPtr("E1"), false},
		{"mail invalid utf-8", ad, adUserWith(map[string][]string{attrMail: {"a\xff@example.test"}}), "ann", nil, strPtr("E1"), false},
		{"mail control", ad, adUserWith(map[string][]string{attrMail: {"a@example.test\r\nBcc: x"}}), "ann", nil, strPtr("E1"), false},
		{"mail too long", ad, adUserWith(map[string][]string{attrMail: {long + "@x"}}), "ann", nil, strPtr("E1"), false},
		{"employee id invalid", ad, adUserWith(map[string][]string{attrEmployeeID: {"E\x001"}}), "ann", strPtr("ann@example.test"), nil, false},
		{"employee id too long", ad, adUserWith(map[string][]string{attrEmployeeID: {long}}), "ann", strPtr("ann@example.test"), nil, false},
		{"employee number invalid", ad, adUserWith(map[string][]string{attrEmployeeID: nil, attrEmployeeNum: {"\xff"}}), "ann", strPtr("ann@example.test"), nil, false},
		{"openldap uid invalid", ol, ldapUser("uid=x,dc=x", testID(1), "u\x00", nil), "", nil, nil, false},
		{"openldap employee number invalid", ol, ldapUser("uid=x,dc=x", testID(1), "u", map[string][]string{attrEmployeeNum: {"\xff"}}), "u", nil, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			snap, err := mapSnapshot(tt.sch, []*goldap.Entry{tt.entry}, nil, slog.New(slog.NewTextHandler(&logs, nil)))
			if err != nil {
				t.Fatalf("a bad identity attribute must not fail the snapshot: %v", err)
			}
			if len(snap.Users) != 1 {
				t.Fatalf("users = %d, want the entry to stay in the snapshot", len(snap.Users))
			}
			u := snap.Users[0]
			if u.Invalid == tt.wantValid {
				t.Errorf("Invalid = %v, want %v", u.Invalid, !tt.wantValid)
			}
			if u.Username != tt.wantUser || !reflect.DeepEqual(u.Email, tt.wantMail) || !reflect.DeepEqual(u.EmployeeNumber, tt.wantEmp) {
				t.Errorf("fields = %q %v %v, want %q %v %v", u.Username, u.Email, u.EmployeeNumber, tt.wantUser, tt.wantMail, tt.wantEmp)
			}
			if tt.sch.hasAccountControl && u.Enabled {
				t.Errorf("Enabled must follow userAccountControl (514 = disabled)")
			}
			if !tt.wantValid && (!strings.Contains(logs.String(), "invalidUsers=1") || strings.Contains(logs.String(), leakMarker)) {
				t.Errorf("log = %q", logs.String())
			}
		})
	}
}

func TestMapUnindexableDNsAreNotFatalAndStayUnresolved(t *testing.T) {
	setLimit(t, &maxValueBytes, 40)
	sch := mustSchema(t, config.DirectoryTypeActiveDirectory)
	long := "CN=" + strings.Repeat("x", 50) + ",DC=x"
	bossDN := "CN=Boss,DC=x"
	mk := func(sam, dn string, id int, manager string) *goldap.Entry {
		attrs := map[string][]string{attrSAMAccount: {sam}, attrAccountCtl: {"512"}}
		if manager != "" {
			attrs[attrManager] = []string{manager}
		}
		return adUserEntry(t, dn, testID(id), attrs)
	}
	users := []*goldap.Entry{
		mk("boss", bossDN, 1, ""),
		mk("badDN", "CN=Bad"+leakMarker+"\x00,DC=x", 2, bossDN), // own DN invalid
		mk("longDN", long, 3, bossDN),                           // own DN too long
		mk("badMgr", "CN=Ok,DC=x", 4, "CN=Boss\n,DC=x"),         // manager reference invalid
		mk("badMgr2", "CN=Ok2,DC=x", 5, "CN=\xffBoss,DC=x"),     // invalid UTF-8
		mk("longMgr", "CN=Ok3,DC=x", 6, long),                   // too long
	}
	groups := []rawGroup{
		{entry: adGroup(t, "CN=G\x00,DC=x", testID(100), "G"), members: []string{bossDN, "CN=Boss\x00,DC=x", "CN=\xff,DC=x", long, "CN=Bad" + leakMarker + "\x00,DC=x"}},
	}
	var logs bytes.Buffer
	snap, err := mapSnapshot(sch, users, groups, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("bad DNs must not fail the snapshot: %v", err)
	}
	if len(snap.Users) != 6 || len(snap.Groups) != 1 {
		t.Fatalf("snapshot has %d users, %d groups", len(snap.Users), len(snap.Groups))
	}
	byName := map[string]public.DirectoryUser{}
	for _, u := range snap.Users {
		byName[u.Username] = u
	}
	for _, name := range []string{"badDN", "longDN"} {
		u := byName[name]
		if u.DistinguishedName != "" || u.Invalid {
			t.Errorf("%s: DN = %q invalid=%v, want empty DN and a valid user", name, u.DistinguishedName, u.Invalid)
		}
		if u.ManagerExternalID == nil || *u.ManagerExternalID != testID(1) {
			t.Errorf("%s: a valid manager reference must still resolve", name)
		}
	}
	if byName["boss"].DistinguishedName != bossDN {
		t.Errorf("valid DN must be kept")
	}
	for _, name := range []string{"badMgr", "badMgr2", "longMgr"} {
		u := byName[name]
		if u.ManagerExternalID != nil || !u.ManagerUnresolved {
			t.Errorf("%s: manager = %v unresolved=%v, want unresolved", name, u.ManagerExternalID, u.ManagerUnresolved)
		}
	}
	g := snap.Groups[0]
	if !reflect.DeepEqual(g.MemberUserIDs, []string{testID(1)}) || g.UnresolvedMembers != 4 {
		t.Errorf("group members = %v, unresolved = %d, want only the boss and 4 unresolved", g.MemberUserIDs, g.UnresolvedMembers)
	}
	out := logs.String()
	if strings.Count(out, "level=WARN") != 1 || !strings.Contains(out, "unindexableDNs=3") || !strings.Contains(out, "invalidReferences=7") {
		t.Errorf("log = %q, want one warning with unindexableDNs=3 invalidReferences=7", out)
	}
	if strings.Contains(out, leakMarker) || strings.Contains(out, "Boss") || strings.Contains(out, "DC=") {
		t.Errorf("log leaks DN content: %q", out)
	}
}

func TestMapUnindexableEntryDNIsNotIndexed(t *testing.T) {
	// A group whose own DN is invalid cannot be referenced by member values,
	// not even by an identical invalid string.
	sch := mustSchema(t, config.DirectoryTypeActiveDirectory)
	badDN := "CN=G\x00,DC=x"
	groups := []rawGroup{
		{entry: adGroup(t, badDN, testID(100), "G")},
		{entry: adGroup(t, "CN=H,DC=x", testID(101), "H"), members: []string{badDN}},
	}
	snap, err := mapSnapshot(sch, nil, groups, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range snap.Groups {
		if g.DisplayName == "H" && (len(g.MemberGroupIDs) != 0 || g.UnresolvedMembers != 1) {
			t.Errorf("group H = %+v", g)
		}
	}
}

func TestMapMissingObjectIDStillFailsEvenWithBadText(t *testing.T) {
	sch := mustSchema(t, config.DirectoryTypeActiveDirectory)
	e := goldap.NewEntry("CN=Bad\x00,DC=x", map[string][]string{attrSAMAccount: {"a\xff"}, attrAccountCtl: {"512"}})
	_, err := mapSnapshot(sch, []*goldap.Entry{e}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "missing or invalid objectGUID") {
		t.Fatalf("err = %v", err)
	}
	if strings.ContainsAny(err.Error(), "\x00\xff") || strings.Contains(err.Error(), "DC=") {
		t.Errorf("error leaks values: %q", err)
	}
}

func TestFetchSurvivesBadTextEndToEnd(t *testing.T) {
	dir := newFakeDirectory()
	dir.rangeSize = 2
	dir.users = []*goldap.Entry{
		adUserEntry(t, annDN, testID(1), map[string][]string{attrSAMAccount: {"ann"}, attrAccountCtl: {"512"}, attrManager: {benDN}}),
		adUserEntry(t, benDN, testID(2), map[string][]string{attrSAMAccount: {"ben\x00"}, attrAccountCtl: {"514"}, attrDisplayName: {leakMarker + "\n"}}),
	}
	dir.groups = []*goldap.Entry{adGroup(t, staffDN, testID(100), "Staff")}
	dir.members[staffDN] = []string{annDN, benDN, "CN=late\x00,DC=x"} // the bad member is in a later range
	var logs bytes.Buffer
	snap, err := newTestSource(t, adConfig(), dir, slog.New(slog.NewTextHandler(&logs, nil))).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch failed on bad text: %v", err)
	}
	if len(snap.Users) != 2 || len(snap.Groups) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	for _, u := range snap.Users {
		switch u.ExternalID {
		case testID(2):
			if !u.Invalid || u.Enabled || u.Username != "" {
				t.Errorf("ben = %+v, want invalid, disabled, empty username", u)
			}
		case testID(1):
			if u.Invalid || u.ManagerExternalID == nil || *u.ManagerExternalID != testID(2) {
				t.Errorf("ann = %+v", u)
			}
		}
	}
	if g := snap.Groups[0]; len(g.MemberUserIDs) != 2 || g.UnresolvedMembers != 1 {
		t.Errorf("group = %+v", g)
	}
	if strings.Contains(logs.String(), leakMarker) || strings.Contains(logs.String(), "ben") {
		t.Errorf("log leaks values: %q", logs.String())
	}
}
