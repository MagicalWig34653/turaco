package authentication

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestCreateLoginMaxLifetimeAndKeepsCreateCompatible(t *testing.T) {
	s, clk, _ := newTestService(t)
	s.cfg.AbsoluteTimeout = 8 * time.Hour
	_, plain, err := s.Create(context.Background(), testUser, "password", "c")
	if err != nil || !plain.AbsoluteExpiresAt.Equal(clk.t.Add(8*time.Hour)) {
		t.Fatalf("Create: %+v, %v", plain, err)
	}
	_, capped, err := s.CreateLogin(context.Background(), LoginSession{UserID: testUser, AuthMethod: "emergency", CorrelationID: "c", MaxLifetime: time.Hour})
	if err != nil || !capped.AbsoluteExpiresAt.Equal(clk.t.Add(time.Hour)) {
		t.Fatalf("CreateLogin: %+v, %v", capped, err)
	}
	// A cap above the configured lifetime does not extend it.
	s.cfg.AbsoluteTimeout = 30 * time.Minute
	_, longer, err := s.CreateLogin(context.Background(), LoginSession{UserID: testUser, AuthMethod: "ldap", CorrelationID: "c", MaxLifetime: 2 * time.Hour})
	if err != nil || !longer.AbsoluteExpiresAt.Equal(clk.t.Add(30*time.Minute)) {
		t.Fatalf("CreateLogin: %+v, %v", longer, err)
	}
}

func TestCreateLoginRollsBack(t *testing.T) {
	s, _, pool := newTestService(t)
	ctx := context.Background()
	count := func() int {
		var n int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM platform.sessions WHERE user_id = $1`, testUser).Scan(&n)
		return n
	}
	boom := errors.New("boom")
	_, _, err := s.CreateLogin(ctx, LoginSession{UserID: testUser, AuthMethod: "ldap", CorrelationID: "c",
		AfterCreate: func(context.Context, pgx.Tx, Session) error { return boom }})
	if !errors.Is(err, boom) || count() != 0 {
		t.Fatalf("err=%v sessions=%d: AfterCreate failure must roll back the session", err, count())
	}
	_, _, err = s.CreateLogin(ctx, LoginSession{UserID: testUser, AuthMethod: "ldap", CorrelationID: "c", Locker: &fakeLocker{active: map[string]bool{}}})
	if !errors.Is(err, ErrUserInactive) || count() != 0 {
		t.Fatalf("err=%v sessions=%d", err, count())
	}
	lockErr := errors.New("lock failed")
	_, _, err = s.CreateLogin(ctx, LoginSession{UserID: testUser, AuthMethod: "ldap", CorrelationID: "c", Locker: &fakeLocker{err: lockErr}})
	if !errors.Is(err, lockErr) || errors.Is(err, ErrUserInactive) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := s.CreateLogin(ctx, LoginSession{UserID: testUser, AuthMethod: "ldap", ReplaceSessionID: "nope"}); err == nil {
		t.Fatal("malformed replaced session id accepted")
	}
}
