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

func TestMapSnapshotAcceptsNonASCIIText(t *testing.T) {
	sch := mustSchema(t, config.DirectoryTypeOpenLDAP)
	e := ldapUser("uid=z,dc=x", testID(1), "z", map[string][]string{attrDisplayName: {"Zoë Müller 田中"}})
	snap, err := mapSnapshot(sch, []*goldap.Entry{e}, nil, nil)
	if err != nil || snap.Users[0].DisplayName != "Zoë Müller 田中" {
		t.Fatalf("err = %v, snapshot = %+v", err, snap)
	}
}
