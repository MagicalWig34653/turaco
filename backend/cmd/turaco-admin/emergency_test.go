package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authentication"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

const emPassword = "cli-break-glass-passphrase"

type emFixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	login  string
	name   string
	stdout *bytes.Buffer
}

func newEmFixture(t *testing.T) *emFixture {
	t.Helper()
	pool := dbtest.Pool(t)
	for _, table := range []string{"platform.local_credentials", "platform.sessions"} {
		var exists bool
		if err := pool.QueryRow(context.Background(), `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil || !exists {
			dbtest.Unavailable(t, table+" missing; run make migrate")
		}
	}
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	sfx := hex.EncodeToString(b)
	f := &emFixture{t: t, pool: pool, login: "zt" + sfx, name: "Break Glass zt" + sfx, stdout: &bytes.Buffer{}}
	t.Cleanup(func() {
		ctx := context.Background()
		rows, err := pool.Query(ctx, `SELECT id::text FROM organization.users WHERE display_name = $1`, f.name)
		if err != nil {
			return
		}
		var ids []string
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE target_id = $1 OR target_id IN (SELECT id::text FROM platform.sessions WHERE user_id = $1::uuid)`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM platform.sessions WHERE user_id = $1::uuid`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM platform.local_credentials WHERE user_id = $1::uuid`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM organization.users WHERE id = $1::uuid`, id)
		}
	})
	return f
}

func (f *emFixture) env(stdin string) env {
	f.stdout.Reset()
	return env{
		pool: f.pool, stdin: strings.NewReader(stdin), stdout: f.stdout,
		logger: slog.New(slog.DiscardHandler),
		actor:  []byte(`{"actor":"cli","osUser":"tester"}`),
	}
}

func (f *emFixture) run(stdin string, args ...string) error {
	return runEmergency(context.Background(), f.env(stdin), args[0], args[1:])
}

func (f *emFixture) credential() (authentication.LocalCredential, bool) {
	c, ok, err := authentication.FindLocalCredential(context.Background(), f.pool, f.login)
	if err != nil {
		f.t.Fatal(err)
	}
	return c, ok
}

func (f *emFixture) audit(userID string) []string {
	rows, err := f.pool.Query(context.Background(),
		`SELECT action || ' ' || coalesce(actor_id::text,'-') || ' ' || metadata::text FROM platform.audit_events WHERE target_id = $1 ORDER BY id`, userID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func (f *emFixture) create(stdin string, extra ...string) error {
	args := append([]string{"create", "--login", f.login, "--display-name", f.name}, extra...)
	return f.run(stdin, args...)
}

func TestEmergencyCreateFromStdin(t *testing.T) {
	f := newEmFixture(t)
	if err := f.create(emPassword+"\r\n", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	out := f.stdout.String()
	if strings.Contains(out, emPassword) || strings.Contains(out, "Generated password") {
		t.Fatalf("a password supplied on stdin must not be echoed: %s", out)
	}
	cred, ok := f.credential()
	if !ok || cred.Enabled {
		t.Fatalf("credential = %+v ok=%v: a new account must be disabled", cred, ok)
	}
	if match, err := authentication.VerifyPasswordHash(context.Background(), cred.PasswordHash, emPassword); err != nil || !match {
		t.Fatalf("stored hash does not verify (CRLF must be trimmed): %v %v", match, err)
	}
	var status, source, display string
	if err := f.pool.QueryRow(context.Background(), `SELECT status, status_source, display_name FROM organization.users WHERE id = $1`, cred.UserID).Scan(&status, &source, &display); err != nil {
		t.Fatal(err)
	}
	if status != "active" || source != "platform" || display != f.name {
		t.Fatalf("user = %q %q %q", status, source, display)
	}
	var identities int
	_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM organization.external_identities WHERE user_id = $1`, cred.UserID).Scan(&identities)
	if identities != 0 {
		t.Fatal("emergency user must have no directory identity")
	}
	if !strings.Contains(out, "disabled") || !strings.Contains(out, "emergency enable") {
		t.Fatalf("output = %s", out)
	}
	audits := f.audit(cred.UserID)
	var actions []string
	for _, a := range audits {
		actions = append(actions, strings.Fields(a)[0])
		if !strings.Contains(a, `"osUser": "tester"`) || strings.Contains(a, " "+cred.UserID+" ") || strings.Contains(a, emPassword) || strings.Contains(a, cred.PasswordHash) {
			t.Fatalf("audit row %q", a)
		}
	}
	if got := strings.Join(actions, ","); got != "organization.user.created_local,authentication.emergency_account.created" {
		t.Fatalf("audit actions = %v", actions)
	}
}

func TestEmergencyCreateGeneratesPasswordOnce(t *testing.T) {
	f := newEmFixture(t)
	if err := f.create(""); err != nil {
		t.Fatal(err)
	}
	var pw string
	for _, line := range strings.Split(f.stdout.String(), "\n") {
		if i := strings.Index(line, "store it securely): "); i >= 0 {
			pw = strings.TrimSpace(line[i+len("store it securely): "):])
		}
	}
	if len(pw) != 24 || strings.Trim(pw, generatedPasswordAlphabet) != "" {
		t.Fatalf("generated password %q", pw)
	}
	cred, _ := f.credential()
	if ok, err := authentication.VerifyPasswordHash(context.Background(), cred.PasswordHash, pw); err != nil || !ok {
		t.Fatalf("generated password does not verify: %v %v", ok, err)
	}
	pw2, _ := generatePassword()
	if pw2 == pw {
		t.Fatal("generated passwords repeat")
	}
}

func TestEmergencyCreateValidation(t *testing.T) {
	f := newEmFixture(t)
	for name, args := range map[string][]string{
		"missing login":        {"create", "--display-name", "x"},
		"missing display name": {"create", "--login", f.login},
		"bad login":            {"create", "--login", "Bad Login", "--display-name", "x"},
		"uppercase login":      {"create", "--login", "ADMIN", "--display-name", "x"},
		"unknown flag":         {"create", "--login", f.login, "--display-name", "x", "--enable"},
		"positional":           {"create", "--login", f.login, "--display-name", "x", "extra"},
	} {
		if err := f.run("", args...); !errors.Is(err, errUsage) {
			t.Errorf("%s: err = %v, want usage error", name, err)
		}
	}
	if err := f.run("", "bogus"); !errors.Is(err, errUsage) {
		t.Errorf("unknown command: %v", err)
	}
	if err := f.create("short\n", "--password-stdin"); !errors.Is(err, authentication.ErrPasswordTooShort) {
		t.Errorf("short password: %v", err)
	}
	if err := f.create("", "--password-stdin"); err == nil || !strings.Contains(err.Error(), "no password") {
		t.Errorf("empty stdin: %v", err)
	}
	if err := f.run("", "create", "--login", f.login, "--display-name", "bad\x00name"); !errors.Is(err, errUsage) {
		t.Errorf("display name with control character: %v", err)
	}
	if _, ok := f.credential(); ok {
		t.Fatal("invalid commands created a credential")
	}
	var users int
	_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM organization.users WHERE display_name = $1`, f.name).Scan(&users)
	if users != 0 {
		t.Fatalf("%d users left behind", users)
	}
}

func TestEmergencyCreateDuplicateLeavesNoUser(t *testing.T) {
	f := newEmFixture(t)
	if err := f.create(emPassword+"\n", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	err := f.create(emPassword+"\n", "--password-stdin")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v", err)
	}
	var users int
	_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM organization.users WHERE display_name = $1`, f.name).Scan(&users)
	if users != 1 {
		t.Fatalf("%d users, want exactly 1 (the failed create must roll back its user)", users)
	}
}

func TestEmergencySetPassword(t *testing.T) {
	f := newEmFixture(t)
	if err := f.create(emPassword+"\n", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	before, _ := f.credential()
	// An open session of the account ends with the password change.
	ctx := context.Background()
	svc := authentication.NewService(f.pool, authentication.Config{IdleTimeout: time.Hour, AbsoluteTimeout: time.Hour}, nil)
	token, _, err := svc.Create(ctx, before.UserID, "emergency", "t")
	if err != nil {
		t.Fatal(err)
	}

	const newPw = "a-different-long-passphrase"
	if err := f.run(newPw+"\n", "set-password", "--login", f.login, "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	after, _ := f.credential()
	if after.PasswordHash == before.PasswordHash {
		t.Fatal("hash unchanged")
	}
	if ok, _ := authentication.VerifyPasswordHash(ctx, after.PasswordHash, newPw); !ok {
		t.Fatal("new password does not verify")
	}
	if ok, _ := authentication.VerifyPasswordHash(ctx, after.PasswordHash, emPassword); ok {
		t.Fatal("old password still verifies")
	}
	if after.Enabled != before.Enabled {
		t.Fatal("set-password must not change the enabled state")
	}
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, authentication.ErrInvalidSession) {
		t.Fatalf("session survived a password change: %v", err)
	}
	if got := f.audit(before.UserID); !containsAction(got, "authentication.emergency_account.password_changed") {
		t.Fatalf("audit = %v", got)
	}
	if strings.Contains(f.stdout.String(), newPw) {
		t.Fatal("password echoed")
	}

	// Generated password variant and failures.
	if err := f.run("", "set-password", "--login", f.login); err != nil || !strings.Contains(f.stdout.String(), "Generated password") {
		t.Fatalf("generate: %v %s", err, f.stdout)
	}
	if err := f.run("short\n", "set-password", "--login", f.login, "--password-stdin"); !errors.Is(err, authentication.ErrPasswordTooShort) {
		t.Fatalf("short: %v", err)
	}
	if err := f.run(emPassword+"\n", "set-password", "--login", "zt-missing"+f.login, "--password-stdin"); err == nil || !strings.Contains(err.Error(), "no emergency account") {
		t.Fatalf("unknown login: %v", err)
	}
}

func TestEmergencyEnableDisable(t *testing.T) {
	f := newEmFixture(t)
	ctx := context.Background()
	if err := f.create("", "--password-stdin"); err == nil {
		t.Fatal("expected failure for an empty stdin")
	}
	if err := f.create(emPassword+"\n", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	cred, _ := f.credential()

	if err := f.run("", "enable", "--login", f.login); err != nil {
		t.Fatal(err)
	}
	if c, _ := f.credential(); !c.Enabled {
		t.Fatal("not enabled")
	}
	if err := f.run("", "enable", "--login", f.login); err != nil || !strings.Contains(f.stdout.String(), "already enabled") {
		t.Fatalf("second enable: %v %s", err, f.stdout)
	}

	svc := authentication.NewService(f.pool, authentication.Config{IdleTimeout: time.Hour, AbsoluteTimeout: time.Hour}, nil)
	token1, _, _ := svc.Create(ctx, cred.UserID, "emergency", "t")
	token2, _, _ := svc.Create(ctx, cred.UserID, "emergency", "t")
	if err := f.run("", "disable", "--login", f.login); err != nil {
		t.Fatal(err)
	}
	if c, _ := f.credential(); c.Enabled {
		t.Fatal("not disabled")
	}
	if !strings.Contains(f.stdout.String(), "2 session(s) revoked") {
		t.Fatalf("output = %s", f.stdout)
	}
	for _, tok := range []string{token1, token2} {
		if _, err := svc.Authenticate(ctx, tok); !errors.Is(err, authentication.ErrInvalidSession) {
			t.Fatalf("session survived disable: %v", err)
		}
	}
	got := f.audit(cred.UserID)
	if !containsAction(got, "authentication.emergency_account.enabled") || !containsAction(got, "authentication.emergency_account.disabled") {
		t.Fatalf("audit = %v", got)
	}
	// A revoked session is audited with the CLI system marker.
	var n int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE action = 'auth.session.revoked' AND metadata->>'actor' = 'cli' AND metadata->>'userId' = $1`, cred.UserID).Scan(&n)
	if n != 2 {
		t.Fatalf("session revocation audits = %d, want 2", n)
	}
	// Re-enabling is audited again; the unchanged state is not.
	if err := f.run("", "disable", "--login", f.login); err != nil || !strings.Contains(f.stdout.String(), "already disabled") {
		t.Fatalf("second disable: %v %s", err, f.stdout)
	}
	disabledAudits := 0
	for _, a := range f.audit(cred.UserID) {
		if strings.HasPrefix(a, "authentication.emergency_account.disabled") {
			disabledAudits++
		}
	}
	if disabledAudits != 1 {
		t.Fatalf("disabled audited %d times", disabledAudits)
	}
	for _, cmd := range []string{"enable", "disable"} {
		if err := f.run("", cmd, "--login", "zt-missing"+f.login); err == nil || !strings.Contains(err.Error(), "no emergency account") {
			t.Fatalf("%s unknown: %v", cmd, err)
		}
		if err := f.run("", cmd); !errors.Is(err, errUsage) {
			t.Fatalf("%s without login: %v", cmd, err)
		}
	}
}

func TestPasswordFromStdinIsBounded(t *testing.T) {
	long := strings.Repeat("a", 5000) + "\n"
	pw, _, err := passwordFor(env{stdin: strings.NewReader(long)}, true)
	if err != nil || len(pw) > authentication.MaxPasswordLength+3 {
		t.Fatalf("len=%d err=%v", len(pw), err)
	}
	if _, err := authentication.HashPassword(context.Background(), pw); !errors.Is(err, authentication.ErrPasswordTooLong) {
		t.Fatalf("an oversized password must be refused, got %v", err)
	}
	_, _, err = passwordFor(env{stdin: io.NopCloser(strings.NewReader(""))}, true)
	if err == nil {
		t.Fatal("empty stdin accepted")
	}
}

func containsAction(rows []string, action string) bool {
	for _, r := range rows {
		if strings.HasPrefix(r, action+" ") {
			return true
		}
	}
	return false
}
