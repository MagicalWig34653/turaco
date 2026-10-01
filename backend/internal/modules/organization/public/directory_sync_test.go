package public

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

func strp(s string) *string { return &s }

func TestAttributesHashIsStableAndSensitive(t *testing.T) {
	base := DirectoryUser{ExternalID: "a", Username: "u", DistinguishedName: "cn=u", DisplayName: "U", Email: strp("u@example.test"), Enabled: true}
	h := attributesHash(base)
	if h != attributesHash(base) || len(h) != 64 {
		t.Fatalf("hash = %q", h)
	}
	mutations := map[string]func(u *DirectoryUser){
		"username": func(u *DirectoryUser) { u.Username = "v" },
		"dn":       func(u *DirectoryUser) { u.DistinguishedName = "cn=v" },
		"display":  func(u *DirectoryUser) { u.DisplayName = "V" },
		"given":    func(u *DirectoryUser) { u.GivenName = strp("G") },
		"family":   func(u *DirectoryUser) { u.FamilyName = strp("F") },
		"email":    func(u *DirectoryUser) { u.Email = strp("v@example.test") },
		"empno":    func(u *DirectoryUser) { u.EmployeeNumber = strp("1") },
		"enabled":  func(u *DirectoryUser) { u.Enabled = false },
		// absent and empty-after-trim are the same; absent and "x" are not.
		"empty vs value": func(u *DirectoryUser) { u.Email = nil },
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
	a := DirectoryUser{Username: "ab", DistinguishedName: "c"}
	b := DirectoryUser{Username: "a", DistinguishedName: "bc"}
	if attributesHash(a) == attributesHash(b) {
		t.Error("ambiguous encoding")
	}
}

func TestPrepareSnapshotNormalizesAndRejects(t *testing.T) {
	users, reason := prepareSnapshot(DirectorySnapshot{Users: []DirectoryUser{
		{ExternalID: "a", DisplayName: "  A  ", Email: strp("   "), GivenName: strp(" G "), Enabled: true},
	}})
	if reason != "" || users[0].DisplayName != "A" || users[0].Email != nil || *users[0].GivenName != "G" || users[0].AttributesHash == "" {
		t.Fatalf("users = %+v reason = %q", users, reason)
	}
	bad := map[string]DirectorySnapshot{
		"no users":   {Groups: []DirectoryGroup{{ExternalID: "g", DisplayName: "G"}}},
		"dup user":   {Users: []DirectoryUser{{ExternalID: "a", DisplayName: "A"}, {ExternalID: "a", DisplayName: "B"}}},
		"dup across": {Users: []DirectoryUser{{ExternalID: "a", DisplayName: "A"}}, Groups: []DirectoryGroup{{ExternalID: "a", DisplayName: "G"}}},
		"no name":    {Users: []DirectoryUser{{ExternalID: "a", DisplayName: " "}}},
		"group name": {Users: []DirectoryUser{{ExternalID: "a", DisplayName: "A"}}, Groups: []DirectoryGroup{{ExternalID: "g"}}},
	}
	for name, snap := range bad {
		if _, reason := prepareSnapshot(snap); reason == "" {
			t.Errorf("%s accepted", name)
		}
	}
	// Reasons never contain attribute values.
	_, reason = prepareSnapshot(DirectorySnapshot{Users: []DirectoryUser{{ExternalID: "secret-id", DisplayName: "A"}, {ExternalID: "secret-id", DisplayName: "A"}}})
	if strings.Contains(reason, "secret-id") {
		t.Errorf("reason leaks value: %q", reason)
	}
}

type fakeStore struct {
	startErr   error
	applyErr   error
	started    int
	applied    int
	finished   []SyncRunFinish
	applyInput SyncApplyInput
	startedAt  time.Time
	abandoned  time.Time
}

func (s *fakeStore) StartRun(_ context.Context, _ string, _ SyncTrigger, startedAt, abandonedBefore time.Time) (string, error) {
	s.started++
	s.startedAt, s.abandoned = startedAt, abandonedBefore
	return "run-1", s.startErr
}
func (s *fakeStore) ApplySnapshot(_ context.Context, in SyncApplyInput) (SyncApplyOutput, error) {
	s.applied++
	s.applyInput = in
	return SyncApplyOutput{Counts: map[string]int{"usersCreated": 1}}, s.applyErr
}
func (s *fakeStore) FinishRun(_ context.Context, f SyncRunFinish) error {
	s.finished = append(s.finished, f)
	return nil
}

type fakeSource struct {
	snap DirectorySnapshot
	err  error
}

func (s fakeSource) ProviderKey() string                              { return "ad" }
func (s fakeSource) Fetch(context.Context) (DirectorySnapshot, error) { return s.snap, s.err }

func newTestSync(store DirectorySyncStore, cfg DirectorySyncConfig) *DirectorySync {
	t := time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC)
	return NewDirectorySync(store, cfg, func() time.Time { return t }, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestRunErrorClassification(t *testing.T) {
	snap := DirectorySnapshot{Users: []DirectoryUser{{ExternalID: "a", DisplayName: "A"}}}
	ctx := context.Background()

	t.Run("already running", func(t *testing.T) {
		st := &fakeStore{startErr: ErrSyncAlreadyRunning}
		res, err := newTestSync(st, DirectorySyncConfig{}).Run(ctx, fakeSource{snap: snap}, SyncTriggerScheduled)
		if !errors.Is(err, ErrSyncAlreadyRunning) || jobs.IsPermanent(err) || st.applied != 0 || len(st.finished) != 0 || res.RunID != "" {
			t.Fatalf("res=%+v err=%v", res, err)
		}
	})
	t.Run("fetch error is retryable and fails the run", func(t *testing.T) {
		st := &fakeStore{}
		_, err := newTestSync(st, DirectorySyncConfig{}).Run(ctx, fakeSource{err: errors.New("boom")}, SyncTriggerScheduled)
		if err == nil || jobs.IsPermanent(err) || len(st.finished) != 1 || st.finished[0].Outcome != SyncOutcomeFailed || !strings.Contains(st.finished[0].Error, "boom") {
			t.Fatalf("err=%v finished=%+v", err, st.finished)
		}
	})
	t.Run("invalid snapshot is permanent", func(t *testing.T) {
		st := &fakeStore{}
		_, err := newTestSync(st, DirectorySyncConfig{}).Run(ctx, fakeSource{}, SyncTriggerScheduled)
		if !errors.Is(err, ErrInvalidSnapshot) || !jobs.IsPermanent(err) || st.applied != 0 || len(st.finished) != 1 {
			t.Fatalf("err=%v applied=%d finished=%+v", err, st.applied, st.finished)
		}
	})
	t.Run("safeguard is permanent and recorded as aborted", func(t *testing.T) {
		st := &fakeStore{applyErr: &SafeguardError{ActiveBefore: 10, Deactivations: 8, MaxPercent: 10}}
		res, err := newTestSync(st, DirectorySyncConfig{}).Run(ctx, fakeSource{snap: snap}, SyncTriggerManual)
		if !errors.Is(err, ErrSyncSafeguard) || !jobs.IsPermanent(err) || res.Outcome != SyncOutcomeAbortedSafeguard {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		if len(st.finished) != 1 || st.finished[0].Outcome != SyncOutcomeAbortedSafeguard || st.finished[0].Safeguard == nil {
			t.Fatalf("finished = %+v", st.finished)
		}
	})
	t.Run("apply error is retryable", func(t *testing.T) {
		st := &fakeStore{applyErr: errors.New("db down")}
		res, err := newTestSync(st, DirectorySyncConfig{}).Run(ctx, fakeSource{snap: snap}, SyncTriggerScheduled)
		if err == nil || jobs.IsPermanent(err) || res.Outcome != SyncOutcomeFailed || len(st.finished) != 1 || st.finished[0].Outcome != SyncOutcomeFailed {
			t.Fatalf("res=%+v err=%v finished=%+v", res, err, st.finished)
		}
	})
	t.Run("success passes normalized input and config", func(t *testing.T) {
		st := &fakeStore{}
		res, err := newTestSync(st, DirectorySyncConfig{MaxDeactivationPercent: 250, RunTimeout: time.Minute}).Run(ctx, fakeSource{snap: snap}, SyncTriggerScheduled)
		if err != nil || res.Outcome != SyncOutcomeSucceeded || res.RunID != "run-1" || res.Counts["usersCreated"] != 1 {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		if st.applyInput.MaxDeactivationPercent != 100 || st.applyInput.ProviderKey != "ad" || len(st.applyInput.Users) != 1 || st.applyInput.Users[0].AttributesHash == "" {
			t.Errorf("input = %+v", st.applyInput)
		}
		// Timestamps are truncated to what PostgreSQL stores; abandonment uses the run timeout.
		if st.startedAt.Nanosecond()%1000 != 0 || st.startedAt.Sub(st.abandoned) != time.Minute {
			t.Errorf("startedAt=%v abandoned=%v", st.startedAt, st.abandoned)
		}
	})
	t.Run("unknown trigger", func(t *testing.T) {
		st := &fakeStore{}
		_, err := newTestSync(st, DirectorySyncConfig{}).Run(ctx, fakeSource{snap: snap}, "bogus")
		if err == nil || !jobs.IsPermanent(err) || st.started != 0 {
			t.Fatalf("err=%v started=%d", err, st.started)
		}
	})
}

func TestSanitizeErrorTruncatesAndStripsControlCharacters(t *testing.T) {
	got := sanitizeError(errors.New("a\nb\t" + strings.Repeat("é", 400)))
	if len(got) > maxStoredErrorLen || strings.ContainsAny(got, "\n\t") || !strings.HasPrefix(got, "a b ") {
		t.Errorf("sanitized = %q (%d)", got, len(got))
	}
	if !strings.HasSuffix(got, "é") {
		t.Error("UTF-8 sequence split")
	}
}
