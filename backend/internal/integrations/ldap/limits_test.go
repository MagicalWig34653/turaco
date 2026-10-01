package ldap

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	ber "github.com/go-asn1-ber/asn1-ber"
	goldap "github.com/go-ldap/ldap/v3"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
)

// setLimit replaces a package-level limit for the duration of a test. Tests
// that use it must not run in parallel.
func setLimit[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

func TestProductionLimits(t *testing.T) {
	if maxPacketLengthBytes != 16<<20 {
		t.Errorf("maxPacketLengthBytes = %d, want 16 MiB", maxPacketLengthBytes)
	}
	if maxUserEntries != 500_000 || maxGroupEntries != 200_000 {
		t.Errorf("entry limits = %d users, %d groups, want 500000 and 200000", maxUserEntries, maxGroupEntries)
	}
	if maxValueBytes != 4096 {
		t.Errorf("maxValueBytes = %d, want 4096", maxValueBytes)
	}
}

func TestBERPacketLimitIsSet(t *testing.T) {
	if ber.MaxPacketLengthBytes != maxPacketLengthBytes {
		t.Fatalf("ber.MaxPacketLengthBytes = %d, want %d", ber.MaxPacketLengthBytes, maxPacketLengthBytes)
	}
	// A header announcing 32 MiB is refused before any allocation.
	huge := []byte{0x30, 0x84, 0x02, 0x00, 0x00, 0x00}
	if _, err := ber.ReadPacket(bytes.NewReader(huge)); err == nil {
		t.Error("ber.ReadPacket accepted a 32 MiB packet header")
	}
}

func TestServerTimeLimit(t *testing.T) {
	for _, tt := range []struct {
		in   time.Duration
		want int
	}{
		{time.Nanosecond, 1},
		{100 * time.Millisecond, 1},
		{time.Second, 1},
		{1500 * time.Millisecond, 2},
		{30 * time.Second, 30},
		{5 * time.Minute, 300},
		{1 << 62, 1<<31 - 1},
	} {
		if got := serverTimeLimit(tt.in); got != tt.want {
			t.Errorf("serverTimeLimit(%v) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestFetchTimeLimitIsAtLeastOneSecond(t *testing.T) {
	dir := basicDirectory(t)
	s := newTestSource(t, adConfig(), dir, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	if _, err := s.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	for _, r := range dir.snapshotSearches() {
		if r.timeLimit != 1 {
			t.Errorf("search time limit = %d, want 1", r.timeLimit)
		}
	}
}

func manyUsers(t testing.TB, n int) []*goldap.Entry {
	users := make([]*goldap.Entry, 0, n)
	for i := 1; i <= n; i++ {
		users = append(users, adUser(t, fmt.Sprintf("CN=u%d,OU=Users,DC=example,DC=test", i), testID(i), fmt.Sprintf("u%d", i), "512"))
	}
	return users
}

func TestFetchUserEntryLimit(t *testing.T) {
	setLimit(t, &maxUserEntries, 3)
	for _, pageLen := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("page length %d", pageLen), func(t *testing.T) {
			dir := newFakeDirectory()
			dir.pageLen = pageLen
			dir.users = manyUsers(t, 3)
			snap, err := newTestSource(t, adConfig(), dir, nil).Fetch(context.Background())
			if err != nil || len(snap.Users) != 3 {
				t.Fatalf("exactly at the limit: err = %v, users = %d", err, len(snap.Users))
			}

			dir = newFakeDirectory()
			dir.pageLen = pageLen
			dir.users = manyUsers(t, 4)
			snap, err = newTestSource(t, adConfig(), dir, nil).Fetch(context.Background())
			if err == nil {
				t.Fatal("over the limit: expected error")
			}
			if len(snap.Users) != 0 || len(snap.Groups) != 0 {
				t.Errorf("partial snapshot returned: %+v", snap)
			}
			if msg := err.Error(); !strings.Contains(msg, "more than 3 entries") || strings.Contains(msg, "DC=example") || strings.Contains(msg, "u1") {
				t.Errorf("error = %q", msg)
			}
			if dir.closeCount() != 1 {
				t.Errorf("connection closed %d times, want 1", dir.closeCount())
			}
		})
	}
}

func TestFetchGroupEntryLimit(t *testing.T) {
	setLimit(t, &maxGroupEntries, 2)
	build := func(n int) *fakeDirectory {
		dir := newFakeDirectory()
		for i := 1; i <= n; i++ {
			dir.groups = append(dir.groups, adGroup(t, fmt.Sprintf("CN=g%d,OU=Groups,DC=example,DC=test", i), testID(100+i), fmt.Sprintf("g%d", i)))
		}
		return dir
	}
	if snap, err := newTestSource(t, adConfig(), build(2), nil).Fetch(context.Background()); err != nil || len(snap.Groups) != 2 {
		t.Fatalf("exactly at the limit: err = %v", err)
	}
	snap, err := newTestSource(t, adConfig(), build(3), nil).Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "search groups: more than 2 entries") {
		t.Fatalf("over the limit: err = %v", err)
	}
	if len(snap.Groups) != 0 {
		t.Errorf("partial snapshot returned: %+v", snap)
	}
}

func TestFetchPagesThroughAllResults(t *testing.T) {
	dir := newFakeDirectory()
	dir.pageLen = 2
	dir.users = manyUsers(t, 5)
	snap, err := newTestSource(t, adConfig(), dir, nil).Fetch(context.Background())
	if err != nil || len(snap.Users) != 5 {
		t.Fatalf("err = %v, users = %d, want 5", err, len(snap.Users))
	}
	pages := 0
	for _, r := range dir.snapshotSearches() {
		if r.baseDN == testUserBaseDN {
			pages++
		}
	}
	if pages != 3 {
		t.Errorf("user search pages = %d, want 3", pages)
	}
}

func TestFetchEndlessPagingCookieFails(t *testing.T) {
	setLimit(t, &maxUserEntries, 500)
	dir := newFakeDirectory()
	dir.endlessCookie = true
	dir.users = manyUsers(t, 1)
	_, err := newTestSource(t, adConfig(), dir, nil).Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "too many result pages") {
		t.Fatalf("err = %v, want too many result pages", err)
	}
	if n := len(dir.snapshotSearches()); n > 12 {
		t.Errorf("%d searches issued for an endless cookie", n)
	}
}

func TestFetchRejectsInvalidAndOversizedText(t *testing.T) {
	const secretValue = "SECRET-VALUE"
	user := func(attrs map[string][]string) []*goldap.Entry {
		m := map[string][]string{attrSAMAccount: {"ann"}, attrAccountCtl: {"512"}}
		for k, v := range attrs {
			m[k] = v
		}
		return []*goldap.Entry{adUserEntry(t, "CN=Ann,OU=Users,DC=example,DC=test", testID(1), m)}
	}
	tests := []struct {
		name  string
		users []*goldap.Entry
		group *goldap.Entry
		want  string
	}{
		{"user invalid utf-8", user(map[string][]string{attrMail: {secretValue + "\xff"}}), nil, "users: 1 entries with an attribute value that is not valid text"},
		{"user nul", user(map[string][]string{attrDisplayName: {secretValue + "\x00"}}), nil, "users: 1 entries with an attribute value that is not valid text"},
		{"user newline", user(map[string][]string{attrDisplayName: {secretValue + "\nInjected"}}), nil, "users: 1 entries with an attribute value that is not valid text"},
		{"user tab", user(map[string][]string{attrGivenName: {secretValue + "\t"}}), nil, "users: 1 entries with an attribute value that is not valid text"},
		{"user DEL", user(map[string][]string{attrSurname: {secretValue + "\x7f"}}), nil, "users: 1 entries with an attribute value that is not valid text"},
		{"user username with control", user(map[string][]string{attrSAMAccount: {secretValue + "\x01"}}), nil, "users: 1 entries with an attribute value that is not valid text"},
		{"user manager with control", user(map[string][]string{attrManager: {"CN=" + secretValue + "\x00,DC=x"}}), nil, "users: 1 entries with an attribute value that is not valid text"},
		{"user DN invalid", []*goldap.Entry{adUser(t, "CN="+secretValue+"\n,OU=Users,DC=example,DC=test", testID(1), "ann", "512")}, nil, "users: 1 entries with an attribute value that is not valid text"},
		{"user DN invalid utf-8", []*goldap.Entry{adUser(t, "CN="+secretValue+"\xc3,OU=Users,DC=example,DC=test", testID(1), "ann", "512")}, nil, "users: 1 entries with an attribute value that is not valid text"},
		{"group name control", nil, goldap.NewEntry("CN=G,OU=Groups,DC=example,DC=test", map[string][]string{
			attrObjectGUID: {encodeGUID(t, testID(100))}, attrCN: {secretValue + "\r"}}), "groups: 1 entries with an attribute value that is not valid text"},
		{"group description invalid utf-8", nil, goldap.NewEntry("CN=G,OU=Groups,DC=example,DC=test", map[string][]string{
			attrObjectGUID: {encodeGUID(t, testID(100))}, attrCN: {"G"}, attrDescription: {"\xfe" + secretValue}}), "groups: 1 entries with an attribute value that is not valid text"},
		{"group DN invalid", nil, goldap.NewEntry("CN=G"+secretValue+"\x00,OU=Groups,DC=example,DC=test", map[string][]string{
			attrObjectGUID: {encodeGUID(t, testID(100))}, attrCN: {"G"}}), "groups: 1 entries with an attribute value that is not valid text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := newFakeDirectory()
			dir.users = tt.users
			if tt.group != nil {
				dir.groups = []*goldap.Entry{tt.group}
			}
			snap, err := newTestSource(t, adConfig(), dir, nil).Fetch(context.Background())
			if err == nil {
				t.Fatalf("expected error, got snapshot %+v", snap)
			}
			if len(snap.Users) != 0 || len(snap.Groups) != 0 {
				t.Errorf("partial snapshot returned: %+v", snap)
			}
			msg := err.Error()
			if !strings.Contains(msg, tt.want) {
				t.Errorf("error = %q, want it to contain %q", msg, tt.want)
			}
			for _, leak := range []string{secretValue, "DC=example", "Injected", "ann"} {
				if strings.Contains(msg, leak) {
					t.Errorf("error leaks %q: %q", leak, msg)
				}
			}
		})
	}
}

func TestFetchRejectsInvalidMembers(t *testing.T) {
	for _, member := range []string{"CN=a\x00,DC=x", "CN=a\n,DC=x", "CN=a\xff,DC=x", "CN=\t,DC=x"} {
		dir := newFakeDirectory()
		dir.groups = []*goldap.Entry{adGroup(t, staffDN, testID(100), "Staff")}
		dir.members[staffDN] = []string{annDN, member}
		_, err := newTestSource(t, adConfig(), dir, nil).Fetch(context.Background())
		if err == nil || !strings.Contains(err.Error(), "groups: 1 entries with a member value that is not valid text") {
			t.Errorf("member %q: err = %v", member, err)
		}
	}
}

func TestFetchRejectsInvalidMemberInLaterRange(t *testing.T) {
	dir := newFakeDirectory()
	dir.rangeSize = 2
	dir.groups = []*goldap.Entry{adGroup(t, staffDN, testID(100), "Staff")}
	dir.members[staffDN] = []string{annDN, benDN, "CN=late\x00,DC=x"}
	_, err := newTestSource(t, adConfig(), dir, nil).Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "member value that is not valid text") {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchValueLengthLimit(t *testing.T) {
	setLimit(t, &maxValueBytes, 16)
	long := strings.Repeat("x", 17)
	exact := strings.Repeat("x", 16)

	dir := newFakeDirectory()
	dir.users = []*goldap.Entry{adUserEntry(t, "CN=Ann,DC=x", testID(1), map[string][]string{
		attrSAMAccount: {"ann"}, attrAccountCtl: {"512"}, attrDisplayName: {exact}})}
	if _, err := newTestSource(t, adConfig(), dir, nil).Fetch(context.Background()); err != nil {
		t.Fatalf("value of exactly the limit rejected: %v", err)
	}

	cases := map[string]*fakeDirectory{}
	d := newFakeDirectory()
	d.users = []*goldap.Entry{adUserEntry(t, "CN=Ann,DC=x", testID(1), map[string][]string{
		attrSAMAccount: {"ann"}, attrAccountCtl: {"512"}, attrDisplayName: {long}})}
	cases["user attribute"] = d
	d = newFakeDirectory()
	d.users = []*goldap.Entry{adUser(t, "CN="+long+",DC=x", testID(1), "ann", "512")}
	cases["user DN"] = d
	d = newFakeDirectory()
	d.groups = []*goldap.Entry{goldap.NewEntry("CN=G,DC=x", map[string][]string{
		attrObjectGUID: {encodeGUID(t, testID(100))}, attrCN: {"G"}, attrDescription: {long}})}
	cases["group attribute"] = d
	d = newFakeDirectory()
	d.groups = []*goldap.Entry{adGroup(t, "CN=G,DC=x", testID(100), "G")}
	d.members["CN=G,DC=x"] = []string{"CN=" + long + ",DC=x"}
	cases["member"] = d

	for name, dir := range cases {
		t.Run(name, func(t *testing.T) {
			snap, err := newTestSource(t, adConfig(), dir, nil).Fetch(context.Background())
			if err == nil || !strings.Contains(err.Error(), "16 bytes") {
				t.Fatalf("err = %v, want a length error", err)
			}
			if strings.Contains(err.Error(), long) || len(snap.Users) != 0 || len(snap.Groups) != 0 {
				t.Errorf("error leaks value or partial snapshot returned: %q %+v", err, snap)
			}
		})
	}
}

func TestMapSnapshotAcceptsNonASCIIText(t *testing.T) {
	sch := mustSchema(t, config.DirectoryTypeOpenLDAP)
	e := ldapUser("uid=z,dc=x", testID(1), "z", map[string][]string{attrDisplayName: {"Zoë Müller 田中"}})
	snap, err := mapSnapshot(sch, []*goldap.Entry{e}, nil, nil)
	if err != nil || snap.Users[0].DisplayName != "Zoë Müller 田中" {
		t.Fatalf("err = %v, snapshot = %+v", err, snap)
	}
}
