package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

// tickClock returns strictly increasing times so every run has distinct,
// deterministic start, observation and finish instants.
type tickClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *tickClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Second)
	return c.t
}

type fakeSource struct {
	key  string
	snap public.DirectorySnapshot
	err  error
}

func (s *fakeSource) ProviderKey() string { return s.key }
func (s *fakeSource) Fetch(context.Context) (public.DirectorySnapshot, error) {
	return s.snap, s.err
}

type syncFixture struct {
	t        *testing.T
	pool     *pgxpool.Pool
	repo     *Repository
	provider string
	pfx      string
	clock    *tickClock
	src      *fakeSource
	sync     *public.DirectorySync
	extra    []string // users created outside sync, removed in cleanup
}

var clockBase = time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)

func newSyncFixture(t *testing.T, maxPercent int) *syncFixture {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	pfx := "zt" + hex.EncodeToString(b)
	f := &syncFixture{t: t, pool: pool, repo: New(pool), provider: pfx + "-ad", pfx: pfx, clock: &tickClock{t: clockBase}}
	f.src = &fakeSource{key: f.provider}
	f.sync = public.NewDirectorySync(f.repo, public.DirectorySyncConfig{MaxDeactivationPercent: maxPercent, RunTimeout: 15 * time.Minute},
		f.clock.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(f.cleanup)
	return f
}

func (f *syncFixture) cleanup() {
	ctx := context.Background()
	stmts := []string{
		`DELETE FROM platform.audit_events WHERE correlation_id IN (SELECT id::text FROM organization.directory_sync_runs WHERE provider_key = $1)`,
		`DELETE FROM platform.outbox_events WHERE correlation_id IN (SELECT id::text FROM organization.directory_sync_runs WHERE provider_key = $1)`,
		`DELETE FROM platform.sessions WHERE user_id IN (SELECT user_id FROM organization.external_identities WHERE provider_key = $1)`,
		`DELETE FROM organization.directory_group_memberships WHERE group_id IN (SELECT id FROM organization.directory_groups WHERE provider_key = $1)`,
		`DELETE FROM organization.directory_group_nesting WHERE parent_group_id IN (SELECT id FROM organization.directory_groups WHERE provider_key = $1)
			OR child_group_id IN (SELECT id FROM organization.directory_groups WHERE provider_key = $1)`,
		`DELETE FROM organization.directory_groups WHERE provider_key = $1`,
	}
	for _, s := range stmts {
		if _, err := f.pool.Exec(ctx, s, f.provider); err != nil {
			f.t.Errorf("cleanup: %v", err)
		}
	}
	rows, err := f.pool.Query(ctx, `SELECT user_id::text FROM organization.external_identities WHERE provider_key = $1`, f.provider)
	if err != nil {
		f.t.Errorf("cleanup: %v", err)
		return
	}
	ids := append([]string{}, f.extra...)
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, s := range []string{
		`DELETE FROM organization.external_identities WHERE provider_key = $1`,
	} {
		if _, err := f.pool.Exec(ctx, s, f.provider); err != nil {
			f.t.Errorf("cleanup: %v", err)
		}
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM organization.users WHERE id = ANY($1::text[]::uuid[])`, ids); err != nil {
		f.t.Errorf("cleanup users: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM organization.directory_sync_runs WHERE provider_key = $1`, f.provider); err != nil {
		f.t.Errorf("cleanup runs: %v", err)
	}
}

func ptr(s string) *string { return &s }

func (f *syncFixture) email(name string) string { return f.pfx + "-" + name + "@example.test" }

func (f *syncFixture) user(name string) public.DirectoryUser {
	return public.DirectoryUser{
		ExternalID: f.pfx + "-" + name, Username: name, DistinguishedName: "cn=" + name + ",dc=test",
		DisplayName: "User " + name, Email: ptr(f.email(name)), Enabled: true,
	}
}

func (f *syncFixture) ext(name string) string { return f.pfx + "-" + name }

func (f *syncFixture) group(name string, users []string, groups []string) public.DirectoryGroup {
	g := public.DirectoryGroup{ExternalID: f.pfx + "-g-" + name, DisplayName: "Group " + name}
	for _, u := range users {
		g.MemberUserIDs = append(g.MemberUserIDs, f.ext(u))
	}
	for _, c := range groups {
		g.MemberGroupIDs = append(g.MemberGroupIDs, f.pfx+"-g-"+c)
	}
	return g
}

func (f *syncFixture) setUsers(users ...public.DirectoryUser) { f.src.snap.Users = users }
func (f *syncFixture) setGroups(groups ...public.DirectoryGroup) {
	f.src.snap.Groups = groups
}

func (f *syncFixture) run() (public.SyncRunResult, error) {
	return f.sync.Run(context.Background(), f.src, public.SyncTriggerScheduled)
}

func (f *syncFixture) mustRun() public.SyncRunResult {
	f.t.Helper()
	res, err := f.run()
	if err != nil {
		f.t.Fatalf("run: %v", err)
	}
	if res.Outcome != "succeeded" {
		f.t.Fatalf("outcome = %q", res.Outcome)
	}
	return res
}

func (f *syncFixture) expectCounts(res public.SyncRunResult, want map[string]int) {
	f.t.Helper()
	for k, v := range want {
		if res.Counts[k] != v {
			f.t.Errorf("counts[%s] = %d, want %d (all: %v)", k, res.Counts[k], v, res.Counts)
		}
	}
}

type userState struct {
	id, display, status, source string
	email, manager              *string
	updatedAt                   time.Time
}

func (f *syncFixture) userRow(name string) userState {
	f.t.Helper()
	var u userState
	err := f.pool.QueryRow(context.Background(), `
		SELECT u.id::text, u.display_name, u.status, u.status_source, u.primary_email, u.manager_user_id::text, u.updated_at
		FROM organization.users u JOIN organization.external_identities e ON e.user_id = u.id
		WHERE e.provider_key = $1 AND e.external_subject = $2`, f.provider, f.ext(name)).
		Scan(&u.id, &u.display, &u.status, &u.source, &u.email, &u.manager, &u.updatedAt)
	if err != nil {
		f.t.Fatalf("user %s: %v", name, err)
	}
	return u
}

func (f *syncFixture) hasUser(name string) bool {
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM organization.external_identities WHERE provider_key = $1 AND external_subject = $2`,
		f.provider, f.ext(name)).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n == 1
}

type identityState struct {
	userID   string
	enabled  bool
	deleted  *time.Time
	lastSeen *time.Time
	hash     *string
}

func (f *syncFixture) identity(name string) identityState {
	f.t.Helper()
	var i identityState
	err := f.pool.QueryRow(context.Background(), `
		SELECT user_id::text, enabled, deleted_observed_at, last_seen_at, attributes_hash
		FROM organization.external_identities WHERE provider_key = $1 AND external_subject = $2`, f.provider, f.ext(name)).
		Scan(&i.userID, &i.enabled, &i.deleted, &i.lastSeen, &i.hash)
	if err != nil {
		f.t.Fatalf("identity %s: %v", name, err)
	}
	return i
}

func (f *syncFixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		f.t.Fatalf("count: %v", err)
	}
	return n
}

func (f *syncFixture) auditRows(runID string) int {
	return f.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1`, runID)
}

func (f *syncFixture) outboxRows(runID string) int {
	return f.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1`, runID)
}

// syncAudit counts sync-originated audit events of an action in a run.
func (f *syncFixture) syncAudit(runID, action string) int {
	return f.count(`SELECT count(*) FROM platform.audit_events
		WHERE correlation_id = $1 AND action = $2 AND actor_id IS NULL
		  AND metadata->>'actor' = 'directory-sync' AND metadata->>'providerKey' = $3 AND metadata->>'runId' = $1`,
		runID, action, f.provider)
}

type syncEvent struct {
	UserID        string   `json:"userId"`
	ProviderKey   string   `json:"providerKey"`
	Created       bool     `json:"created"`
	ChangedFields []string `json:"changedFields"`
	StatusChanged bool     `json:"statusChanged"`
}

func (f *syncFixture) events(runID string) map[string]syncEvent {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `
		SELECT event_type, event_version, actor_id IS NULL, payload FROM platform.outbox_events WHERE correlation_id = $1`, runID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]syncEvent{}
	for rows.Next() {
		var typ string
		var ver int
		var noActor bool
		var raw []byte
		if err := rows.Scan(&typ, &ver, &noActor, &raw); err != nil {
			f.t.Fatal(err)
		}
		if typ != "UserSynchronized" || ver != 1 || !noActor {
			f.t.Errorf("unexpected event %s v%d noActor=%v", typ, ver, noActor)
		}
		var ev syncEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			f.t.Fatal(err)
		}
		if ev.ProviderKey != f.provider {
			f.t.Errorf("event provider = %q", ev.ProviderKey)
		}
		out[ev.UserID] = ev
	}
	return out
}

func (f *syncFixture) runRow(runID string) application.DirectorySyncRun {
	f.t.Helper()
	run, err := f.repo.GetDirectorySyncRun(context.Background(), runID)
	if err != nil {
		f.t.Fatal(err)
	}
	return run
}

func (f *syncFixture) insertSession(userID string) string {
	f.t.Helper()
	var id string
	err := f.pool.QueryRow(context.Background(), `
		INSERT INTO platform.sessions (token_hash, user_id, auth_method, idle_expires_at, absolute_expires_at)
		VALUES (decode(md5(random()::text) || md5(random()::text), 'hex'), $1, 'test', now() + interval '1 hour', now() + interval '2 hours')
		RETURNING id::text`, userID).Scan(&id)
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *syncFixture) sessionRevoked(id string) bool {
	return f.count(`SELECT count(*) FROM platform.sessions WHERE id = $1 AND revoked_at IS NOT NULL`, id) == 1
}

func (f *syncFixture) platformUser(display, email string) string {
	f.t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `INSERT INTO organization.users(display_name, primary_email) VALUES ($1, $2) RETURNING id::text`, display, email).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	f.extra = append(f.extra, id)
	return id
}

func (f *syncFixture) openMembers(group string) []string {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `
		SELECT e.external_subject FROM organization.directory_group_memberships m
		JOIN organization.directory_groups g ON g.id = m.group_id
		JOIN organization.external_identities e ON e.user_id = m.user_id AND e.provider_key = g.provider_key
		WHERE g.provider_key = $1 AND g.external_id = $2 AND m.observed_until IS NULL`, f.provider, f.pfx+"-g-"+group)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, strings.TrimPrefix(s, f.pfx+"-"))
	}
	sort.Strings(out)
	return out
}

func (f *syncFixture) groupState(name string) (id string, display string, deleted *time.Time, first, last time.Time) {
	f.t.Helper()
	err := f.pool.QueryRow(context.Background(), `
		SELECT id::text, display_name, deleted_observed_at, first_observed_at, last_observed_at
		FROM organization.directory_groups WHERE provider_key = $1 AND external_id = $2`, f.provider, f.pfx+"-g-"+name).
		Scan(&id, &display, &deleted, &first, &last)
	if err != nil {
		f.t.Fatalf("group %s: %v", name, err)
	}
	return
}

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func TestSyncCreatesUsersGroupsAndRun(t *testing.T) {
	f := newSyncFixture(t, 10)
	alice, bob := f.user("alice"), f.user("bob")
	bob.Enabled = false
	alice.GivenName, alice.FamilyName, alice.EmployeeNumber = ptr("Alice"), ptr("Liddell"), ptr("E-1")
	f.setUsers(alice, bob)
	f.setGroups(f.group("staff", []string{"alice", "bob"}, nil))

	res := f.mustRun()
	f.expectCounts(res, map[string]int{
		"usersObserved": 2, "usersCreated": 2, "usersUpdated": 0, "usersUnchanged": 0, "usersNotObserved": 0,
		"groupsObserved": 1, "groupsCreated": 1, "membershipsOpened": 2, "unresolvedMembers": 0,
	})
	if res.ConflictCount != 0 {
		t.Errorf("conflicts = %d", res.ConflictCount)
	}

	a, b := f.userRow("alice"), f.userRow("bob")
	eq(t, "alice status", a.status, "active")
	eq(t, "alice source", a.source, "directory")
	eq(t, "bob status", b.status, "inactive")
	eq(t, "bob source", b.source, "directory")
	if a.email == nil || *a.email != f.email("alice") {
		t.Errorf("alice email = %v", a.email)
	}
	var given, family, empNo *string
	if err := f.pool.QueryRow(context.Background(), `SELECT given_name, family_name, employee_number FROM organization.users WHERE id = $1`, a.id).Scan(&given, &family, &empNo); err != nil {
		t.Fatal(err)
	}
	if given == nil || *given != "Alice" || family == nil || *family != "Liddell" || empNo == nil || *empNo != "E-1" {
		t.Errorf("attributes = %v %v %v", given, family, empNo)
	}
	run := f.runRow(res.RunID)
	eq(t, "outcome", run.Outcome, "succeeded")
	eq(t, "trigger", run.Trigger, "scheduled")
	if run.ObservedAt == nil || run.FinishedAt == nil || run.FinishedAt.Before(run.StartedAt) || run.ObservedAt.Before(run.StartedAt) {
		t.Errorf("run times: %+v", run)
	}
	ia := f.identity("alice")
	if ia.hash == nil || *ia.hash == "" || ia.lastSeen == nil || !ia.lastSeen.Equal(*run.ObservedAt) || ia.deleted != nil || !ia.enabled {
		t.Errorf("identity = %+v", ia)
	}
	if f.identity("bob").enabled {
		t.Error("bob identity must be disabled")
	}

	// Audit: one created_from_directory per new user, nothing else, actor NULL.
	eq(t, "created audit", f.syncAudit(res.RunID, "organization.user.created_from_directory"), 2)
	eq(t, "all audit rows", f.auditRows(res.RunID), 2)
	// Outbox: one UserSynchronized per created user.
	evs := f.events(res.RunID)
	if len(evs) != 2 || !evs[a.id].Created || !evs[b.id].Created || evs[a.id].ChangedFields == nil {
		t.Errorf("events = %+v", evs)
	}
	g, display, deleted, first, last := f.groupState("staff")
	_ = g
	eq(t, "group display", display, "Group staff")
	if deleted != nil || !first.Equal(*run.ObservedAt) || !last.Equal(*run.ObservedAt) {
		t.Errorf("group times: %v %v %v", deleted, first, last)
	}
}

func TestSyncSecondApplyOnlyBumpsFreshness(t *testing.T) {
	f := newSyncFixture(t, 10)
	f.setUsers(f.user("alice"), f.user("bob"))
	f.setGroups(f.group("staff", []string{"alice", "bob"}, nil), f.group("sub", []string{"bob"}, nil), f.group("outer", nil, []string{"sub"}))
	first := f.mustRun()
	a1, ia1 := f.userRow("alice"), f.identity("alice")
	_, _, _, _, gLast1 := f.groupState("staff")

	second := f.mustRun()
	f.expectCounts(second, map[string]int{
		"usersObserved": 2, "usersCreated": 0, "usersUpdated": 0, "usersUnchanged": 2,
		"groupsObserved": 3, "groupsCreated": 0, "groupsUpdated": 0,
		"membershipsOpened": 0, "membershipsClosed": 0, "nestingOpened": 0, "nestingClosed": 0,
	})
	eq(t, "audit rows of second run", f.auditRows(second.RunID), 0)
	eq(t, "outbox rows of second run", f.outboxRows(second.RunID), 0)
	eq(t, "audit rows of first run", f.auditRows(first.RunID), 2)

	a2, ia2 := f.userRow("alice"), f.identity("alice")
	if !a2.updatedAt.Equal(a1.updatedAt) {
		t.Error("user row must not be rewritten")
	}
	if !ia2.lastSeen.After(*ia1.lastSeen) {
		t.Errorf("last_seen_at not bumped: %v -> %v", ia1.lastSeen, ia2.lastSeen)
	}
	_, _, _, _, gLast2 := f.groupState("staff")
	if !gLast2.After(gLast1) {
		t.Error("group last_observed_at not bumped")
	}
	eq(t, "membership rows", f.count(`SELECT count(*) FROM organization.directory_group_memberships m JOIN organization.directory_groups g ON g.id = m.group_id WHERE g.provider_key = $1`, f.provider), 3)
	eq(t, "open membership last_observed_at bumped", f.count(`
		SELECT count(*) FROM organization.directory_group_memberships m JOIN organization.directory_groups g ON g.id = m.group_id
		WHERE g.provider_key = $1 AND m.last_observed_at > m.observed_from`, f.provider), 3)
	eq(t, "nesting rows", f.count(`SELECT count(*) FROM organization.directory_group_nesting n JOIN organization.directory_groups g ON g.id = n.parent_group_id WHERE g.provider_key = $1`, f.provider), 1)
}

func TestSyncUpdatesChangedAttributes(t *testing.T) {
	f := newSyncFixture(t, 10)
	f.setUsers(f.user("alice"), f.user("bob"))
	f.mustRun()
	alice := f.user("alice")
	alice.DisplayName = "Alice Renamed"
	alice.Email = ptr(f.email("alice2"))
	alice.Username = "alice.r"
	alice.GivenName = ptr("Alice")
	f.setUsers(alice, f.user("bob"))
	hashBefore := f.identity("alice").hash

	res := f.mustRun()
	f.expectCounts(res, map[string]int{"usersUpdated": 1, "usersUnchanged": 1, "usersCreated": 0})
	a := f.userRow("alice")
	eq(t, "display", a.display, "Alice Renamed")
	if a.email == nil || *a.email != f.email("alice2") {
		t.Errorf("email = %v", a.email)
	}
	if h := f.identity("alice").hash; h == nil || *h == *hashBefore {
		t.Error("attributes hash must change")
	}
	eq(t, "username", f.count(`SELECT count(*) FROM organization.external_identities WHERE provider_key = $1 AND username = 'alice.r'`, f.provider), 1)
	// Attribute refreshes are observations, not audit events.
	eq(t, "audit rows", f.auditRows(res.RunID), 0)
	evs := f.events(res.RunID)
	if len(evs) != 1 {
		t.Fatalf("events = %+v", evs)
	}
	ev := evs[a.id]
	if ev.Created || ev.StatusChanged || strings.Join(ev.ChangedFields, ",") != "displayName,givenName,primaryEmail,username" {
		t.Errorf("event = %+v", ev)
	}
	// Event payloads carry field names only.
	var raw string
	if err := f.pool.QueryRow(context.Background(), `SELECT payload::text FROM platform.outbox_events WHERE correlation_id = $1`, res.RunID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "Renamed") || strings.Contains(raw, "example.test") {
		t.Errorf("payload leaks values: %s", raw)
	}
}

func TestSyncStatusRuleAndSessionRevocation(t *testing.T) {
	f := newSyncFixture(t, 100)
	names := []string{"alice", "platform", "departed", "other"}
	var users []public.DirectoryUser
	for _, n := range names {
		users = append(users, f.user(n))
	}
	f.setUsers(users...)
	f.mustRun()

	// Platform-owned states the sync must respect.
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE organization.users SET status = 'inactive', status_source = 'platform' WHERE id = $1`, f.userRow("platform").id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE organization.users SET status = 'departed', status_source = 'platform' WHERE id = $1`, f.userRow("departed").id); err != nil {
		t.Fatal(err)
	}
	session := f.insertSession(f.userRow("alice").id)
	otherSession := f.insertSession(f.userRow("other").id)

	disable := func(enabled bool) {
		var next []public.DirectoryUser
		for _, n := range names {
			u := f.user(n)
			if n != "other" {
				u.Enabled = enabled
			}
			next = append(next, u)
		}
		f.setUsers(next...)
	}

	disable(false)
	res := f.mustRun()
	f.expectCounts(res, map[string]int{"usersDeactivated": 1, "usersActivated": 0, "sessionsRevoked": 1, "usersUpdated": 3})
	a := f.userRow("alice")
	eq(t, "alice status", a.status, "inactive")
	eq(t, "alice source", a.source, "directory")
	eq(t, "platform status", f.userRow("platform").status, "inactive")
	eq(t, "platform source", f.userRow("platform").source, "platform")
	eq(t, "departed status", f.userRow("departed").status, "departed")
	eq(t, "other status", f.userRow("other").status, "active")
	if !f.sessionRevoked(session) || f.sessionRevoked(otherSession) {
		t.Error("only the deactivated user's session must be revoked")
	}
	eq(t, "status audit", f.syncAudit(res.RunID, "organization.user.status_changed"), 1)
	var before, after string
	if err := f.pool.QueryRow(ctx, `SELECT before_data::text, after_data::text FROM platform.audit_events WHERE correlation_id = $1 AND action = 'organization.user.status_changed'`, res.RunID).Scan(&before, &after); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(before, `"active"`) || !strings.Contains(after, `"inactive"`) || !strings.Contains(after, `"directory"`) || strings.Contains(before+after, "example.test") {
		t.Errorf("before=%s after=%s", before, after)
	}
	eq(t, "session revoked audit", f.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'auth.session.revoked'`, res.RunID), 1)
	ev := f.events(res.RunID)[a.id]
	if !ev.StatusChanged || len(ev.ChangedFields) == 0 {
		t.Errorf("event = %+v", ev)
	}

	// Re-enabling reactivates only the user the sync deactivated; the old session stays revoked.
	disable(true)
	res = f.mustRun()
	f.expectCounts(res, map[string]int{"usersDeactivated": 0, "usersActivated": 1, "sessionsRevoked": 0})
	eq(t, "alice status", f.userRow("alice").status, "active")
	eq(t, "platform status", f.userRow("platform").status, "inactive")
	eq(t, "departed status", f.userRow("departed").status, "departed")
	if !f.sessionRevoked(session) {
		t.Error("re-enabling must not revive a revoked session")
	}
	eq(t, "status audit", f.syncAudit(res.RunID, "organization.user.status_changed"), 1)
}

func TestSyncNotObservedSweepAndReappear(t *testing.T) {
	f := newSyncFixture(t, 100)
	f.setUsers(f.user("alice"), f.user("bob"), f.user("carol"))
	f.mustRun()
	session := f.insertSession(f.userRow("bob").id)

	f.setUsers(f.user("alice"), f.user("carol"))
	res := f.mustRun()
	f.expectCounts(res, map[string]int{"usersNotObserved": 1, "usersDeactivated": 1, "usersObserved": 2, "usersUnchanged": 2, "sessionsRevoked": 1})
	bob := f.identity("bob")
	run := f.runRow(res.RunID)
	if bob.enabled || bob.deleted == nil || !bob.deleted.Equal(*run.ObservedAt) {
		t.Errorf("bob identity = %+v", bob)
	}
	eq(t, "bob status", f.userRow("bob").status, "inactive")
	if !f.sessionRevoked(session) {
		t.Error("session of a not-observed user must be revoked")
	}
	eq(t, "status audit", f.syncAudit(res.RunID, "organization.user.status_changed"), 1)
	if ev := f.events(res.RunID)[bob.userID]; !ev.StatusChanged {
		t.Errorf("event = %+v", ev)
	}

	// A third run does not move deleted_observed_at.
	res3 := f.mustRun()
	f.expectCounts(res3, map[string]int{"usersNotObserved": 0, "usersDeactivated": 0})
	if d := f.identity("bob").deleted; d == nil || !d.Equal(*bob.deleted) {
		t.Errorf("deleted_observed_at moved: %v", d)
	}

	// Reappearing clears the deletion and reactivates the user the sync deactivated.
	f.setUsers(f.user("alice"), f.user("bob"), f.user("carol"))
	res4 := f.mustRun()
	f.expectCounts(res4, map[string]int{"usersUpdated": 1, "usersActivated": 1, "usersCreated": 0})
	bob = f.identity("bob")
	if !bob.enabled || bob.deleted != nil {
		t.Errorf("bob identity = %+v", bob)
	}
	eq(t, "bob status", f.userRow("bob").status, "active")
}

func TestSyncSafeguardAbortLeavesDataUnchanged(t *testing.T) {
	f := newSyncFixture(t, 10)
	var all []public.DirectoryUser
	for i := 0; i < 10; i++ {
		all = append(all, f.user(fmt.Sprintf("u%02d", i)))
	}
	f.setUsers(all...)
	f.setGroups(f.group("g", []string{"u00", "u05"}, nil))
	f.mustRun()
	before := f.userRow("u05")
	session := f.insertSession(before.id)

	f.setUsers(all[0], all[1]) // 8 of 10 missing: more than 5 and more than 10%
	f.setGroups(f.group("g", []string{"u00"}, nil))
	res, err := f.run()
	if !errors.Is(err, public.ErrSyncSafeguard) || !jobs.IsPermanent(err) {
		t.Fatalf("err = %v, want permanent ErrSyncSafeguard", err)
	}
	eq(t, "outcome", res.Outcome, "aborted_safeguard")
	run := f.runRow(res.RunID)
	eq(t, "run outcome", run.Outcome, "aborted_safeguard")
	if run.FinishedAt == nil || run.Error == nil || !strings.Contains(*run.Error, "8 of 10") {
		t.Errorf("run = %+v", run)
	}
	eq(t, "active identities", f.count(`SELECT count(*) FROM organization.external_identities WHERE provider_key = $1 AND enabled AND deleted_observed_at IS NULL`, f.provider), 10)
	eq(t, "active users", f.count(`SELECT count(*) FROM organization.users u JOIN organization.external_identities e ON e.user_id = u.id WHERE e.provider_key = $1 AND u.status = 'active'`, f.provider), 10)
	after := f.userRow("u05")
	if !after.updatedAt.Equal(before.updatedAt) || after.status != "active" {
		t.Errorf("user changed: %+v -> %+v", before, after)
	}
	if f.sessionRevoked(session) {
		t.Error("aborted run must not revoke sessions")
	}
	eq(t, "open memberships", len(f.openMembers("g")), 2)
	eq(t, "aborted audit", f.syncAudit(res.RunID, "organization.directory_sync.aborted"), 1)
	eq(t, "all audit rows of the run", f.auditRows(res.RunID), 1)
	var target string
	if err := f.pool.QueryRow(context.Background(), `SELECT target_type || ':' || target_id FROM platform.audit_events WHERE correlation_id = $1`, res.RunID).Scan(&target); err != nil || target != "directory_sync_run:"+res.RunID {
		t.Errorf("target = %q (%v)", target, err)
	}
	eq(t, "outbox rows", f.outboxRows(res.RunID), 0)

	// Boundary: deactivating exactly 5 identities is never a safeguard abort.
	f.setUsers(all[0], all[1], all[2], all[3], all[4]) // 5 missing of 10
	f.setGroups()
	res, err = f.run()
	if err != nil || res.Outcome != "succeeded" {
		t.Fatalf("5 deactivations: %v %+v", err, res)
	}
	f.expectCounts(res, map[string]int{"usersNotObserved": 5})
}

func TestSyncEmailInUse(t *testing.T) {
	f := newSyncFixture(t, 100)
	f.platformUser("Platform Pat", strings.ToUpper(f.email("pat")))
	pat := f.user("pat")
	dup1, dup2 := f.user("dup1"), f.user("dup2")
	dup1.Email, dup2.Email = ptr(f.email("shared")), ptr(strings.ToUpper(f.email("shared")))
	f.setUsers(f.user("alice"), pat, dup1, dup2)
	f.setGroups(f.group("g", []string{"alice", "pat", "dup2"}, nil))

	res := f.mustRun()
	f.expectCounts(res, map[string]int{"usersObserved": 4, "usersCreated": 2, "usersUnchanged": 0, "unresolvedMembers": 2, "membershipsOpened": 1})
	eq(t, "conflict count", res.ConflictCount, 2)
	if f.hasUser("pat") || f.hasUser("dup2") || !f.hasUser("dup1") {
		t.Error("pat and the second duplicate must be skipped, the first duplicate created")
	}
	// No account was linked to the existing platform user (D2).
	eq(t, "identities", f.count(`SELECT count(*) FROM organization.external_identities WHERE provider_key = $1`, f.provider), 2)
	run := f.runRow(res.RunID)
	if len(run.Conflicts) != 2 {
		t.Fatalf("conflicts = %+v", run.Conflicts)
	}
	raw, _ := json.Marshal(run.Conflicts)
	if strings.Contains(string(raw), "example.test") {
		t.Errorf("conflicts carry email values: %s", raw)
	}
	for _, c := range run.Conflicts {
		if c.Kind != "email_in_use" || c.ExternalID == "" || c.Username == "" {
			t.Errorf("conflict = %+v", c)
		}
	}
	eq(t, "created audit", f.syncAudit(res.RunID, "organization.user.created_from_directory"), 2)

	// The conflict is recorded again on every run.
	res2 := f.mustRun()
	eq(t, "conflict count 2nd run", res2.ConflictCount, 2)
	f.expectCounts(res2, map[string]int{"usersObserved": 4, "usersCreated": 0, "usersUnchanged": 2})
	eq(t, "audit rows 2nd run", f.auditRows(res2.RunID), 0)
}

func TestSyncEmailChangeInUseKeepsOldValue(t *testing.T) {
	f := newSyncFixture(t, 100)
	f.setUsers(f.user("alice"), f.user("bob"))
	f.mustRun()
	alice := f.user("alice")
	alice.Email = ptr(f.email("bob"))
	f.setUsers(alice, f.user("bob"))

	res := f.mustRun()
	eq(t, "conflict count", res.ConflictCount, 1)
	run := f.runRow(res.RunID)
	if len(run.Conflicts) != 1 || run.Conflicts[0].Kind != "email_in_use" || run.Conflicts[0].Username != "alice" {
		t.Errorf("conflicts = %+v", run.Conflicts)
	}
	if e := f.userRow("alice").email; e == nil || *e != f.email("alice") {
		t.Errorf("alice email = %v, want unchanged", e)
	}
	eq(t, "outbox rows", f.outboxRows(res.RunID), 0)

	// Still in conflict on the next run, without churn.
	res2 := f.mustRun()
	eq(t, "conflict count 2nd run", res2.ConflictCount, 1)
	f.expectCounts(res2, map[string]int{"usersUpdated": 0, "usersUnchanged": 2})
	eq(t, "outbox rows 2nd run", f.outboxRows(res2.RunID), 0)

	// The address is released by bob in this run; alice is evaluated against the
	// state before the run (conservative), so she still conflicts once more and
	// the address is applied on the following run.
	bob := f.user("bob")
	bob.Email = ptr(f.email("bob2"))
	f.setUsers(alice, bob)
	res3 := f.mustRun()
	eq(t, "conflict count 3rd run", res3.ConflictCount, 1)
	res4 := f.mustRun()
	eq(t, "conflict count 4th run", res4.ConflictCount, 0)
	if e := f.userRow("alice").email; e == nil || *e != f.email("bob") {
		t.Errorf("alice email = %v, want released address", e)
	}
	if ev := f.events(res4.RunID)[f.userRow("alice").id]; strings.Join(ev.ChangedFields, ",") != "primaryEmail" {
		t.Errorf("event = %+v", ev)
	}
}

func TestSyncManagers(t *testing.T) {
	f := newSyncFixture(t, 100)
	boss, alice, bob, carol, dan, eve := f.user("boss"), f.user("alice"), f.user("bob"), f.user("carol"), f.user("dan"), f.user("eve")
	alice.ManagerExternalID = ptr(boss.ExternalID)
	bob.ManagerExternalID = ptr(f.ext("nobody")) // not in the snapshot
	carol.ManagerUnresolved = true
	dan.ManagerExternalID = ptr(dan.ExternalID) // self
	f.setUsers(boss, alice, bob, carol, dan, eve)

	res := f.mustRun()
	eq(t, "conflict count", res.ConflictCount, 2) // bob, carol; self reference is not a conflict
	run := f.runRow(res.RunID)
	kinds := map[string]string{}
	for _, c := range run.Conflicts {
		kinds[c.Username] = c.Kind
	}
	if kinds["bob"] != "manager_unresolved" || kinds["carol"] != "manager_unresolved" || len(kinds) != 2 {
		t.Errorf("conflicts = %+v", run.Conflicts)
	}
	bossID := f.userRow("boss").id
	if m := f.userRow("alice").manager; m == nil || *m != bossID {
		t.Errorf("alice manager = %v", m)
	}
	for _, n := range []string{"bob", "carol", "dan", "eve", "boss"} {
		if m := f.userRow(n).manager; m != nil {
			t.Errorf("%s manager = %v, want null", n, *m)
		}
	}

	// Manager changes are applied and announced even when no other attribute changed.
	alice.ManagerExternalID = ptr(eve.ExternalID)
	f.setUsers(boss, alice, bob, carol, dan, eve)
	res2 := f.mustRun()
	f.expectCounts(res2, map[string]int{"usersUpdated": 1, "usersUnchanged": 5})
	if m := f.userRow("alice").manager; m == nil || *m != f.userRow("eve").id {
		t.Errorf("alice manager = %v", m)
	}
	ev := f.events(res2.RunID)[f.userRow("alice").id]
	if strings.Join(ev.ChangedFields, ",") != "managerUserId" {
		t.Errorf("event = %+v", ev)
	}
	// Removing the manager clears the reference.
	alice.ManagerExternalID = nil
	f.setUsers(boss, alice, bob, carol, dan, eve)
	f.mustRun()
	if m := f.userRow("alice").manager; m != nil {
		t.Errorf("alice manager = %v, want null", *m)
	}
}

func TestSyncGroupsLifecycle(t *testing.T) {
	f := newSyncFixture(t, 100)
	f.setUsers(f.user("alice"), f.user("bob"))
	staff, ops := f.group("staff", []string{"alice", "bob"}, nil), f.group("ops", []string{"alice"}, nil)
	staff.Description = ptr("All staff")
	f.setGroups(staff, ops)
	res := f.mustRun()
	f.expectCounts(res, map[string]int{"groupsObserved": 2, "groupsCreated": 2, "groupsUpdated": 0, "groupsNotObserved": 0})
	_, _, _, firstObserved, _ := f.groupState("ops")

	// Update display name and description.
	staff.DisplayName = "Staff Renamed"
	staff.Description = nil
	f.setGroups(staff, ops)
	res = f.mustRun()
	f.expectCounts(res, map[string]int{"groupsUpdated": 1, "groupsCreated": 0, "groupsNotObserved": 0, "membershipsOpened": 0})
	_, display, _, _, _ := f.groupState("staff")
	eq(t, "display", display, "Staff Renamed")
	eq(t, "description null", f.count(`SELECT count(*) FROM organization.directory_groups WHERE provider_key = $1 AND external_id = $2 AND description IS NULL`, f.provider, staff.ExternalID), 1)

	// Not observed: soft-deleted, open memberships closed.
	f.setGroups(staff)
	res = f.mustRun()
	f.expectCounts(res, map[string]int{"groupsNotObserved": 1, "membershipsClosed": 1})
	_, _, deleted, _, _ := f.groupState("ops")
	if deleted == nil || !deleted.Equal(*f.runRow(res.RunID).ObservedAt) {
		t.Errorf("ops deleted = %v", deleted)
	}
	eq(t, "ops members", len(f.openMembers("ops")), 0)
	// A further run does not touch the deletion time or count it again.
	res = f.mustRun()
	f.expectCounts(res, map[string]int{"groupsNotObserved": 0})

	// Reappears: undeleted, first_observed_at kept, a new membership interval opens.
	f.setGroups(staff, ops)
	res = f.mustRun()
	f.expectCounts(res, map[string]int{"groupsUpdated": 1, "groupsCreated": 0, "membershipsOpened": 1})
	_, _, deleted, firstAgain, _ := f.groupState("ops")
	if deleted != nil || !firstAgain.Equal(firstObserved) {
		t.Errorf("ops after reappear: deleted=%v first=%v want first=%v", deleted, firstAgain, firstObserved)
	}
	eq(t, "ops members", strings.Join(f.openMembers("ops"), ","), "alice")
	eq(t, "ops membership intervals", f.count(`SELECT count(*) FROM organization.directory_group_memberships m JOIN organization.directory_groups g ON g.id = m.group_id WHERE g.provider_key = $1 AND g.external_id = $2`, f.provider, ops.ExternalID), 2)
}

func TestSyncMembershipIntervals(t *testing.T) {
	f := newSyncFixture(t, 100)
	f.setUsers(f.user("alice"), f.user("bob"))
	f.setGroups(f.group("g", []string{"alice", "bob"}, nil))
	r1 := f.mustRun()
	f.expectCounts(r1, map[string]int{"membershipsOpened": 2, "membershipsClosed": 0})
	obs1 := *f.runRow(r1.RunID).ObservedAt

	f.setGroups(f.group("g", []string{"alice"}, nil))
	r2 := f.mustRun()
	f.expectCounts(r2, map[string]int{"membershipsOpened": 0, "membershipsClosed": 1})
	obs2 := *f.runRow(r2.RunID).ObservedAt
	eq(t, "open members", strings.Join(f.openMembers("g"), ","), "alice")
	var from, last, until time.Time
	bobID := f.userRow("bob").id
	if err := f.pool.QueryRow(context.Background(), `SELECT observed_from, last_observed_at, observed_until FROM organization.directory_group_memberships WHERE user_id = $1`, bobID).Scan(&from, &last, &until); err != nil {
		t.Fatal(err)
	}
	if !from.Equal(obs1) || !last.Equal(obs1) || !until.Equal(obs2) {
		t.Errorf("closed interval = %v %v %v", from, last, until)
	}
	// Alice's open interval got a freshness bump but kept its start.
	if err := f.pool.QueryRow(context.Background(), `SELECT observed_from, last_observed_at FROM organization.directory_group_memberships WHERE user_id = $1 AND observed_until IS NULL`, f.userRow("alice").id).Scan(&from, &last); err != nil {
		t.Fatal(err)
	}
	if !from.Equal(obs1) || !last.Equal(obs2) {
		t.Errorf("alice interval = %v %v", from, last)
	}

	// Re-adding bob opens a NEW interval; history keeps both.
	f.setGroups(f.group("g", []string{"alice", "bob"}, nil))
	r3 := f.mustRun()
	f.expectCounts(r3, map[string]int{"membershipsOpened": 1, "membershipsClosed": 0})
	obs3 := *f.runRow(r3.RunID).ObservedAt
	eq(t, "bob intervals", f.count(`SELECT count(*) FROM organization.directory_group_memberships WHERE user_id = $1`, bobID), 2)
	if err := f.pool.QueryRow(context.Background(), `SELECT observed_from FROM organization.directory_group_memberships WHERE user_id = $1 AND observed_until IS NULL`, bobID).Scan(&from); err != nil || !from.Equal(obs3) {
		t.Errorf("reopened interval from = %v (%v), want %v", from, err, obs3)
	}

	// The read API lists only currently observed members with observedFrom.
	gid, _, _, _, _ := f.groupState("g")
	page, err := f.repo.ListDirectoryGroupMembers(context.Background(), gid, application.Page{})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("members = %+v (%v)", page, err)
	}
	for _, m := range page.Items {
		want := obs1
		if m.UserID == bobID {
			want = obs3
		}
		if !m.ObservedFrom.Equal(want) {
			t.Errorf("observedFrom of %s = %v, want %v", m.UserID, m.ObservedFrom, want)
		}
	}
}

func TestSyncNestingAndUnresolvedMembers(t *testing.T) {
	f := newSyncFixture(t, 100)
	f.setUsers(f.user("alice"))
	outer := f.group("outer", []string{"alice"}, []string{"inner", "missing", "outer"}) // missing group and self-reference
	outer.MemberUserIDs = append(outer.MemberUserIDs, f.ext("ghost"))
	outer.UnresolvedMembers = 3 // foreign principals reported by the source
	inner := f.group("inner", nil, nil)
	f.setGroups(outer, inner)

	res := f.mustRun()
	// ghost user + missing group + 3 reported by the source; the self edge is ignored silently.
	f.expectCounts(res, map[string]int{"nestingOpened": 1, "unresolvedMembers": 5, "membershipsOpened": 1})
	nest := func() int {
		return f.count(`SELECT count(*) FROM organization.directory_group_nesting n JOIN organization.directory_groups g ON g.id = n.parent_group_id WHERE g.provider_key = $1 AND n.observed_until IS NULL`, f.provider)
	}
	eq(t, "open nesting", nest(), 1)

	res = f.mustRun()
	f.expectCounts(res, map[string]int{"nestingOpened": 0, "nestingClosed": 0})

	outer.MemberGroupIDs = nil
	f.setGroups(outer, inner)
	res = f.mustRun()
	f.expectCounts(res, map[string]int{"nestingOpened": 0, "nestingClosed": 1})
	eq(t, "open nesting", nest(), 0)

	outer.MemberGroupIDs = []string{inner.ExternalID}
	f.setGroups(outer, inner)
	res = f.mustRun()
	f.expectCounts(res, map[string]int{"nestingOpened": 1, "nestingClosed": 0})
	eq(t, "nesting history rows", f.count(`SELECT count(*) FROM organization.directory_group_nesting n JOIN organization.directory_groups g ON g.id = n.parent_group_id WHERE g.provider_key = $1`, f.provider), 2)

	// A deleted child closes the edge.
	f.setGroups(outer)
	res = f.mustRun()
	f.expectCounts(res, map[string]int{"groupsNotObserved": 1, "nestingClosed": 1})
	eq(t, "open nesting", nest(), 0)
}

func TestSyncAbandonedRunCleanupAndConcurrentRejection(t *testing.T) {
	f := newSyncFixture(t, 10)
	f.setUsers(f.user("alice"))
	ctx := context.Background()

	// A run that started within the timeout blocks a new one.
	var live string
	if err := f.pool.QueryRow(ctx, `INSERT INTO organization.directory_sync_runs(provider_key, trigger, started_at) VALUES ($1,'manual',$2) RETURNING id::text`,
		f.provider, clockBase.Add(-time.Minute)).Scan(&live); err != nil {
		t.Fatal(err)
	}
	res, err := f.run()
	if !errors.Is(err, public.ErrSyncAlreadyRunning) || jobs.IsPermanent(err) || res.RunID != "" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	eq(t, "runs", f.count(`SELECT count(*) FROM organization.directory_sync_runs WHERE provider_key = $1`, f.provider), 1)
	eq(t, "live run untouched", f.runRow(live).Outcome, "running")

	// Another provider is not blocked by it.
	other := &fakeSource{key: f.provider + "-other", snap: f.src.snap}
	otherRes, err := f.sync.Run(ctx, other, public.SyncTriggerManual)
	if err != nil {
		t.Fatalf("other provider: %v", err)
	}
	t.Cleanup(func() {
		o := *f
		o.provider = other.key
		o.cleanup()
	})
	eq(t, "other provider trigger", f.runRow(otherRes.RunID).Trigger, "manual")

	// Once it is older than the run timeout it is closed as abandoned.
	if _, err := f.pool.Exec(ctx, `UPDATE organization.directory_sync_runs SET started_at = $2 WHERE id = $1`, live, clockBase.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	res = f.mustRun()
	old := f.runRow(live)
	if old.Outcome != "failed" || old.Error == nil || *old.Error != "abandoned" || old.FinishedAt == nil {
		t.Errorf("abandoned run = %+v", old)
	}
	eq(t, "new run outcome", f.runRow(res.RunID).Outcome, "succeeded")
}

func TestSyncInvalidSnapshotIsPermanentAndChangesNothing(t *testing.T) {
	f := newSyncFixture(t, 10)
	good := f.user("alice")
	cases := map[string]public.DirectorySnapshot{
		"empty":                   {},
		"duplicate user":          {Users: []public.DirectoryUser{good, good}},
		"user id equals group id": {Users: []public.DirectoryUser{good}, Groups: []public.DirectoryGroup{{ExternalID: good.ExternalID, DisplayName: "G"}}},
		"duplicate group":         {Users: []public.DirectoryUser{good}, Groups: []public.DirectoryGroup{{ExternalID: "g1", DisplayName: "G"}, {ExternalID: "g1", DisplayName: "G"}}},
		"empty user id":           {Users: []public.DirectoryUser{{DisplayName: "X"}}},
		"empty display name":      {Users: []public.DirectoryUser{{ExternalID: "x"}}},
	}
	for name, snap := range cases {
		t.Run(name, func(t *testing.T) {
			f.src.snap = snap
			res, err := f.run()
			if !errors.Is(err, public.ErrInvalidSnapshot) || !jobs.IsPermanent(err) {
				t.Fatalf("err = %v", err)
			}
			run := f.runRow(res.RunID)
			if run.Outcome != "failed" || run.Error == nil || !strings.HasPrefix(*run.Error, "invalid snapshot") || run.FinishedAt == nil {
				t.Errorf("run = %+v", run)
			}
			eq(t, "identities", f.count(`SELECT count(*) FROM organization.external_identities WHERE provider_key = $1`, f.provider), 0)
			eq(t, "groups", f.count(`SELECT count(*) FROM organization.directory_groups WHERE provider_key = $1`, f.provider), 0)
		})
	}
}

func TestSyncFetchErrorFailsRunRetryably(t *testing.T) {
	f := newSyncFixture(t, 10)
	f.src.err = errors.New("search failed\n" + strings.Repeat("x", 800))
	res, err := f.run()
	if err == nil || jobs.IsPermanent(err) || !errors.Is(err, f.src.err) {
		t.Fatalf("err = %v, want retryable wrapped fetch error", err)
	}
	run := f.runRow(res.RunID)
	if run.Outcome != "failed" || run.Error == nil || !strings.HasPrefix(*run.Error, "fetch failed: search failed") || len(*run.Error) > 500 || strings.ContainsRune(*run.Error, '\n') {
		t.Errorf("run = %+v", run)
	}
	eq(t, "audit rows", f.auditRows(res.RunID), 0)
	// The failed run does not block the next attempt.
	f.src.err = nil
	f.setUsers(f.user("alice"))
	f.mustRun()
}

func TestSyncCancelledContextStillRecordsFailure(t *testing.T) {
	f := newSyncFixture(t, 10)
	f.setUsers(f.user("alice"))
	ctx, cancel := context.WithCancel(context.Background())
	src := cancelSource{fakeSource: f.src, cancel: cancel}
	res, err := f.sync.Run(ctx, src, public.SyncTriggerScheduled)
	if err == nil {
		t.Fatal("expected error")
	}
	run := f.runRow(res.RunID)
	eq(t, "outcome", run.Outcome, "failed")
}

type cancelSource struct {
	*fakeSource
	cancel context.CancelFunc
}

func (s cancelSource) Fetch(ctx context.Context) (public.DirectorySnapshot, error) {
	s.cancel()
	return public.DirectorySnapshot{}, ctx.Err()
}

func TestSyncLargeSnapshotScales(t *testing.T) {
	if testing.Short() {
		t.Skip("large snapshot")
	}
	f := newSyncFixture(t, 100)
	const n = 3000
	users := make([]public.DirectoryUser, 0, n)
	names := make([]string, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("big%05d", i)
		u := f.user(name)
		if i > 0 {
			u.ManagerExternalID = ptr(f.ext("big00000"))
		}
		users = append(users, u)
		names = append(names, name)
	}
	f.setUsers(users...)
	f.setGroups(f.group("all", names, nil), f.group("half", names[:n/2], []string{"all"}))
	start := time.Now()
	r1 := f.mustRun()
	f.expectCounts(r1, map[string]int{"usersCreated": n, "membershipsOpened": n + n/2, "nestingOpened": 1})
	eq(t, "events", len(f.events(r1.RunID)), n)
	r2 := f.mustRun()
	f.expectCounts(r2, map[string]int{"usersUnchanged": n, "usersUpdated": 0, "membershipsOpened": 0, "membershipsClosed": 0})
	eq(t, "second run audit", f.auditRows(r2.RunID), 0)
	t.Logf("two runs over %d users took %v", n, time.Since(start))
}

// ---- read API and manual request ----

func TestListUserExternalIdentitiesAndSyncRuns(t *testing.T) {
	f := newSyncFixture(t, 100)
	f.setUsers(f.user("alice"), f.user("bob"))
	r1 := f.mustRun()
	f.setUsers(f.user("alice"))
	r2 := f.mustRun()
	ctx := context.Background()

	ids, err := f.repo.ListUserExternalIdentities(ctx, f.userRow("bob").id)
	if err != nil || len(ids) != 1 {
		t.Fatalf("identities = %+v (%v)", ids, err)
	}
	if ids[0].ProviderKey != f.provider || ids[0].Username == nil || *ids[0].Username != "bob" || ids[0].Enabled || ids[0].LastSeenAt == nil || ids[0].DeletedObservedAt == nil {
		t.Errorf("identity = %+v", ids[0])
	}
	if _, err := f.repo.ListUserExternalIdentities(ctx, "00000000-0000-7000-8000-000000000000"); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
	none := f.platformUser("No Directory", f.email("nodir"))
	ids, err = f.repo.ListUserExternalIdentities(ctx, none)
	if err != nil || ids == nil || len(ids) != 0 {
		t.Errorf("user without identities: %+v (%v)", ids, err)
	}

	// Newest first, filtered by provider, keyset paginated.
	page1, err := f.repo.ListDirectorySyncRuns(ctx, application.RunFilter{ProviderKey: f.provider, Page: application.Page{Limit: 1}})
	if err != nil || len(page1.Items) != 1 || page1.Items[0].ID != r2.RunID || page1.NextCursor != r2.RunID {
		t.Fatalf("page1 = %+v (%v)", page1, err)
	}
	page2, err := f.repo.ListDirectorySyncRuns(ctx, application.RunFilter{ProviderKey: f.provider, Page: application.Page{Limit: 1, Cursor: page1.NextCursor}})
	if err != nil || len(page2.Items) != 1 || page2.Items[0].ID != r1.RunID || page2.NextCursor != "" {
		t.Fatalf("page2 = %+v (%v)", page2, err)
	}
	if _, err := f.repo.ListDirectorySyncRuns(ctx, application.RunFilter{Page: application.Page{Cursor: "bad"}}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Errorf("bad cursor: %v", err)
	}
	empty, err := f.repo.ListDirectorySyncRuns(ctx, application.RunFilter{ProviderKey: f.provider + "-none"})
	if err != nil || len(empty.Items) != 0 {
		t.Errorf("other provider: %+v (%v)", empty, err)
	}
	run, err := f.repo.GetDirectorySyncRun(ctx, r1.RunID)
	if err != nil || run.Counts["usersCreated"] != 2 || run.Conflicts == nil || run.ProviderKey != f.provider {
		t.Errorf("run = %+v (%v)", run, err)
	}
	if _, err := f.repo.GetDirectorySyncRun(ctx, "nope"); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown run: %v", err)
	}
}

func TestRequestDirectorySyncEnqueuesAuditsAndDeduplicates(t *testing.T) {
	f := newSyncFixture(t, 100)
	ctx := context.Background()
	actor := f.platformUser("Requesting Admin", f.email("admin"))
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.jobs WHERE dedupe_key = $1 OR payload->>'providerKey' = $2`, public.DirectorySyncDedupeKey(f.provider), f.provider)
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE target_type = 'directory_provider' AND target_id = $1`, f.provider)
	})

	id1, created, err := f.repo.RequestDirectorySync(ctx, actor, f.provider)
	if err != nil || !created || id1 == "" {
		t.Fatalf("first: %q %v %v", id1, created, err)
	}
	var typ, payload string
	var attempts int
	if err := f.pool.QueryRow(ctx, `SELECT job_type, payload::text, max_attempts FROM platform.jobs WHERE id = $1`, id1).Scan(&typ, &payload, &attempts); err != nil {
		t.Fatal(err)
	}
	var p public.DirectorySyncJobPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil || typ != public.DirectorySyncJobType || p.ProviderKey != f.provider || p.Trigger != public.SyncTriggerManual || attempts != 3 {
		t.Errorf("job = %s %s %d (%v)", typ, payload, attempts, err)
	}

	id2, created2, err := f.repo.RequestDirectorySync(ctx, actor, f.provider)
	if err != nil || created2 || id2 != id1 {
		t.Fatalf("second: %q %v %v, want %q false", id2, created2, err, id1)
	}

	rows, err := f.pool.Query(ctx, `SELECT actor_id::text, target_id, metadata->>'jobId', metadata->>'created' FROM platform.audit_events
		WHERE action = 'organization.directory_sync.requested' AND target_type = 'directory_provider' AND target_id = $1 ORDER BY occurred_at, id`, f.provider)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got [][3]string
	for rows.Next() {
		var a, target, job, c string
		if err := rows.Scan(&a, &target, &job, &c); err != nil {
			t.Fatal(err)
		}
		if a != actor || target != f.provider {
			t.Errorf("audit actor/target = %s %s", a, target)
		}
		got = append(got, [3]string{"", job, c})
	}
	if len(got) != 2 || got[0][1] != id1 || got[1][1] != id1 || got[0][2] != "true" || got[1][2] != "false" {
		// Order of the two rows is by time; accept either order but require one true and one false.
		seen := map[string]int{}
		for _, g := range got {
			seen[g[2]]++
		}
		if len(got) != 2 || seen["true"] != 1 || seen["false"] != 1 {
			t.Errorf("audit rows = %+v", got)
		}
	}
	if _, _, err := f.repo.RequestDirectorySync(ctx, "not-a-uuid", f.provider); err == nil {
		t.Error("non-user actor must be rejected")
	}
}

func TestStartRunConcurrentCallsAdmitExactlyOne(t *testing.T) {
	f := newSyncFixture(t, 10)
	const workers = 8
	var wg sync.WaitGroup
	results := make(chan error, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.repo.StartRun(context.Background(), f.provider, public.SyncTriggerScheduled, clockBase, clockBase.Add(-time.Hour))
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	ok, rejected := 0, 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, public.ErrSyncAlreadyRunning):
			rejected++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || rejected != workers-1 {
		t.Errorf("admitted=%d rejected=%d", ok, rejected)
	}
}
