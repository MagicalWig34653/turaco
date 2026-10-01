package application

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func strp(s string) *string { return &s }

func TestAttributesHashIsStableAndSensitive(t *testing.T) {
	base := SnapshotUser{ExternalID: "a", Username: "u", DistinguishedName: "cn=u", DisplayName: "U", Email: strp("u@example.test"), Enabled: true}
	h := attributesHash(base)
	if h != attributesHash(base) || len(h) != 64 {
		t.Fatalf("hash = %q", h)
	}
	mutations := map[string]func(u *SnapshotUser){
		"username": func(u *SnapshotUser) { u.Username = "v" },
		"dn":       func(u *SnapshotUser) { u.DistinguishedName = "cn=v" },
		"display":  func(u *SnapshotUser) { u.DisplayName = "V" },
		"given":    func(u *SnapshotUser) { u.GivenName = strp("G") },
		"family":   func(u *SnapshotUser) { u.FamilyName = strp("F") },
		"email":    func(u *SnapshotUser) { u.Email = strp("v@example.test") },
		"empno":    func(u *SnapshotUser) { u.EmployeeNumber = strp("1") },
		"enabled":  func(u *SnapshotUser) { u.Enabled = false },
		"absent":   func(u *SnapshotUser) { u.Email = nil },
	}
	for name, mutate := range mutations {
		u := base
		mutate(&u)
		if attributesHash(u) == h {
			t.Errorf("%s change does not change the hash", name)
		}
	}
	// The manager is resolved separately and must not influence the hash.
	u := base
	u.ManagerExternalID = strp("m")
	u.ManagerUnresolved = true
	if attributesHash(u) != h {
		t.Error("manager must not be part of the hash")
	}
	// Field boundaries are unambiguous.
	a := SnapshotUser{Username: "ab", DistinguishedName: "c"}
	b := SnapshotUser{Username: "a", DistinguishedName: "bc"}
	if attributesHash(a) == attributesHash(b) {
		t.Error("ambiguous encoding")
	}
}

func TestPrepareSnapshotNormalizes(t *testing.T) {
	users, groups, reason := prepareSnapshot(DirectorySnapshot{
		Users:  []SnapshotUser{{ExternalID: "a", Username: " u ", DisplayName: "  A  ", Email: strp("   "), GivenName: strp(" G "), Enabled: true}},
		Groups: []SnapshotGroup{{ExternalID: "g", DisplayName: " Staff ", Description: strp("  ")}},
	})
	if reason != "" || users[0].Invalid || users[0].DisplayName != "A" || users[0].Username != "u" || users[0].Email != nil ||
		*users[0].GivenName != "G" || users[0].AttributesHash == "" {
		t.Fatalf("users = %+v reason = %q", users, reason)
	}
	if groups[0].DisplayName != "Staff" || groups[0].Description != nil {
		t.Errorf("groups = %+v", groups)
	}
}

func TestPrepareSnapshotRejectsSnapshotLevelProblems(t *testing.T) {
	bad := map[string]DirectorySnapshot{
		"no users":          {Groups: []SnapshotGroup{{ExternalID: "g", DisplayName: "G"}}},
		"empty user id":     {Users: []SnapshotUser{{DisplayName: "A"}}},
		"dup user":          {Users: []SnapshotUser{{ExternalID: "a", DisplayName: "A"}, {ExternalID: "a", DisplayName: "B"}}},
		"dup across":        {Users: []SnapshotUser{{ExternalID: "a", DisplayName: "A"}}, Groups: []SnapshotGroup{{ExternalID: "a", DisplayName: "G"}}},
		"empty group id":    {Users: []SnapshotUser{{ExternalID: "a", DisplayName: "A"}}, Groups: []SnapshotGroup{{DisplayName: "G"}}},
		"dup group":         {Users: []SnapshotUser{{ExternalID: "a", DisplayName: "A"}}, Groups: []SnapshotGroup{{ExternalID: "g", DisplayName: "G"}, {ExternalID: "g", DisplayName: "H"}}},
		"NUL in user id":    {Users: []SnapshotUser{{ExternalID: "a\x00", DisplayName: "A"}}},
		"bad utf8 group id": {Users: []SnapshotUser{{ExternalID: "a", DisplayName: "A"}}, Groups: []SnapshotGroup{{ExternalID: "g\xff", DisplayName: "G"}}},
	}
	for name, snap := range bad {
		if _, _, reason := prepareSnapshot(snap); reason == "" {
			t.Errorf("%s accepted", name)
		}
	}
	// Reasons are well formed and never contain attribute values.
	_, _, reason := prepareSnapshot(DirectorySnapshot{Users: []SnapshotUser{{ExternalID: "secret-id", DisplayName: "A"}, {ExternalID: "secret-id", DisplayName: "A"}}})
	if strings.Contains(reason, "secret-id") || reason != "users[1]: external id used twice" {
		t.Errorf("reason = %q", reason)
	}
	_, _, reason = prepareSnapshot(DirectorySnapshot{Users: []SnapshotUser{{DisplayName: "A"}}})
	if reason != "users[0]: empty external id" {
		t.Errorf("reason = %q", reason)
	}
}

func TestPrepareUserSanitizesDisplayAndInvalidatesIdentityText(t *testing.T) {
	long := strings.Repeat("é", 3000) // 6000 bytes
	u := prepareUser(SnapshotUser{
		ExternalID: "a", Username: "u", DisplayName: "Ann\x00\tLee\xff", GivenName: strp(long), FamilyName: strp("\x01"),
		DistinguishedName: "cn=a\n", Enabled: true,
	})
	if u.Invalid {
		t.Fatal("display text must not invalidate the account")
	}
	if u.DisplayName != "Ann  Lee�" {
		t.Errorf("display = %q", u.DisplayName)
	}
	if u.GivenName == nil || len(*u.GivenName) > maxTextBytes || !strings.HasSuffix(*u.GivenName, "é") || strings.ContainsRune(*u.GivenName, '�') {
		t.Errorf("given name not truncated at a rune boundary: %d bytes", len(*u.GivenName))
	}
	if u.FamilyName != nil || u.DistinguishedName != "cn=a" {
		t.Errorf("family = %v dn = %q", u.FamilyName, u.DistinguishedName)
	}

	invalid := map[string]SnapshotUser{
		"username NUL":   {ExternalID: "a", Username: "u\x00", DisplayName: "A", Enabled: true},
		"username utf8":  {ExternalID: "a", Username: "u\xff", DisplayName: "A", Enabled: true},
		"email control":  {ExternalID: "a", Username: "u", DisplayName: "A", Email: strp("a@b\n.test"), Enabled: true},
		"email too long": {ExternalID: "a", Username: "u", DisplayName: "A", Email: strp(strings.Repeat("a", 4097)), Enabled: true},
		"empno":          {ExternalID: "a", Username: "u", DisplayName: "A", EmployeeNumber: strp("1\x7f"), Enabled: true},
		"source marks":   {ExternalID: "a", Username: "u", DisplayName: "A", Invalid: true, Enabled: true},
		"empty display":  {ExternalID: "a", Username: "u", DisplayName: " \x00 ", Enabled: true},
	}
	for name, in := range invalid {
		got := prepareUser(in)
		if !got.Invalid || got.Username != "" || got.Email != nil || got.DisplayName != "" || got.AttributesHash != "" || !got.Enabled || got.ExternalID != "a" {
			t.Errorf("%s: %+v", name, got)
		}
	}
	// A manager reference that is not text becomes unresolved instead of reaching the database.
	m := prepareUser(SnapshotUser{ExternalID: "a", Username: "u", DisplayName: "A", ManagerExternalID: strp("m\x00")})
	if m.Invalid || m.ManagerExternalID != nil || !m.ManagerUnresolved {
		t.Errorf("manager = %+v", m)
	}
}

func TestPrepareGroupSanitizesAndDropsBadReferences(t *testing.T) {
	g := prepareGroup(SnapshotGroup{
		ExternalID: "g", DisplayName: "\x00", Description: strp("d\x00e"),
		MemberUserIDs: []string{"u1", "u\x00", ""}, MemberGroupIDs: []string{"g\xff"}, UnresolvedMembers: 1,
	})
	if g.DisplayName != "g" || g.Description == nil || *g.Description != "d e" {
		t.Errorf("group = %+v", g)
	}
	if len(g.MemberUserIDs) != 1 || len(g.MemberGroupIDs) != 0 || g.UnresolvedMembers != 4 {
		t.Errorf("members = %+v", g)
	}
}

func TestSweepWithheld(t *testing.T) {
	tests := []struct {
		name                     string
		missing, before, percent int
		want                     bool
	}{
		{"none missing", 0, 10, 10, false},
		{"exactly five is never withheld", 5, 5, 0, false},
		{"six of ten over 10 percent", 6, 10, 10, true},
		{"six of sixty is exactly 10 percent", 6, 60, 10, false},
		{"seven of sixty over 10 percent", 7, 60, 10, true},
		{"all missing of a large directory", 100, 100, 100, false},
		{"over the minimum with percent zero", 6, 1000, 0, true},
		{"empty before", 6, 0, 10, true},
	}
	for _, tt := range tests {
		if got := SweepWithheld(tt.missing, tt.before, tt.percent); got != tt.want {
			t.Errorf("%s: SweepWithheld(%d, %d, %d) = %v, want %v", tt.name, tt.missing, tt.before, tt.percent, got, tt.want)
		}
	}
}

func TestDecideEmailChange(t *testing.T) {
	key := "a@x.test"
	tests := []struct {
		name    string
		desired *string
		owner   string
		user    string
		want    EmailDecision
	}{
		{"no email", nil, "", "u1", EmailApply},
		{"free", &key, "", "u1", EmailApply},
		{"own address", &key, "u1", "u1", EmailApply},
		{"taken by another user", &key, "u2", "u1", EmailConflict},
		{"new account, free", &key, "", "", EmailApply},
		{"new account, taken", &key, "u2", "", EmailConflict},
		{"new account, no email", nil, "u2", "", EmailApply},
	}
	for _, tt := range tests {
		if got := DecideEmailChange(tt.desired, tt.owner, tt.user); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

type fakeStore struct {
	startErr   error
	keyChanged bool
	keyErr     error
	applyErr   error
	applyOut   SyncApplyOutput
	started    int
	applied    int
	finished   []SyncRunFinish
	applyInput SyncApplyInput
	startedAt  time.Time
	abandoned  time.Time
	jobID      string
}

func (s *fakeStore) StartRun(_ context.Context, _ string, _ SyncTrigger, jobID string, startedAt, abandonedBefore time.Time) (string, error) {
	s.started++
	s.jobID = jobID
	s.startedAt, s.abandoned = startedAt, abandonedBefore
	return "run-1", s.startErr
}
func (s *fakeStore) ProviderKeyChanged(context.Context, string) (bool, error) {
	return s.keyChanged, s.keyErr
}
func (s *fakeStore) ApplySnapshot(_ context.Context, in SyncApplyInput) (SyncApplyOutput, error) {
	s.applied++
	s.applyInput = in
	if s.applyOut.Outcome == "" {
		s.applyOut = SyncApplyOutput{Outcome: SyncOutcomeSucceeded, Counts: map[string]int{"usersCreated": 1}}
	}
	return s.applyOut, s.applyErr
}
func (s *fakeStore) FinishRun(_ context.Context, f SyncRunFinish) error {
	s.finished = append(s.finished, f)
	return nil
}

type fakeSource struct {
	snap DirectorySnapshot
	err  error
	key  string
}

func (s fakeSource) ProviderKey() string {
	if s.key != "" {
		return s.key
	}
	return "ad"
}
func (s fakeSource) Fetch(context.Context) (DirectorySnapshot, error) { return s.snap, s.err }

func newTestSync(store DirectorySyncStore, cfg DirectorySyncConfig) *DirectorySync {
	t := time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC)
	return NewDirectorySync(store, cfg, func() time.Time { return t }, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestRunErrorClassification(t *testing.T) {
	snap := DirectorySnapshot{Users: []SnapshotUser{{ExternalID: "a", DisplayName: "A"}}}
	ctx := context.Background()

	t.Run("already running", func(t *testing.T) {
		st := &fakeStore{startErr: ErrSyncAlreadyRunning}
		res, err := newTestSync(st, DirectorySyncConfig{}).Run(ctx, fakeSource{snap: snap}, SyncTriggerScheduled, "")
		if !errors.Is(err, ErrSyncAlreadyRunning) || st.applied != 0 || len(st.finished) != 0 || res.RunID != "" {
			t.Fatalf("res=%+v err=%v", res, err)
		}
	})
	t.Run("provider key changed fails the run before fetching", func(t *testing.T) {
		st := &fakeStore{keyChanged: true}
		src := fakeSource{err: errors.New("must not be fetched")}
		res, err := newTestSync(st, DirectorySyncConfig{}).Run(ctx, src, SyncTriggerScheduled, "")
		if !errors.Is(err, ErrProviderKeyChanged) || res.Outcome != SyncOutcomeFailed || st.applied != 0 ||
			len(st.finished) != 1 || !strings.Contains(st.finished[0].Error, "LDAP_PROVIDER_KEY") {
			t.Fatalf("res=%+v err=%v finished=%+v", res, err, st.finished)
		}
	})
	t.Run("provider key check error is retryable", func(t *testing.T) {
		st := &fakeStore{keyErr: errors.New("db")}
		_, err := newTestSync(st, DirectorySyncConfig{}).Run(ctx, fakeSource{snap: snap}, SyncTriggerScheduled, "")
		if err == nil || errors.Is(err, ErrProviderKeyChanged) || len(st.finished) != 1 {
			t.Fatalf("err=%v finished=%+v", err, st.finished)
		}
	})
	t.Run("fetch error fails the run", func(t *testing.T) {
		st := &fakeStore{}
		_, err := newTestSync(st, DirectorySyncConfig{}).Run(ctx, fakeSource{err: errors.New("boom")}, SyncTriggerScheduled, "")
		if err == nil || len(st.finished) != 1 || st.finished[0].Outcome != SyncOutcomeFailed || !strings.Contains(st.finished[0].Error, "boom") {
			t.Fatalf("err=%v finished=%+v", err, st.finished)
		}
	})
	t.Run("invalid snapshot", func(t *testing.T) {
		st := &fakeStore{}
		_, err := newTestSync(st, DirectorySyncConfig{}).Run(ctx, fakeSource{}, SyncTriggerScheduled, "")
		if !errors.Is(err, ErrInvalidSnapshot) || st.applied != 0 || len(st.finished) != 1 {
			t.Fatalf("err=%v applied=%d finished=%+v", err, st.applied, st.finished)
		}
	})
	t.Run("sweep withheld is success", func(t *testing.T) {
		st := &fakeStore{applyOut: SyncApplyOutput{Outcome: SyncOutcomeSweepWithheld, Counts: map[string]int{"usersSweepWithheld": 7}}}
		res, err := newTestSync(st, DirectorySyncConfig{}).Run(ctx, fakeSource{snap: snap}, SyncTriggerManual, "")
		if err != nil || res.Outcome != SyncOutcomeSweepWithheld || res.Counts["usersSweepWithheld"] != 7 || len(st.finished) != 0 {
			t.Fatalf("res=%+v err=%v finished=%+v", res, err, st.finished)
		}
	})
	t.Run("apply error fails the run", func(t *testing.T) {
		st := &fakeStore{applyErr: errors.New("db down")}
		res, err := newTestSync(st, DirectorySyncConfig{}).Run(ctx, fakeSource{snap: snap}, SyncTriggerScheduled, "")
		if err == nil || res.Outcome != SyncOutcomeFailed || len(st.finished) != 1 || st.finished[0].Outcome != SyncOutcomeFailed {
			t.Fatalf("res=%+v err=%v finished=%+v", res, err, st.finished)
		}
	})
	t.Run("success passes normalized input, config and job id", func(t *testing.T) {
		st := &fakeStore{}
		res, err := newTestSync(st, DirectorySyncConfig{MaxMissingPercent: 250, RunTimeout: time.Minute}).Run(ctx, fakeSource{snap: snap}, SyncTriggerScheduled, "job-9")
		if err != nil || res.Outcome != SyncOutcomeSucceeded || res.RunID != "run-1" || res.Counts["usersCreated"] != 1 {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		in := st.applyInput
		if in.MaxMissingPercent != 100 || in.ProviderKey != "ad" || len(in.Users) != 1 || in.Users[0].AttributesHash == "" || in.Now == nil || st.jobID != "job-9" {
			t.Errorf("input = %+v job = %q", in, st.jobID)
		}
		// Timestamps are truncated to what PostgreSQL stores; abandonment uses the run timeout.
		if st.startedAt.Nanosecond()%1000 != 0 || st.startedAt.Sub(st.abandoned) != time.Minute || in.FetchedAt.Nanosecond()%1000 != 0 {
			t.Errorf("startedAt=%v abandoned=%v", st.startedAt, st.abandoned)
		}
	})
	t.Run("invalid requests start no run", func(t *testing.T) {
		st := &fakeStore{}
		if _, err := newTestSync(st, DirectorySyncConfig{}).Run(ctx, fakeSource{snap: snap}, "bogus", ""); !errors.Is(err, ErrInvalidRequest) || st.started != 0 {
			t.Fatalf("trigger: err=%v started=%d", err, st.started)
		}
		st.keyChanged = false
		src := fakeSource{snap: snap}
		src.key = ""
		if _, err := newTestSync(st, DirectorySyncConfig{}).Run(ctx, emptyKey{}, SyncTriggerManual, ""); !errors.Is(err, ErrInvalidRequest) || st.started != 0 {
			t.Fatalf("provider key: err=%v started=%d", err, st.started)
		}
	})
}

type emptyKey struct{}

func (emptyKey) ProviderKey() string                              { return "" }
func (emptyKey) Fetch(context.Context) (DirectorySnapshot, error) { return DirectorySnapshot{}, nil }

func TestSanitizeErrorTruncatesAndStripsControlCharacters(t *testing.T) {
	got := sanitizeError(errors.New("a\nb\t" + strings.Repeat("é", 400)))
	if len(got) > maxStoredErrorLen || strings.ContainsAny(got, "\n\t") || !strings.HasPrefix(got, "a b ") {
		t.Errorf("sanitized = %q (%d)", got, len(got))
	}
	if !strings.HasSuffix(got, "é") {
		t.Error("UTF-8 sequence split")
	}
}
