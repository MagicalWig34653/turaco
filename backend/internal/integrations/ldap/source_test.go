package ldap

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
)

const (
	annDN   = "CN=Ann Admin,OU=Users,DC=example,DC=test"
	benDN   = "CN=Ben Boss,OU=Users,DC=example,DC=test"
	staffDN = "CN=Staff,OU=Groups,DC=example,DC=test"
)

func basicDirectory(t testing.TB) *fakeDirectory {
	dir := newFakeDirectory()
	dir.users = []*goldap.Entry{
		adUserEntry(t, annDN, testID(1), map[string][]string{
			attrSAMAccount: {"ann"}, attrAccountCtl: {"512"}, attrManager: {benDN}, attrMail: {"ann@example.test"},
		}),
		adUser(t, benDN, testID(2), "ben", "514"),
	}
	dir.groups = []*goldap.Entry{adGroup(t, staffDN, testID(100), "Staff")}
	dir.members[staffDN] = []string{annDN, benDN, "CN=Nobody,DC=elsewhere"}
	return dir
}

func TestSourceProviderKey(t *testing.T) {
	cfg := adConfig()
	cfg.ProviderKey = "corp-ad"
	s := newTestSource(t, cfg, newFakeDirectory(), nil)
	if s.ProviderKey() != "corp-ad" {
		t.Fatalf("ProviderKey = %q", s.ProviderKey())
	}
	var _ public.DirectorySource = s
}

func TestFetchActiveDirectorySnapshot(t *testing.T) {
	dir := basicDirectory(t)
	cfg := adConfig()
	s := newTestSource(t, cfg, dir, nil)

	snap, err := s.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(snap.Users) != 2 || len(snap.Groups) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	ann, ben := snap.Users[0], snap.Users[1]
	if ann.Username != "ann" || !ann.Enabled || ann.ManagerExternalID == nil || *ann.ManagerExternalID != testID(2) {
		t.Errorf("ann = %+v", ann)
	}
	if ben.Username != "ben" || ben.Enabled {
		t.Errorf("ben = %+v", ben)
	}
	g := snap.Groups[0]
	if !reflect.DeepEqual(g.MemberUserIDs, []string{testID(1), testID(2)}) || g.UnresolvedMembers != 1 {
		t.Errorf("group = %+v", g)
	}

	if dir.boundUser != cfg.BindDN || dir.boundPass != testBindPassword {
		t.Errorf("bind used %q / <password match=%v>", dir.boundUser, dir.boundPass == testBindPassword)
	}
	if dir.startTLS != 0 {
		t.Error("StartTLS called for an ldaps URL")
	}
	if dir.closeCount() != 1 {
		t.Errorf("connection closed %d times, want exactly 1", dir.closeCount())
	}

	searches := dir.snapshotSearches()
	if len(searches) != 2 {
		t.Fatalf("searches = %+v", searches)
	}
	wantUsers := recordedSearch{baseDN: cfg.UserBaseDN, scope: goldap.ScopeWholeSubtree, filter: cfg.UserFilter,
		attrs: mustSchema(t, config.DirectoryTypeActiveDirectory).userAttrs, pageSize: 500, hasPaging: true, timeLimit: int(defaultRequestTimeout / time.Second)}
	wantGroups := recordedSearch{baseDN: cfg.GroupBaseDN, scope: goldap.ScopeWholeSubtree, filter: cfg.GroupFilter,
		attrs: mustSchema(t, config.DirectoryTypeActiveDirectory).groupAttrs, pageSize: 500, hasPaging: true, timeLimit: int(defaultRequestTimeout / time.Second)}
	if !reflect.DeepEqual(searches[0], wantUsers) {
		t.Errorf("user search = %+v\nwant        %+v", searches[0], wantUsers)
	}
	if !reflect.DeepEqual(searches[1], wantGroups) {
		t.Errorf("group search = %+v\nwant         %+v", searches[1], wantGroups)
	}
}

func TestFetchOnlyRequestsAllowlistedAttributes(t *testing.T) {
	dir := basicDirectory(t)
	dir.rangeSize = 1
	s := newTestSource(t, adConfig(), dir, nil)
	if _, err := s.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{}
	for _, a := range s.schema.userAttrs {
		allowed[strings.ToLower(a)] = true
	}
	for _, a := range s.schema.groupAttrs {
		allowed[strings.ToLower(a)] = true
	}
	for _, req := range dir.snapshotSearches() {
		if len(req.attrs) == 0 {
			t.Errorf("search without attribute list requests all attributes: %+v", req)
		}
		for _, a := range req.attrs {
			if strings.HasPrefix(strings.ToLower(a), "member;range=") {
				continue
			}
			if !allowed[strings.ToLower(a)] || a == "*" {
				t.Errorf("attribute %q is not in the allowlist", a)
			}
		}
	}
}

func TestFetchOpenLDAPSnapshot(t *testing.T) {
	dir := newFakeDirectory()
	dir.users = []*goldap.Entry{
		ldapUser("uid=bob,ou=people,dc=example,dc=test", "A1B2C3D4-E5F6-4789-ABCD-EF0123456789", "bob", map[string][]string{attrCN: {"Bob"}}),
	}
	dir.groups = []*goldap.Entry{ldapGroup("cn=ops,ou=groups,dc=example,dc=test", testID(7), "ops")}
	dir.members["cn=ops,ou=groups,dc=example,dc=test"] = []string{"UID=Bob,OU=People,DC=example,DC=test"}
	s := newTestSource(t, openLDAPConfig(), dir, nil)

	snap, err := s.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Users[0].ExternalID != "a1b2c3d4-e5f6-4789-abcd-ef0123456789" || !snap.Users[0].Enabled {
		t.Errorf("user = %+v", snap.Users[0])
	}
	if !reflect.DeepEqual(snap.Groups[0].MemberUserIDs, []string{"a1b2c3d4-e5f6-4789-abcd-ef0123456789"}) {
		t.Errorf("group = %+v", snap.Groups[0])
	}
	searches := dir.snapshotSearches()
	if searches[0].attrs[0] != "entryUUID" || searches[0].filter != "(objectClass=inetOrgPerson)" {
		t.Errorf("user search = %+v", searches[0])
	}
}

func TestFetchStartTLS(t *testing.T) {
	cfg := adConfig()
	cfg.URL = "ldap://dc.example.test:389"
	cfg.StartTLS = true
	dir := basicDirectory(t)
	s := newTestSource(t, cfg, dir, nil)
	if _, err := s.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if dir.startTLS != 1 || dir.startTLSCfg != s.tlsConfig {
		t.Fatalf("StartTLS calls = %d, config = %p, want 1 call with the source TLS config", dir.startTLS, dir.startTLSCfg)
	}
	if dir.startTLSCfg.InsecureSkipVerify || dir.startTLSCfg.ServerName != "dc.example.test" {
		t.Errorf("StartTLS config = %+v", dir.startTLSCfg)
	}
}

func TestFetchStartTLSFailureClosesConnectionAndDoesNotBind(t *testing.T) {
	cfg := adConfig()
	cfg.URL = "ldap://dc.example.test:389"
	cfg.StartTLS = true
	dir := basicDirectory(t)
	dir.startTLSErr = goldap.NewError(goldap.ErrorNetwork, errors.New("x509: certificate is valid for secret.host, not dc.example.test"))
	s := newTestSource(t, cfg, dir, nil)

	_, err := s.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ldap: starttls") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "secret.host") {
		t.Errorf("error leaks server diagnostics: %v", err)
	}
	if dir.boundUser != "" || len(dir.snapshotSearches()) != 0 {
		t.Error("bind or search happened after failed StartTLS")
	}
	if dir.closeCount() != 1 {
		t.Errorf("close count = %d", dir.closeCount())
	}
}

func TestFetchRangeRetrievalAcrossMultiplePages(t *testing.T) {
	dir := newFakeDirectory()
	var userEntries []*goldap.Entry
	var memberDNs []string
	const total = 7
	for i := 1; i <= total; i++ {
		dn := fmt.Sprintf("CN=User %d,OU=Users,DC=example,DC=test", i)
		userEntries = append(userEntries, adUser(t, dn, testID(i), fmt.Sprintf("u%d", i), "512"))
		memberDNs = append(memberDNs, strings.ToLower(dn)) // also exercises case-insensitive resolution
	}
	dir.users = userEntries
	dir.groups = []*goldap.Entry{adGroup(t, staffDN, testID(100), "Staff")}
	dir.members[staffDN] = memberDNs
	dir.rangeSize = 3 // pages: 0-2, 3-5, 6-*
	s := newTestSource(t, adConfig(), dir, nil)

	snap, err := s.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for i := 1; i <= total; i++ {
		want = append(want, testID(i))
	}
	if got := snap.Groups[0].MemberUserIDs; !reflect.DeepEqual(got, want) {
		t.Fatalf("members = %v, want %v", got, want)
	}
	if snap.Groups[0].UnresolvedMembers != 0 {
		t.Errorf("unresolved = %d", snap.Groups[0].UnresolvedMembers)
	}

	searches := dir.snapshotSearches()
	if len(searches) != 4 {
		t.Fatalf("searches = %+v, want users, groups and 2 range requests", searches)
	}
	for i, wantAttr := range []string{"member;range=3-*", "member;range=6-*"} {
		r := searches[2+i]
		if r.baseDN != staffDN || r.scope != goldap.ScopeBaseObject || r.hasPaging || !reflect.DeepEqual(r.attrs, []string{wantAttr}) {
			t.Errorf("range request %d = %+v, want base search for %s", i, r, wantAttr)
		}
	}
}

func TestFetchRangeRetrievalExactMultipleOfPageSize(t *testing.T) {
	dir := newFakeDirectory()
	var memberDNs []string
	for i := 1; i <= 6; i++ {
		dn := fmt.Sprintf("CN=User %d,OU=Users,DC=example,DC=test", i)
		dir.users = append(dir.users, adUser(t, dn, testID(i), fmt.Sprintf("u%d", i), "512"))
		memberDNs = append(memberDNs, dn)
	}
	dir.groups = []*goldap.Entry{adGroup(t, staffDN, testID(100), "Staff")}
	dir.members[staffDN] = memberDNs
	dir.rangeSize = 3 // pages: 0-2, 3-*
	s := newTestSource(t, adConfig(), dir, nil)
	snap, err := s.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Groups[0].MemberUserIDs) != 6 {
		t.Fatalf("members = %v", snap.Groups[0].MemberUserIDs)
	}
}

func TestFetchRangeRetrievalFailures(t *testing.T) {
	setup := func(t *testing.T) *fakeDirectory {
		dir := newFakeDirectory()
		var memberDNs []string
		for i := 1; i <= 7; i++ {
			dn := fmt.Sprintf("CN=User %d,OU=Users,DC=example,DC=test", i)
			dir.users = append(dir.users, adUser(t, dn, testID(i), fmt.Sprintf("u%d", i), "512"))
			memberDNs = append(memberDNs, dn)
		}
		dir.groups = []*goldap.Entry{adGroup(t, staffDN, testID(100), "Staff")}
		dir.members[staffDN] = memberDNs
		dir.rangeSize = 3
		return dir
	}
	tests := []struct {
		name    string
		prepare func(*fakeDirectory)
		want    string
	}{
		{"search error", func(d *fakeDirectory) {
			d.rangeSearchEr = goldap.NewError(goldap.LDAPResultBusy, errors.New("server says CN=Leak"))
		}, "ldap: search group members: result code 51 (Busy)"},
		{"unexpected start", func(d *fakeDirectory) {
			d.mutateRange = func(a *goldap.EntryAttribute) { a.Name = "member;range=100-103" }
		}, "unexpected member range"},
		{"no progress", func(d *fakeDirectory) {
			d.mutateRange = func(a *goldap.EntryAttribute) {
				a.Name = strings.Replace(a.Name, "-*", "-4", 1) // not the last range, but no values
				a.Values = nil
			}
		}, "made no progress"},
		{"range attribute missing", func(d *fakeDirectory) {
			d.mutateRange = func(a *goldap.EntryAttribute) { a.Name = "description" }
		}, "unexpected member range"},
		{"malformed range", func(d *fakeDirectory) {
			d.mutateRange = func(a *goldap.EntryAttribute) { a.Name = "member;range=x-y" }
		}, "malformed member range"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := setup(t)
			tt.prepare(dir)
			s := newTestSource(t, adConfig(), dir, nil)
			snap, err := s.Fetch(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "Leak") || strings.Contains(err.Error(), "CN=") {
				t.Errorf("error leaks directory data: %v", err)
			}
			if len(snap.Users) != 0 || len(snap.Groups) != 0 {
				t.Error("partial snapshot returned")
			}
			if dir.closeCount() != 1 {
				t.Errorf("close count = %d", dir.closeCount())
			}
		})
	}
}

func TestFetchBindFailureDoesNotLeakPasswordOrDiagnostics(t *testing.T) {
	dir := basicDirectory(t)
	dir.bindErr = goldap.NewError(goldap.LDAPResultInvalidCredentials,
		fmt.Errorf("80090308: LdapErr: DSID-0C09044E, data 52e for CN=svc-turaco password %s", testBindPassword))
	var logs bytes.Buffer
	s := newTestSource(t, adConfig(), dir, slog.New(slog.NewTextHandler(&logs, nil)))

	snap, err := s.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected bind error")
	}
	for _, want := range []string{"ldap: bind", "49", "Invalid Credentials"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	for _, leak := range []string{testBindPassword, "DSID", "80090308", "svc-turaco", "CN="} {
		if strings.Contains(err.Error(), leak) || strings.Contains(logs.String(), leak) {
			t.Errorf("error/log leaks %q: %v / %s", leak, err, logs.String())
		}
	}
	if len(snap.Users) != 0 || len(dir.snapshotSearches()) != 0 {
		t.Error("search ran after failed bind")
	}
	if dir.closeCount() != 1 {
		t.Errorf("close count = %d", dir.closeCount())
	}
}

func TestFetchSearchErrorDiscardsPartialResults(t *testing.T) {
	dir := basicDirectory(t)
	dir.userSearchPartial = dir.users
	dir.userSearchErr = goldap.NewError(goldap.LDAPResultSizeLimitExceeded, errors.New("limit for CN=Ann Admin"))
	s := newTestSource(t, adConfig(), dir, nil)

	snap, err := s.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ldap: search users: result code 4 (Size Limit Exceeded)") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "Ann") {
		t.Errorf("error leaks directory data: %v", err)
	}
	if len(snap.Users) != 0 || len(snap.Groups) != 0 {
		t.Fatal("partial snapshot returned")
	}
}

func TestFetchGroupSearchError(t *testing.T) {
	dir := basicDirectory(t)
	dir.groupSearchEr = goldap.NewError(goldap.LDAPResultNoSuchObject, errors.New("matched OU=Groups"))
	s := newTestSource(t, adConfig(), dir, nil)
	_, err := s.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ldap: search groups: result code 32") || strings.Contains(err.Error(), "OU=") {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchDialError(t *testing.T) {
	dir := basicDirectory(t)
	dir.dialErr = goldap.NewError(goldap.ErrorNetwork, errors.New("dial tcp 10.1.2.3:636: connect: connection refused"))
	s := newTestSource(t, adConfig(), dir, nil)
	_, err := s.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ldap: connect: result code 200 (Network Error)") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "10.1.2.3") {
		t.Errorf("error leaks the server address: %v", err)
	}
}

func TestFetchInvalidDataIsAnErrorNotAPartialSnapshot(t *testing.T) {
	dir := basicDirectory(t)
	dir.users = append(dir.users, goldap.NewEntry("CN=No Guid,OU=Users,DC=example,DC=test",
		map[string][]string{attrSAMAccount: {"noguid"}, attrAccountCtl: {"512"}}))
	s := newTestSource(t, adConfig(), dir, nil)
	snap, err := s.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "1 entries with missing or invalid objectGUID") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "noguid") || strings.Contains(err.Error(), "No Guid") {
		t.Errorf("error leaks directory data: %v", err)
	}
	if len(snap.Users) != 0 {
		t.Fatal("partial snapshot returned")
	}
	if dir.closeCount() != 1 {
		t.Errorf("close count = %d", dir.closeCount())
	}
}

func TestFetchAlreadyCancelledContext(t *testing.T) {
	dir := basicDirectory(t)
	s := newTestSource(t, adConfig(), dir, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Fetch(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if dir.dials != 0 {
		t.Error("dialed with a cancelled context")
	}
}

func TestFetchCancelledWhileSearching(t *testing.T) {
	dir := basicDirectory(t)
	dir.blockUsers = true
	s := newTestSource(t, adConfig(), dir, nil)
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		_, err := s.Fetch(ctx)
		errCh <- err
	}()
	// Wait until the search is blocked.
	deadline := time.Now().Add(5 * time.Second)
	for len(dir.snapshotSearches()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("search never started")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Fetch did not return after cancellation")
	}
	if dir.closeCount() != 1 {
		t.Errorf("close count = %d, want 1", dir.closeCount())
	}
}

func TestFetchDeadlineExceededWhileSearching(t *testing.T) {
	dir := basicDirectory(t)
	dir.blockUsers = true
	s := newTestSource(t, adConfig(), dir, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := s.Fetch(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestFetchCancelledWhileDialing(t *testing.T) {
	dir := basicDirectory(t)
	dir.dialBlock = make(chan struct{})
	s := newTestSource(t, adConfig(), dir, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := s.Fetch(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	// The late connection must still be closed.
	close(dir.dialBlock)
	deadline := time.Now().Add(5 * time.Second)
	for dir.closeCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("connection established after cancellation was never closed")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestFetchSetsRequestTimeoutFromDeadline(t *testing.T) {
	dir := basicDirectory(t)
	s := newTestSource(t, adConfig(), dir, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := s.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	if len(dir.timeouts) == 0 {
		t.Fatal("SetTimeout never called")
	}
	for _, d := range dir.timeouts {
		if d <= 0 || d > time.Minute {
			t.Errorf("timeout %v outside (0, 1m]", d)
		}
	}
	// Every search carries a server-side time limit derived from the deadline.
	searches := dir.snapshotSearches()
	if len(searches) == 0 {
		t.Fatal("no searches recorded")
	}
	for _, r := range searches {
		if r.timeLimit < 59 || r.timeLimit > 60 {
			t.Errorf("search time limit = %d, want 59..60 seconds for a one-minute deadline", r.timeLimit)
		}
	}

	// Without a deadline a bounded default applies.
	dir2 := basicDirectory(t)
	s2 := newTestSource(t, adConfig(), dir2, nil)
	if _, err := s2.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, d := range dir2.timeouts {
		if d != defaultRequestTimeout {
			t.Errorf("timeout without deadline = %v, want %v", d, defaultRequestTimeout)
		}
	}
}

func TestFetchLogsOnlyCountsAndDurations(t *testing.T) {
	dir := basicDirectory(t)
	var logs bytes.Buffer
	s := newTestSource(t, adConfig(), dir, slog.New(slog.NewTextHandler(&logs, nil)))
	if _, err := s.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	for _, want := range []string{"users=2", "groups=1", "duration="} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q missing %q", out, want)
		}
	}
	for _, leak := range []string{testBindPassword, "CN=", "ann@example.test", "ann", "svc-turaco"} {
		if strings.Contains(out, leak) {
			t.Errorf("log leaks %q: %s", leak, out)
		}
	}
}

func TestFetchIsRepeatableAndUsesAFreshConnection(t *testing.T) {
	dir := basicDirectory(t)
	s := newTestSource(t, adConfig(), dir, nil)
	first, err := s.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Error("snapshots differ between identical fetches")
	}
	if dir.dials != 2 || dir.closeCount() != 2 {
		t.Errorf("dials = %d, closes = %d, want 2 and 2", dir.dials, dir.closeCount())
	}
}

func TestNewSourceValidation(t *testing.T) {
	good := adConfig()
	tests := []struct {
		name     string
		mutate   func(*config.LDAPConfig)
		password string
	}{
		{"disabled", func(c *config.LDAPConfig) { *c = config.LDAPConfig{} }, testBindPassword},
		{"empty password", func(c *config.LDAPConfig) {}, ""},
		{"unknown directory type", func(c *config.LDAPConfig) { c.DirectoryType = "novell" }, testBindPassword},
		{"missing bind dn", func(c *config.LDAPConfig) { c.BindDN = "" }, testBindPassword},
		{"missing user base", func(c *config.LDAPConfig) { c.UserBaseDN = "" }, testBindPassword},
		{"missing group filter", func(c *config.LDAPConfig) { c.GroupFilter = "" }, testBindPassword},
		{"missing provider key", func(c *config.LDAPConfig) { c.ProviderKey = "" }, testBindPassword},
		{"starttls with ldaps", func(c *config.LDAPConfig) { c.StartTLS = true }, testBindPassword},
		{"unsupported scheme", func(c *config.LDAPConfig) { c.URL = "http://dc.example.test" }, testBindPassword},
		{"url without host", func(c *config.LDAPConfig) { c.URL = "ldaps://" }, testBindPassword},
		{"plaintext without opt-in", func(c *config.LDAPConfig) { c.URL = "ldap://dc.example.test:389" }, testBindPassword},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := good
			tt.mutate(&cfg)
			s, err := NewSource(cfg, tt.password, nil)
			if err == nil {
				t.Fatalf("expected error, got source %+v", s)
			}
			if strings.Contains(err.Error(), testBindPassword) {
				t.Errorf("error leaks password: %v", err)
			}
		})
	}
	if _, err := NewSource(good, testBindPassword, nil); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestNewSourcePlaintextRules(t *testing.T) {
	plain := adConfig()
	plain.URL = "ldap://dc.example.test:389"

	if _, err := NewSource(plain, testBindPassword, nil); err == nil {
		t.Error("ldap:// without StartTLS and without AllowPlaintext was accepted")
	} else if strings.Contains(err.Error(), testBindPassword) || strings.Contains(err.Error(), "dc.example.test") {
		t.Errorf("error leaks configuration: %v", err)
	}

	startTLS := plain
	startTLS.StartTLS = true
	if _, err := NewSource(startTLS, testBindPassword, nil); err != nil {
		t.Errorf("ldap:// with StartTLS rejected: %v", err)
	}

	var logs bytes.Buffer
	allowed := plain
	allowed.AllowPlaintext = true
	if _, err := NewSource(allowed, testBindPassword, slog.New(slog.NewTextHandler(&logs, nil))); err != nil {
		t.Errorf("ldap:// with AllowPlaintext rejected: %v", err)
	}
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("allowed plaintext must still warn, log = %q", logs.String())
	}

	// AllowPlaintext never relaxes ldaps:// or the StartTLS conflict check.
	ldaps := adConfig()
	ldaps.AllowPlaintext = true
	ldaps.StartTLS = true
	if _, err := NewSource(ldaps, testBindPassword, nil); err == nil {
		t.Error("ldaps:// with StartTLS accepted")
	}
}

func TestNewSourceWarnsAboutUnencryptedConnection(t *testing.T) {
	cfg := adConfig()
	cfg.URL = "ldap://dc.example.test:389"
	cfg.AllowPlaintext = true
	var logs bytes.Buffer
	if _, err := NewSource(cfg, testBindPassword, slog.New(slog.NewTextHandler(&logs, nil))); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "not encrypted") || strings.Contains(logs.String(), testBindPassword) {
		t.Errorf("log = %q", logs.String())
	}
}

func TestParseRange(t *testing.T) {
	tests := []struct {
		in   string
		want rangedAttr
		ok   bool
	}{
		{"member;range=0-1499", rangedAttr{low: 0, high: 1499}, true},
		{"member;range=1500-*", rangedAttr{low: 1500, last: true}, true},
		{"member;range=0-*", rangedAttr{low: 0, last: true}, true},
		{"member;range=5-4", rangedAttr{}, false},
		{"member;range=-1-5", rangedAttr{}, false},
		{"member;range=a-b", rangedAttr{}, false},
		{"member;range=10", rangedAttr{}, false},
		{"member;range=", rangedAttr{}, false},
	}
	for _, tt := range tests {
		got, ok := parseRange(tt.in)
		if ok != tt.ok || got.low != tt.want.low || got.high != tt.want.high || got.last != tt.want.last {
			t.Errorf("parseRange(%q) = %+v, %v; want %+v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestOpErrorTransportReasons(t *testing.T) {
	ctx := context.Background()
	tlsErr := goldap.NewError(goldap.ErrorNetwork, fmt.Errorf("tls: %w", x509.UnknownAuthorityError{}))
	err := opError(ctx, "connect", tlsErr)
	if err.Error() != "ldap: connect: result code 200 (Network Error): certificate signed by unknown authority" {
		t.Errorf("error = %q", err)
	}
	plain := opError(ctx, "bind", errors.New("some failure with CN=Leak"))
	if plain.Error() != "ldap: bind: unexpected error" {
		t.Errorf("plain error = %q", plain)
	}
}
