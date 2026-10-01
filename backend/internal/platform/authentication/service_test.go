package authentication

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

const testUser = "0190a000-0000-7000-8000-000000000001"

func newTestService(t *testing.T) (*Service, *fakeClock, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	pool := dbtest.Pool(t)
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('platform.sessions') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		dbtest.Unavailable(t, "platform.sessions missing; run make migrate")
	}
	clk := &fakeClock{t: time.Now().UTC().Truncate(time.Microsecond)}
	svc := NewService(pool, Config{IdleTimeout: 30 * time.Minute, AbsoluteTimeout: time.Hour, TouchInterval: time.Minute}, clk.now)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE target_type='session' AND target_id IN (SELECT id::text FROM platform.sessions WHERE user_id=$1)`, testUser)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.sessions WHERE user_id=$1`, testUser)
	})
	return svc, clk, pool
}

func mustCreate(t *testing.T, s *Service) (string, Session) {
	t.Helper()
	tok, sess, err := s.Create(context.Background(), testUser, "password", "corr-1")
	if err != nil {
		t.Fatal(err)
	}
	return tok, sess
}

func TestCreateAndAuthenticate(t *testing.T) {
	s, clk, _ := newTestService(t)
	tok, sess := mustCreate(t, s)
	if sess.UserID != testUser || sess.AuthMethod != "password" || sess.ID == "" {
		t.Fatalf("unexpected session %+v", sess)
	}
	if !sess.IdleExpiresAt.Equal(clk.t.Add(30*time.Minute)) || !sess.AbsoluteExpiresAt.Equal(clk.t.Add(time.Hour)) {
		t.Fatalf("unexpected expiries %+v", sess)
	}
	got, err := s.Authenticate(context.Background(), tok)
	if err != nil || got.ID != sess.ID {
		t.Fatalf("Authenticate = %+v, %v", got, err)
	}
}

func TestCreateValidation(t *testing.T) {
	s, _, _ := newTestService(t)
	if _, _, err := s.Create(context.Background(), "nope", "password", "c"); err == nil {
		t.Fatal("expected error for bad user id")
	}
	if _, _, err := s.Create(context.Background(), testUser, "", "c"); err == nil {
		t.Fatal("expected error for empty auth method")
	}
}

func TestCreateIdleCappedAtAbsolute(t *testing.T) {
	s, _, _ := newTestService(t)
	s.cfg.IdleTimeout = 2 * time.Hour
	_, sess := mustCreate(t, s)
	if !sess.IdleExpiresAt.Equal(sess.AbsoluteExpiresAt) {
		t.Fatalf("idle %v should equal absolute %v", sess.IdleExpiresAt, sess.AbsoluteExpiresAt)
	}
}

func TestAuthenticateUnknownAndMalformed(t *testing.T) {
	s, _, _ := newTestService(t)
	unknown, _ := newToken()
	for _, tok := range []string{"", "garbage", unknown} {
		if _, err := s.Authenticate(context.Background(), tok); !errors.Is(err, ErrInvalidSession) {
			t.Errorf("Authenticate(%q) err = %v, want ErrInvalidSession", tok, err)
		}
	}
}

func TestMalformedTokenSkipsDatabase(t *testing.T) {
	s := NewService(nil, Config{}, nil) // nil pool would panic on any DB access
	if _, err := s.Authenticate(context.Background(), "bad"); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("err = %v", err)
	}
}

func TestIdleExpiry(t *testing.T) {
	s, clk, _ := newTestService(t)
	tok, _ := mustCreate(t, s)
	clk.advance(30 * time.Minute)
	if _, err := s.Authenticate(context.Background(), tok); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("err = %v", err)
	}
}

func TestAbsoluteExpiry(t *testing.T) {
	s, clk, _ := newTestService(t)
	tok, _ := mustCreate(t, s)
	// Stay active so idle never lapses, then cross the absolute limit.
	for i := 0; i < 5; i++ {
		clk.advance(10 * time.Minute)
		if _, err := s.Authenticate(context.Background(), tok); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	clk.advance(10 * time.Minute) // t+60m == absolute
	if _, err := s.Authenticate(context.Background(), tok); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("err = %v", err)
	}
}

func TestSlidingExtensionCappedAtAbsolute(t *testing.T) {
	s, clk, _ := newTestService(t)
	tok, sess := mustCreate(t, s)
	clk.advance(20 * time.Minute)
	got, err := s.Authenticate(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IdleExpiresAt.Equal(clk.t.Add(30*time.Minute)) || !got.LastSeenAt.Equal(clk.t) {
		t.Fatalf("not extended: %+v", got)
	}
	clk.advance(25 * time.Minute) // t+45m; now+30m would exceed absolute (t+60m)
	got, err = s.Authenticate(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IdleExpiresAt.Equal(sess.AbsoluteExpiresAt) {
		t.Fatalf("idle %v should be capped at %v", got.IdleExpiresAt, sess.AbsoluteExpiresAt)
	}
}

func TestTouchIntervalThrottling(t *testing.T) {
	s, clk, _ := newTestService(t)
	tok, sess := mustCreate(t, s)
	clk.advance(30 * time.Second)
	got, err := s.Authenticate(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastSeenAt.Equal(sess.LastSeenAt) || !got.IdleExpiresAt.Equal(sess.IdleExpiresAt) {
		t.Fatalf("touched within interval: %+v", got)
	}
	clk.advance(30 * time.Second)
	got, err = s.Authenticate(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastSeenAt.Equal(clk.t) {
		t.Fatalf("expected touch at interval: %+v", got)
	}
}

func TestRevoke(t *testing.T) {
	s, _, pool := newTestService(t)
	ctx := context.Background()
	tok, sess := mustCreate(t, s)
	if err := s.Revoke(ctx, sess.ID, testUser, "corr-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, tok); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("err = %v", err)
	}
	if err := s.Revoke(ctx, sess.ID, testUser, "corr-3"); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	if err := s.Revoke(ctx, "0190a000-0000-7000-8000-0000000000ff", testUser, "corr-4"); err != nil {
		t.Fatalf("unknown revoke: %v", err)
	}
	for action, want := range map[string]int{"auth.session.created": 1, "auth.session.revoked": 1} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE target_type='session' AND target_id=$1 AND action=$2`, sess.ID, action).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("%s audit rows = %d, want %d", action, n, want)
		}
	}
}

func TestAuditContainsNoSecret(t *testing.T) {
	s, _, pool := newTestService(t)
	ctx := context.Background()
	tok, sess := mustCreate(t, s)
	if err := s.Revoke(ctx, sess.ID, testUser, "c"); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `SELECT row_to_json(a)::text FROM platform.audit_events a WHERE target_id=$1`, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	hashHex := ""
	if err := pool.QueryRow(ctx, `SELECT encode(token_hash,'hex') FROM platform.sessions WHERE id=$1`, sess.ID).Scan(&hashHex); err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			t.Fatal(err)
		}
		n++
		if strings.Contains(doc, tok) || strings.Contains(doc, hashHex) {
			t.Fatalf("audit row leaks secret: %s", doc)
		}
	}
	if n != 2 {
		t.Fatalf("audit rows = %d, want 2", n)
	}
}

func TestDistinctTokensAndHashOnlyStorage(t *testing.T) {
	s, _, pool := newTestService(t)
	ctx := context.Background()
	t1, s1 := mustCreate(t, s)
	t2, s2 := mustCreate(t, s)
	if t1 == t2 || s1.ID == s2.ID {
		t.Fatal("sessions must be distinct")
	}
	var doc string
	if err := pool.QueryRow(ctx, `SELECT row_to_json(x)::text FROM platform.sessions x WHERE id=$1`, s1.ID).Scan(&doc); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(doc, t1) {
		t.Fatal("raw token stored in row")
	}
	var stored []byte
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM platform.sessions WHERE id=$1`, s1.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(HashToken(t1)) {
		t.Fatal("stored hash mismatch")
	}
}

func TestRevokeAuditIdentifiesOwner(t *testing.T) {
	s, _, pool := newTestService(t)
	ctx := context.Background()
	_, sess := mustCreate(t, s)
	if err := s.Revoke(ctx, sess.ID, "", "c"); err != nil {
		t.Fatal(err)
	}
	var owner, method string
	err := pool.QueryRow(ctx, `SELECT metadata->>'userId', metadata->>'authMethod' FROM platform.audit_events WHERE target_id=$1 AND action='auth.session.revoked'`, sess.ID).Scan(&owner, &method)
	if err != nil {
		t.Fatal(err)
	}
	if owner != sess.UserID || method != sess.AuthMethod {
		t.Fatalf("metadata userId=%q authMethod=%q", owner, method)
	}
}

func TestRevokeUserSessions(t *testing.T) {
	s, clk, pool := newTestService(t)
	ctx := context.Background()
	tok1, _ := mustCreate(t, s)
	tok2, _ := mustCreate(t, s)
	var n int
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var err error
		n, err = RevokeUserSessions(ctx, tx, testUser, "user_deactivated", "directory-sync", "corr-revoke-all", clk.now())
		return err
	})
	if err != nil || n != 2 {
		t.Fatalf("RevokeUserSessions = %d, %v; want 2", n, err)
	}
	for _, tok := range []string{tok1, tok2} {
		if _, err := s.Authenticate(ctx, tok); !errors.Is(err, ErrInvalidSession) {
			t.Fatalf("revoked session still valid: %v", err)
		}
	}
	var audited int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events
		WHERE action = 'auth.session.revoked' AND correlation_id = 'corr-revoke-all'
		  AND actor_id IS NULL AND metadata->>'reason' = 'user_deactivated' AND metadata->>'actor' = 'directory-sync'`).Scan(&audited); err != nil || audited != 2 {
		t.Fatalf("audited = %d, %v; want 2", audited, err)
	}
	// Idempotent: nothing left to revoke, nothing audited.
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var err error
		n, err = RevokeUserSessions(ctx, tx, testUser, "user_deactivated", "directory-sync", "corr-revoke-all-2", clk.now())
		return err
	})
	if err != nil || n != 0 {
		t.Fatalf("second RevokeUserSessions = %d, %v; want 0", n, err)
	}
}

func TestRevokeUserSessionsValidation(t *testing.T) {
	_, clk, pool := newTestService(t)
	ctx := context.Background()
	_ = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := RevokeUserSessions(ctx, tx, "nope", "r", "s", "c", clk.now()); err == nil {
			t.Error("expected error for bad user id")
		}
		if _, err := RevokeUserSessions(ctx, tx, testUser, "", "s", "c", clk.now()); err == nil {
			t.Error("expected error for empty reason")
		}
		return nil
	})
}
