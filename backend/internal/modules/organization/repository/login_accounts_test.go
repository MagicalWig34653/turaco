package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authentication"
)

type identitySpec struct {
	userID, subject, username, dn string
	enabled                       bool
	deleted                       bool
}

// identity inserts an external identity of provider and registers cleanup.
func (f *fixture) identity(provider string, s identitySpec) {
	f.t.Helper()
	var dn any
	if s.dn != "" {
		dn = s.dn
	}
	var deleted any
	if s.deleted {
		deleted = time.Now().UTC().Truncate(time.Microsecond)
	}
	f.insert(`
		INSERT INTO organization.external_identities(user_id, provider_key, external_subject, username, distinguished_name, enabled, deleted_observed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id::text`,
		`DELETE FROM organization.external_identities WHERE id = $1`,
		s.userID, provider, s.subject, s.username, dn, s.enabled, deleted)
}

func (f *fixture) setEmail(userID, email string) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), `UPDATE organization.users SET primary_email = $2 WHERE id = $1`, userID, email); err != nil {
		f.t.Fatal(err)
	}
}

func newLoginAccounts(f *fixture) *application.LoginAccounts {
	return application.NewLoginAccounts(f.repo, nil)
}

func TestFindDirectoryAccount(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	prov := "zl-" + f.pfx
	dn := func(n string) string { return "CN=" + n + ",OU=Users,DC=example,DC=test" }

	alice := f.user("Alice "+f.pfx, "active")
	f.setEmail(alice, "Alice."+f.pfx+"@Example.Test")
	f.identity(prov, identitySpec{userID: alice, subject: f.pfx + "-a", username: "Alice" + f.pfx, dn: dn("alice"), enabled: true})

	disabled := f.user("Disabled "+f.pfx, "active")
	f.identity(prov, identitySpec{userID: disabled, subject: f.pfx + "-d", username: "dis" + f.pfx, dn: dn("dis"), enabled: false})
	deleted := f.user("Deleted "+f.pfx, "active")
	f.identity(prov, identitySpec{userID: deleted, subject: f.pfx + "-x", username: "del" + f.pfx, dn: dn("del"), enabled: true, deleted: true})
	noDN := f.user("NoDN "+f.pfx, "active")
	f.identity(prov, identitySpec{userID: noDN, subject: f.pfx + "-n", username: "nodn" + f.pfx, dn: "", enabled: true})
	inactive := f.user("Inactive "+f.pfx, "inactive")
	f.identity(prov, identitySpec{userID: inactive, subject: f.pfx + "-i", username: "inact" + f.pfx, dn: dn("inact"), enabled: true})
	// Two identities with the same username are ambiguous.
	u1 := f.user("Dup1 "+f.pfx, "active")
	u2 := f.user("Dup2 "+f.pfx, "active")
	f.identity(prov, identitySpec{userID: u1, subject: f.pfx + "-d1", username: "dup" + f.pfx, dn: dn("d1"), enabled: true})
	f.identity(prov, identitySpec{userID: u2, subject: f.pfx + "-d2", username: "DUP" + f.pfx, dn: dn("d2"), enabled: true})

	la := newLoginAccounts(f)
	tests := []struct {
		name       string
		provider   string
		identifier string
		want       string // user id, empty = not found
	}{
		{"username", prov, "Alice" + f.pfx, alice},
		{"username case-insensitive", prov, "aLICE" + strings.ToUpper(f.pfx), alice},
		{"DOMAIN backslash user", prov, `EXAMPLE\alice` + f.pfx, alice},
		{"email", prov, "alice." + f.pfx + "@example.test", alice},
		{"email upper", prov, "ALICE." + f.pfx + "@EXAMPLE.TEST", alice},
		{"email with spaces trimmed", prov, "  alice." + f.pfx + "@example.test ", alice},
		{"username is not matched as email", prov, "alice." + f.pfx, ""},
		{"wrong provider", "other-" + prov, "Alice" + f.pfx, ""},
		{"disabled identity", prov, "dis" + f.pfx, ""},
		{"deleted identity", prov, "del" + f.pfx, ""},
		{"empty distinguished name", prov, "nodn" + f.pfx, ""},
		{"inactive user", prov, "inact" + f.pfx, ""},
		{"ambiguous", prov, "dup" + f.pfx, ""},
		{"unknown", prov, "nobody" + f.pfx, ""},
		{"empty identifier", prov, "", ""},
		{"empty after backslash", prov, `EXAMPLE\`, ""},
		{"empty provider", "", "Alice" + f.pfx, ""},
		{"LIKE metacharacters are literal", prov, "%", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := la.FindDirectoryAccount(ctx, tt.provider, tt.identifier)
			if err != nil {
				t.Fatal(err)
			}
			if ok != (tt.want != "") || got.UserID != tt.want {
				t.Fatalf("got %+v ok=%v, want user %q", got, ok, tt.want)
			}
			if ok && got.DistinguishedName != dn("alice") {
				t.Fatalf("dn = %q", got.DistinguishedName)
			}
		})
	}
}

func TestLockActiveUser(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	active := f.user("A "+f.pfx, "active")
	inactive := f.user("I "+f.pfx, "inactive")
	for _, tt := range []struct {
		name string
		id   string
		want bool
	}{{"active", active, true}, {"inactive", inactive, false}, {"unknown", missingID, false}, {"malformed", "nope", false}} {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			got, err := f.repo.LockActiveUser(ctx, tx, tt.id)
			if err != nil || got != tt.want {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
}

func cleanupSessionsAndAudit(f *fixture, userID string) {
	f.t.Cleanup(func() {
		ctx := context.Background()
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE target_id = $1 OR target_id IN (SELECT id::text FROM platform.sessions WHERE user_id = $1)`, userID)
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.sessions WHERE user_id = $1`, userID)
	})
}

func newSessions(f *fixture) *authentication.Service {
	return authentication.NewService(f.pool, authentication.Config{IdleTimeout: 30 * time.Minute, AbsoluteTimeout: 8 * time.Hour}, nil)
}

func countSessions(t *testing.T, f *fixture, userID string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM platform.sessions WHERE user_id = $1`, userID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A login that locks the user FOR SHARE must wait for a status change that
// holds the row FOR UPDATE, and then see the new status.
func TestSessionCreationWaitsForStatusChange(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	userID := f.user("Race "+f.pfx, "active")
	cleanupSessionsAndAudit(f, userID)
	svc := newSessions(f)
	locker := newLoginAccounts(f)

	// "Directory sync": lock FOR UPDATE and deactivate, not yet committed.
	status, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = status.Rollback(ctx) }()
	if _, err := status.Exec(ctx, `SELECT 1 FROM organization.users WHERE id = $1 FOR UPDATE`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := status.Exec(ctx, `UPDATE organization.users SET status = 'inactive' WHERE id = $1`, userID); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, _, err := svc.CreateLogin(ctx, authentication.LoginSession{
			UserID: userID, AuthMethod: "ldap", CorrelationID: "race", Locker: locker,
		})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("session creation did not wait for the status change (err=%v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := status.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, authentication.ErrUserInactive) {
			t.Fatalf("err = %v, want ErrUserInactive", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session creation did not finish after the status change committed")
	}
	if n := countSessions(t, f, userID); n != 0 {
		t.Fatalf("%d sessions exist for an inactive user", n)
	}
}

// Conversely, a status change must wait for an in-flight session creation, so
// the revocation that follows it covers the new session.
func TestStatusChangeWaitsForSessionCreation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	userID := f.user("Race2 "+f.pfx, "active")
	cleanupSessionsAndAudit(f, userID)

	login, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = login.Rollback(ctx) }()
	ok, err := f.repo.LockActiveUser(ctx, login, userID)
	if err != nil || !ok {
		t.Fatalf("lock = %v, %v", ok, err)
	}

	done := make(chan error, 1)
	go func() {
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		_, err = tx.Exec(ctx, `SELECT 1 FROM organization.users WHERE id = $1 FOR UPDATE`, userID)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("status change did not wait for the login (err=%v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := login.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("status change did not proceed after the login committed")
	}
}

func TestCreateEmergencyUser(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	la := newLoginAccounts(f)
	actor := json.RawMessage(`{"actor":"cli","osUser":"tester"}`)

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id, err := la.CreateEmergencyUser(ctx, tx, "  Break Glass "+f.pfx+" ", "corr-1", actor)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE target_id = $1`, id)
		_, _ = f.pool.Exec(ctx, `DELETE FROM organization.users WHERE id = $1`, id)
	})

	var name, status, source string
	var identities int
	if err := f.pool.QueryRow(ctx, `SELECT display_name, status, status_source, (SELECT count(*) FROM organization.external_identities WHERE user_id = $1) FROM organization.users WHERE id = $1`, id).
		Scan(&name, &status, &source, &identities); err != nil {
		t.Fatal(err)
	}
	if name != "Break Glass "+f.pfx || status != "active" || source != "platform" || identities != 0 {
		t.Fatalf("user = %q %q %q identities=%d", name, status, source, identities)
	}
	var action, corr string
	var actorID *string
	var meta, after string
	if err := f.pool.QueryRow(ctx, `SELECT action, correlation_id, actor_id::text, metadata::text, after_data::text FROM platform.audit_events WHERE target_id = $1`, id).
		Scan(&action, &corr, &actorID, &meta, &after); err != nil {
		t.Fatal(err)
	}
	if action != "organization.user.created_local" || corr != "corr-1" || actorID != nil ||
		!strings.Contains(meta, `"cli"`) || !strings.Contains(after, `"statusSource": "platform"`) {
		t.Fatalf("audit = %q %q %v %s %s", action, corr, actorID, meta, after)
	}

	// Validation and rollback.
	for _, bad := range []string{"", "   ", "a\x00b", "line\nbreak", strings.Repeat("x", 201)} {
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := la.CreateEmergencyUser(ctx, tx, bad, "c", actor); !errors.Is(err, application.ErrInvalidLocalUser) {
			t.Errorf("display name %q: err = %v", bad, err)
		}
		_ = tx.Rollback(ctx)
	}
	tx2, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rolled, err := la.CreateEmergencyUser(ctx, tx2, "Rolled back "+f.pfx, "c", actor)
	if err != nil {
		t.Fatal(err)
	}
	_ = tx2.Rollback(ctx)
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM organization.users WHERE id = $1`, rolled).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rolled back user persisted: n=%d err=%v", n, err)
	}
}
