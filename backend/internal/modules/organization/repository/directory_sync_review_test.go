package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
)

func conflictKinds(run application.DirectorySyncRun) map[string]int {
	out := map[string]int{}
	for _, c := range run.Conflicts {
		out[c.Kind]++
	}
	return out
}

func TestSyncInvalidAttributesKeepStoredValuesAndStillObserve(t *testing.T) {
	f := newSyncFixture(t, 100)
	alice, bob, carol := f.user("alice"), f.user("bob"), f.user("carol")
	f.setUsers(alice, bob, carol)
	f.setGroups(f.group("g", []string{"alice", "bob", "carol"}, nil))
	f.mustRun()
	aliceBefore := f.userRow("alice")
	session := f.insertSession(aliceBefore.id)

	// alice: invalid email and disabled; bob: marked invalid by the source;
	// carol: unchanged and valid. dave is new and invalid.
	alice.Email = ptr("a\x00@example.test")
	alice.DisplayName = "Alice Changed"
	alice.Enabled = false
	bob.Invalid = true
	bob.DisplayName = "Bob Changed"
	dave := f.user("dave")
	dave.Username = "dave\x01"
	f.setUsers(alice, bob, carol, dave)
	f.setGroups(f.group("g", []string{"alice", "bob", "carol", "dave"}, nil))
	res := f.mustRun()

	// Never swept: still observed. Enabled and the status rule still apply.
	f.expectCounts(res, map[string]int{"usersNotObserved": 0, "usersObserved": 4, "usersCreated": 0, "usersUpdated": 1, "usersUnchanged": 2, "usersDeactivated": 1, "unresolvedMembers": 1})
	aliceNow := f.userRow("alice")
	if aliceNow.display != aliceBefore.display || aliceNow.email == nil || *aliceNow.email != f.email("alice") {
		t.Errorf("alice attributes changed: %+v", aliceNow)
	}
	id := f.identity("alice")
	if id.enabled || id.deleted != nil || aliceNow.status != "inactive" || !f.sessionRevoked(session) {
		t.Errorf("alice identity = %+v status = %s", id, aliceNow.status)
	}
	eq(t, "bob display", f.userRow("bob").display, "User bob")
	run := f.runRow(res.RunID)
	eq(t, "invalid conflicts", conflictKinds(run)[application.ConflictInvalidAttributes], 3)
	if f.hasUser("dave") {
		t.Error("new invalid account must be skipped")
	}
	eq(t, "open members", len(f.openMembers("g")), 3)
	seen := f.identity("bob").lastSeen
	if seen == nil || !seen.Equal(*run.ObservedAt) {
		t.Errorf("bob last_seen_at = %v, want observation time", seen)
	}
	raw := string(mustJSON(t, run.Conflicts))
	if strings.Contains(raw, "example.test") {
		t.Errorf("conflicts carry values: %s", raw)
	}

	// Once the values are valid again they are applied.
	f.setUsers(f.user("alice"), f.user("bob"), f.user("carol"))
	f.setGroups(f.group("g", []string{"alice", "bob", "carol"}, nil))
	res2 := f.mustRun()
	f.expectCounts(res2, map[string]int{"usersUpdated": 1, "usersUnchanged": 2, "usersActivated": 1})
	eq(t, "alice enabled", f.identity("alice").enabled, true)
}

func TestSyncSanitizesDisplayTextAndFallsBackForGroups(t *testing.T) {
	f := newSyncFixture(t, 100)
	u := f.user("alice")
	u.DisplayName = "Al\x00ice\xff\tLee"
	u.GivenName = ptr(strings.Repeat("é", 3000))
	f.setUsers(u)
	g := f.group("g", []string{"alice"}, nil)
	g.DisplayName = "\x00"
	g.Description = ptr("d\x00e")
	f.setGroups(g)
	res := f.mustRun()
	f.expectCounts(res, map[string]int{"usersCreated": 1, "groupsCreated": 1})
	eq(t, "display", f.userRow("alice").display, "Al ice� Lee")
	var given string
	if err := f.pool.QueryRow(context.Background(), `SELECT u.given_name FROM organization.users u JOIN organization.external_identities e ON e.user_id = u.id
		WHERE e.provider_key = $1 AND e.external_subject = $2`, f.provider, f.ext("alice")).Scan(&given); err != nil || len(given) > 4096 || !strings.HasSuffix(given, "é") {
		t.Errorf("given name len = %d (%v)", len(given), err)
	}
	_, display, _, _, _ := f.groupState("g")
	eq(t, "group display falls back to the external id", display, g.ExternalID)
	var desc string
	if err := f.pool.QueryRow(context.Background(), `SELECT description FROM organization.directory_groups WHERE provider_key = $1`, f.provider).Scan(&desc); err != nil || desc != "d e" {
		t.Errorf("description = %q (%v)", desc, err)
	}
}

func TestSyncEmailKeysUsePostgreSQLLower(t *testing.T) {
	f := newSyncFixture(t, 100)
	const circledA = "Ⓐ" // Latin capital letter A in a circle
	var lowered string
	if err := f.pool.QueryRow(context.Background(), `SELECT lower($1::text)`, circledA).Scan(&lowered); err != nil {
		t.Fatal(err)
	}
	postgresFolds := lowered != circledA

	// The very same address owned by a platform user always conflicts, whatever
	// the database locale does with the character.
	f.platformUser("Platform Circle", f.pfx+"-"+circledA+"@example.test")
	same := f.user("same")
	same.Email = ptr(f.pfx + "-" + circledA + "@example.test")
	// The small circled letter is the lower-case form only where PostgreSQL folds.
	folded := f.user("folded")
	folded.Email = ptr(f.pfx + "-ⓐ@example.test")
	f.setUsers(f.user("alice"), same, folded)
	res := f.mustRun()
	if f.hasUser("same") {
		t.Error("identical address must conflict")
	}
	wantConflicts, wantCreated := 1, 2
	if postgresFolds {
		wantConflicts, wantCreated = 2, 1
	}
	eq(t, "conflicts", res.ConflictCount, wantConflicts)
	f.expectCounts(res, map[string]int{"usersCreated": wantCreated})
	eq(t, "folded created", f.hasUser("folded"), !postgresFolds)
}

// holdEmail inserts a platform user with email in a transaction that stays
// open, so the sync's own insert blocks on the unique index until commit.
func (f *syncFixture) holdEmail(email string) (commit func()) {
	f.t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO organization.users(display_name, primary_email) VALUES ($1, $2) RETURNING id::text`, "Racer "+f.pfx, email).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	f.extra = append(f.extra, id)
	// If the test fails before commit, roll back so the held connection is
	// released and the pool can close instead of hanging.
	committed := false
	f.t.Cleanup(func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	})
	return func() {
		committed = true
		if err := tx.Commit(ctx); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *syncFixture) runAsync() chan error {
	done := make(chan error, 1)
	go func() {
		res, err := f.run()
		if err == nil && res.Outcome != "succeeded" {
			err = errors.New("outcome " + res.Outcome)
		}
		done <- err
	}()
	return done
}

func waitBlocked(t *testing.T, done chan error) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("run finished while the address was held: %v", err)
	case <-time.After(400 * time.Millisecond):
	}
}

func TestSyncInsertRaceOnEmailBecomesConflict(t *testing.T) {
	f := newSyncFixture(t, 100)
	racer := f.user("racer")
	racer.Email = ptr(f.email("taken"))
	f.setUsers(f.user("alice"), racer, f.user("bob"))
	commit := f.holdEmail(f.email("taken"))
	done := f.runAsync()
	waitBlocked(t, done)
	commit()
	if err := <-done; err != nil {
		t.Fatalf("run must not fail on an email race: %v", err)
	}
	if f.hasUser("racer") || !f.hasUser("alice") || !f.hasUser("bob") {
		t.Error("only the account that lost the race is skipped")
	}
	var runID string
	if err := f.pool.QueryRow(context.Background(), `SELECT id::text FROM organization.directory_sync_runs WHERE provider_key = $1`, f.provider).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	run := f.runRow(runID)
	if len(run.Conflicts) != 1 || run.Conflicts[0].Kind != "email_in_use" || run.Conflicts[0].Username != "racer" {
		t.Errorf("conflicts = %+v", run.Conflicts)
	}
	f.expectCounts(application.SyncRunResult{Counts: run.Counts}, map[string]int{"usersCreated": 2, "usersObserved": 3, "usersUnchanged": 0})
	eq(t, "created audits", f.syncAudit(runID, "organization.user.created_from_directory"), 2)
}

func TestSyncUpdateRaceOnEmailKeepsOldValueAndForcesReevaluation(t *testing.T) {
	f := newSyncFixture(t, 100)
	f.setUsers(f.user("alice"), f.user("bob"))
	f.mustRun()
	alice := f.user("alice")
	alice.Email = ptr(f.email("taken"))
	alice.DisplayName = "Alice Renamed"
	f.setUsers(alice, f.user("bob"))
	commit := f.holdEmail(f.email("taken"))
	done := f.runAsync()
	waitBlocked(t, done)
	commit()
	if err := <-done; err != nil {
		t.Fatalf("run must not fail on an email race: %v", err)
	}
	a := f.userRow("alice")
	if a.email == nil || *a.email != f.email("alice") || a.display != "Alice Renamed" {
		t.Errorf("alice = %+v: want old email kept and other fields applied", a)
	}
	if f.identity("alice").hash != nil {
		t.Error("attributes_hash must be NULL after a withheld email")
	}
	if got := f.count(`SELECT count(*) FROM platform.audit_events WHERE action = 'organization.user.primary_email_changed' AND target_id = $1`, a.id); got != 0 {
		t.Errorf("email audit for an unapplied change: %d", got)
	}
}

func TestSyncWithheldEmailStoresNullHashAndDoesNotChurn(t *testing.T) {
	f := newSyncFixture(t, 100)
	f.setUsers(f.user("alice"), f.user("bob"))
	f.mustRun()
	alice := f.user("alice")
	alice.Email = ptr(f.email("bob"))
	f.setUsers(alice, f.user("bob"))
	f.mustRun()
	if f.identity("alice").hash != nil {
		t.Error("attributes_hash must be NULL while the email change is withheld")
	}
	res := f.mustRun()
	f.expectCounts(res, map[string]int{"usersUpdated": 0, "usersUnchanged": 2})
	eq(t, "audit rows", f.auditRows(res.RunID), 0)
	// Released: applied by the re-evaluation, hash restored.
	bob := f.user("bob")
	bob.Email = ptr(f.email("bob2"))
	f.setUsers(alice, bob)
	f.mustRun()
	res = f.mustRun()
	if h := f.identity("alice").hash; h == nil {
		t.Error("hash must be stored once the email is applied")
	}
	eq(t, "alice email audit", f.syncAudit(res.RunID, "organization.user.primary_email_changed"), 1)
}

func TestSyncIdentityOnlyChangeSkipsUserUpdate(t *testing.T) {
	f := newSyncFixture(t, 100)
	f.setUsers(f.user("alice"))
	f.mustRun()
	before := f.userRow("alice")
	alice := f.user("alice")
	alice.Username = "alice.new"
	f.setUsers(alice)
	res := f.mustRun()
	f.expectCounts(res, map[string]int{"usersUpdated": 1})
	if after := f.userRow("alice"); !after.updatedAt.Equal(before.updatedAt) {
		t.Errorf("users row touched by an identity-only change: %v -> %v", before.updatedAt, after.updatedAt)
	}
	if ev := f.events(res.RunID)[before.id]; strings.Join(ev.ChangedFields, ",") != "username" {
		t.Errorf("event = %+v", ev)
	}
}

func TestSyncObservedAtNeverMovesBackwards(t *testing.T) {
	f := newSyncFixture(t, 100)
	f.setUsers(f.user("alice"))
	r1 := f.mustRun()
	prev := f.runRow(r1.RunID)

	behind := &tickClock{t: clockBase.Add(-time.Hour)}
	slow := public.NewDirectorySync(f.repo, public.DirectorySyncConfig{MaxMissingPercent: 100}, behind.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	res, err := slow.Run(context.Background(), f.src, public.SyncTriggerScheduled, "")
	if err != nil {
		t.Fatal(err)
	}
	run := f.runRow(res.RunID)
	if run.ObservedAt == nil || run.ObservedAt.Before(*prev.ObservedAt) {
		t.Errorf("observed_at %v moved behind %v", run.ObservedAt, prev.ObservedAt)
	}
	if seen := f.identity("alice").lastSeen; seen == nil || seen.Before(*prev.ObservedAt) {
		t.Errorf("last_seen_at %v moved backwards", seen)
	}
	if run.FinishedAt == nil || run.FinishedAt.Before(run.StartedAt) {
		t.Errorf("finished_at %v before started_at %v", run.FinishedAt, run.StartedAt)
	}
}

func TestSyncFinishedAtIsReadAfterApply(t *testing.T) {
	f := newSyncFixture(t, 100)
	f.setUsers(f.user("alice"))
	res := f.mustRun()
	run := f.runRow(res.RunID)
	if !run.FinishedAt.After(*run.ObservedAt) {
		t.Errorf("finished_at %v must be later than observed_at %v", run.FinishedAt, run.ObservedAt)
	}
}

func TestSyncProviderKeyChangeFailsPermanently(t *testing.T) {
	f1 := newSyncFixture(t, 100)
	f1.setUsers(f1.user("alice"))
	f1.mustRun()

	f2 := newSyncFixture(t, 100)
	f2.setUsers(f2.user("alice"))
	res, err := f2.run()
	if !errors.Is(err, public.ErrProviderKeyChanged) {
		t.Fatalf("err = %v, want ErrProviderKeyChanged", err)
	}
	run := f2.runRow(res.RunID)
	if run.Outcome != "failed" || run.Error == nil || !strings.Contains(*run.Error, "LDAP_PROVIDER_KEY") {
		t.Errorf("run = %+v", run)
	}
	eq(t, "identities of the new key", f2.count(`SELECT count(*) FROM organization.external_identities WHERE provider_key = $1`, f2.provider), 0)

	// An existing key keeps syncing, and a directory whose identities are all
	// deleted can be re-keyed.
	f1.mustRun()
	if _, err := f1.pool.Exec(context.Background(), `UPDATE organization.external_identities SET enabled = false, deleted_observed_at = last_seen_at WHERE provider_key = $1`, f1.provider); err != nil {
		t.Fatal(err)
	}
	f2.mustRun()
}

func TestSyncRunIsLinkedToJob(t *testing.T) {
	f := newSyncFixture(t, 100)
	f.setUsers(f.user("alice"))
	var jobID string
	if err := f.pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	res, err := f.sync.Run(context.Background(), f.src, public.SyncTriggerManual, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if run := f.runRow(res.RunID); run.JobID == nil || *run.JobID != jobID {
		t.Errorf("job id = %v, want %s", run.JobID, jobID)
	}
	if run := f.runRow(f.mustRun().RunID); run.JobID != nil {
		t.Errorf("job id = %v, want nil", run.JobID)
	}
}

func TestSyncStatusAuditCarriesBeforeAndAfter(t *testing.T) {
	f := newSyncFixture(t, 100)
	f.setUsers(f.user("alice"), f.user("bob"))
	f.mustRun()
	bob := f.user("bob")
	bob.Enabled = false
	f.setUsers(f.user("alice"), bob)
	res := f.mustRun()
	var before, after, source string
	if err := f.pool.QueryRow(context.Background(), `
		SELECT before_data::text, after_data::text, metadata->>'actor' FROM platform.audit_events
		WHERE correlation_id = $1 AND action = 'organization.user.status_changed'`, res.RunID).Scan(&before, &after, &source); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(before, `"status": "active"`) || !strings.Contains(before, `"statusSource": "directory"`) ||
		!strings.Contains(after, `"status": "inactive"`) || source != "directory-sync" {
		t.Errorf("before=%s after=%s actor=%s", before, after, source)
	}
	if f.userRow("bob").source != "directory" {
		t.Error("status_source must be directory")
	}
}

func TestChangeUserStatusRejectsUnknownSource(t *testing.T) {
	_, err := changeUserStatus(context.Background(), nil, []string{"x"}, statusTransition{From: "active", To: "inactive", ToSource: "other"}, statusChangeContext{})
	if err == nil {
		t.Fatal("unknown status source accepted")
	}
}

func (f *syncFixture) startedRun() string {
	f.t.Helper()
	id, err := f.repo.StartRun(context.Background(), f.provider, public.SyncTriggerScheduled, "", clockBase, clockBase.Add(-time.Hour))
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func TestApplySnapshotRefusesAbandonedRun(t *testing.T) {
	f := newSyncFixture(t, 100)
	ctx := context.Background()
	runID := f.startedRun()
	// A second connection (the next run's StartRun) declares the run abandoned.
	if _, err := f.pool.Exec(ctx, `UPDATE organization.directory_sync_runs SET outcome = 'failed', finished_at = $2, error = 'abandoned' WHERE id = $1`, runID, clockBase.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	u := f.user("alice")
	_, err := f.repo.ApplySnapshot(ctx, application.SyncApplyInput{
		RunID: runID, ProviderKey: f.provider, FetchedAt: clockBase.Add(time.Hour), MaxMissingPercent: 100,
		Users: []application.SyncUser{{SnapshotUser: u, AttributesHash: "h"}}, Now: f.clock.Now,
	})
	if err == nil || !strings.Contains(err.Error(), "no longer running") {
		t.Fatalf("err = %v, want refusal", err)
	}
	eq(t, "identities", f.count(`SELECT count(*) FROM organization.external_identities WHERE provider_key = $1`, f.provider), 0)
	eq(t, "run outcome", f.runRow(runID).Outcome, "failed")
}

func TestStartRunWaitsForApplyingRun(t *testing.T) {
	f := newSyncFixture(t, 100)
	ctx := context.Background()
	runID := f.startedRun()

	// An applying run holds the run row lock for its whole transaction.
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT 1 FROM organization.directory_sync_runs WHERE id = $1 FOR UPDATE`, runID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		id  string
		err error
	}
	done := make(chan result, 1)
	go func() {
		id, err := f.repo.StartRun(ctx, f.provider, public.SyncTriggerScheduled, "", clockBase.Add(2*time.Hour), clockBase.Add(time.Hour))
		done <- result{id, err}
	}()
	select {
	case r := <-done:
		t.Fatalf("StartRun returned while the run was applying: %+v", r)
	case <-time.After(400 * time.Millisecond):
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		// The lock holder went away without finishing: its run is abandoned and replaced.
		if r.err != nil || r.id == "" {
			t.Fatalf("StartRun = %+v", r)
		}
		old := f.runRow(runID)
		if old.Outcome != "failed" || old.Error == nil || *old.Error != "abandoned" {
			t.Errorf("old run = %+v", old)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StartRun did not proceed after the lock was released")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
