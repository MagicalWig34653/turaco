package authentication

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const emergencyPassword = "break-glass-passphrase-2026"

var testActor = json.RawMessage(`{"actor":"cli","osUser":"tester"}`)

// newEmergencyAccount stores a credential for the fixture's user.
func (f *loginFixture) newEmergencyAccount(enabled bool) string {
	f.t.Helper()
	ctx := context.Background()
	name := "zt" + randHex(6)
	hash, err := HashPassword(ctx, emergencyPassword)
	if err != nil {
		f.t.Fatal(err)
	}
	f.trackKey(IdentifierKey(methodEmergency + ":" + name))
	err = pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error {
		if err := CreateLocalCredential(ctx, tx, f.userID, name, hash, LocalCredentialAudit{Actor: testActor, CorrelationID: "t", At: time.Now()}); err != nil {
			return err
		}
		if enabled {
			_, _, err := SetLocalEnabled(ctx, tx, name, true, LocalCredentialAudit{Actor: testActor, CorrelationID: "t", At: time.Now()})
			return err
		}
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return name
}

func (f *loginFixture) emergency(login, password string, mod ...func(*http.Request)) loginResponse {
	f.t.Helper()
	b, _ := json.Marshal(map[string]string{"login": login, "password": password})
	return f.post("/api/v1/auth/emergency-login", string(b), mod...)
}

func TestEmergencyLoginDisabledByDefault(t *testing.T) {
	f := newLoginFixture(t)
	name := f.newEmergencyAccount(true)
	rec := f.emergency(name, emergencyPassword)
	if rec.Code != http.StatusNotFound || rec.errCode() != "auth.method_unavailable" {
		t.Fatalf("status=%d code=%s", rec.Code, rec.errCode())
	}
	if f.sessionCount() != 0 || f.throttleFailures(ClientKey(f.ip)) != 0 {
		t.Fatal("disabled endpoint had side effects")
	}
}

func TestEmergencyLoginSuccess(t *testing.T) {
	f := newLoginFixture(t, withEmergency())
	name := f.newEmergencyAccount(true)
	rec := f.emergency(strings.ToUpper(name), emergencyPassword)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	token := rec.cookieToken(false)
	sess, err := f.svc.Authenticate(context.Background(), token)
	if err != nil || sess.UserID != f.userID || sess.AuthMethod != "emergency" {
		t.Fatalf("session = %+v, %v", sess, err)
	}
	// Absolute lifetime is capped at one hour although the service allows 8h.
	if got := sess.AbsoluteExpiresAt.Sub(sess.CreatedAt); got != time.Hour {
		t.Fatalf("absolute lifetime = %v, want 1h", got)
	}
	if c := rec.Result().Cookies()[0]; c.MaxAge > 3600 || c.MaxAge <= 0 {
		t.Fatalf("cookie max-age = %d", c.MaxAge)
	}

	var lastUsed *time.Time
	if err := f.pool.QueryRow(context.Background(), `SELECT last_used_at FROM platform.local_credentials WHERE login_name = $1`, name).Scan(&lastUsed); err != nil || lastUsed == nil {
		t.Fatalf("last_used_at = %v, %v", lastUsed, err)
	}
	rows := f.auditRows(`action = 'auth.emergency_login.succeeded' AND target_id = $1::text`, f.userID)
	if len(rows) != 1 || !strings.Contains(rows[0], f.userID+" ") || !strings.Contains(rows[0], `"loginName": "`+name+`"`) || !strings.Contains(rows[0], f.ip) {
		t.Fatalf("success audit = %v", rows)
	}
	if got := f.auditRows(`action = 'auth.session.created' AND target_id = $1`, sess.ID); len(got) != 1 || !strings.Contains(got[0], `"emergency"`) {
		t.Fatalf("session audit = %v", got)
	}
	// ERROR-level log without secrets.
	var found bool
	for _, line := range strings.Split(strings.TrimSpace(f.logs.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		if m["msg"] == "emergency account used" {
			found = true
			if m["level"] != "ERROR" || m["login"] != name {
				t.Fatalf("log = %v", m)
			}
		}
	}
	if !found {
		t.Fatalf("no 'emergency account used' log line in %s", f.logs)
	}
	if strings.Contains(f.logs.String(), emergencyPassword) {
		t.Fatal("password logged")
	}
}

func TestEmergencyLoginFailuresAreUniform(t *testing.T) {
	f := newLoginFixture(t, withEmergency())
	enabled := f.newEmergencyAccount(true)

	// A disabled account (a second user so credentials do not collide).
	otherUser := f.userID[:len(f.userID)-1] + "d"
	if otherUser == f.userID {
		otherUser = f.userID[:len(f.userID)-1] + "c"
	}
	disabled := "zt" + randHex(6)
	hash, _ := HashPassword(context.Background(), emergencyPassword)
	err := pgx.BeginFunc(context.Background(), f.pool, func(tx pgx.Tx) error {
		return CreateLocalCredential(context.Background(), tx, otherUser, disabled, hash, LocalCredentialAudit{Actor: testActor, At: time.Now()})
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM platform.local_credentials WHERE user_id = $1`, otherUser)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM platform.audit_events WHERE target_id = $1::text`, otherUser)
	})
	f.trackKey(IdentifierKey(methodEmergency + ":" + disabled))
	unknown := "zt" + randHex(6)
	f.trackKey(IdentifierKey(methodEmergency + ":" + unknown))
	dummy := "zt" + randHex(6)
	f.trackKey(IdentifierKey(methodEmergency + ":" + dummy))

	cases := []struct{ name, login, password, reason string }{
		{"wrong password", enabled, "wrong-wrong-wrong-wrong", "wrong_password"},
		{"unknown login", unknown, emergencyPassword, "unknown_account"},
		{"unknown login, dummy password", dummy, "turaco-dummy-password", "unknown_account"},
		{"disabled account, right password", disabled, emergencyPassword, "disabled"},
	}
	var bodies []string
	for _, c := range cases {
		rec := f.emergency(c.login, c.password)
		if rec.Code != http.StatusUnauthorized || rec.errCode() != "auth.invalid_credentials" || len(rec.Result().Cookies()) != 0 {
			t.Fatalf("%s: status=%d code=%s", c.name, rec.Code, rec.errCode())
		}
		bodies = append(bodies, rec.uniformBody())
		rows := f.auditRows(`action = 'auth.emergency_login.failed' AND target_id = $1`, IdentifierKey(methodEmergency+":"+c.login))
		if len(rows) != 1 || !strings.Contains(rows[0], `"reason": "`+c.reason+`"`) || !strings.Contains(rows[0], `"method": "emergency"`) {
			t.Fatalf("%s: audit = %v", c.name, rows)
		}
		if strings.Contains(strings.Join(rows, ""), c.password) {
			t.Fatalf("%s: password in audit", c.name)
		}
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] {
			t.Fatalf("bodies differ:\n%s\n%s", bodies[0], b)
		}
	}
	for _, d := range f.sleepsSnapshot() {
		if d != 400*time.Millisecond {
			t.Fatalf("sleep = %v", d)
		}
	}
	if len(f.sleepsSnapshot()) != len(cases) {
		t.Fatalf("sleeps = %v", f.sleepsSnapshot())
	}
	if f.sessionCount() != 0 {
		t.Fatal("session created by a failed emergency login")
	}
}

func TestEmergencyLoginInactiveUser(t *testing.T) {
	f := newLoginFixture(t, withEmergency())
	name := f.newEmergencyAccount(true)
	f.locker.active[f.userID] = false
	rec := f.emergency(name, emergencyPassword)
	if rec.Code != http.StatusUnauthorized || rec.errCode() != "auth.invalid_credentials" {
		t.Fatalf("status=%d code=%s", rec.Code, rec.errCode())
	}
	rows := f.auditRows(`action = 'auth.emergency_login.failed' AND target_id = $1`, IdentifierKey(methodEmergency+":"+name))
	if len(rows) != 1 || !strings.Contains(rows[0], `"reason": "user_inactive"`) {
		t.Fatalf("audit = %v", rows)
	}
	if f.sessionCount() != 0 {
		t.Fatal("session for inactive user")
	}
	var lastUsed *time.Time
	_ = f.pool.QueryRow(context.Background(), `SELECT last_used_at FROM platform.local_credentials WHERE login_name = $1`, name).Scan(&lastUsed)
	if lastUsed != nil {
		t.Fatal("last_used_at updated by a refused login")
	}
	if strings.Contains(f.logs.String(), "emergency account used") {
		t.Fatal("refused login logged as use")
	}
}

func TestEmergencyLoginThrottle(t *testing.T) {
	f := newLoginFixture(t, withEmergency())
	name := f.newEmergencyAccount(true)
	for i := 0; i < 5; i++ {
		if rec := f.emergency(name, "wrong-wrong-wrong-wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i+1, rec.Code)
		}
	}
	rec := f.emergency(name, emergencyPassword)
	if rec.Code != http.StatusTooManyRequests || rec.errCode() != "auth.too_many_attempts" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("status=%d code=%s retry=%q", rec.Code, rec.errCode(), rec.Header().Get("Retry-After"))
	}
	if f.sessionCount() != 0 {
		t.Fatal("session created while locked")
	}
	// A directory identifier with the same name has its own counter.
	if rec := f.login(name, "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("directory login shares the emergency lock: %d", rec.Code)
	}
}

func TestEmergencyLoginRequestValidation(t *testing.T) {
	f := newLoginFixture(t, withEmergency())
	name := f.newEmergencyAccount(true)
	for _, body := range []string{
		``, `{}`, `{"login":"","password":"x"}`, `{"login":"` + name + `","password":""}`,
		`{"login":"` + name + `","password":"x","extra":1}`, `{"login":"` + strings.Repeat("a", 65) + `","password":"x"}`,
		`{"login":"` + name + `","password":"x"} x`,
	} {
		if rec := f.post("/api/v1/auth/emergency-login", body); rec.Code != http.StatusBadRequest || rec.errCode() != "auth.invalid_request" {
			t.Errorf("body %.40q: status=%d code=%s", body, rec.Code, rec.errCode())
		}
	}
	if rec := f.post("/api/v1/auth/emergency-login", `{"login":"a","password":"b"}`, func(r *http.Request) { r.Header.Del("Sec-Fetch-Site") }); rec.Code != http.StatusForbidden {
		t.Fatalf("csrf: %d", rec.Code)
	}
}

func TestLocalCredentialLifecycle(t *testing.T) {
	f := newLoginFixture(t)
	ctx := context.Background()
	name := "zt" + randHex(6)
	at := LocalCredentialAudit{Actor: testActor, CorrelationID: "corr", At: time.Now()}
	hash, _ := HashPassword(ctx, emergencyPassword)
	run := func(fn func(tx pgx.Tx) error) error { return pgx.BeginFunc(ctx, f.pool, fn) }

	if err := run(func(tx pgx.Tx) error { return CreateLocalCredential(ctx, tx, f.userID, "Bad Name", hash, at) }); !errors.Is(err, ErrInvalidLoginName) {
		t.Fatalf("invalid login name: %v", err)
	}
	if err := run(func(tx pgx.Tx) error { return CreateLocalCredential(ctx, tx, f.userID, name, hash, at) }); err != nil {
		t.Fatal(err)
	}
	if err := run(func(tx pgx.Tx) error { return CreateLocalCredential(ctx, tx, f.userID, name, hash, at) }); !errors.Is(err, ErrLocalCredentialExist) {
		t.Fatalf("duplicate: %v", err)
	}
	cred, ok, err := FindLocalCredential(ctx, f.pool, name)
	if err != nil || !ok || cred.Enabled || cred.UserID != f.userID || cred.PasswordHash != hash {
		t.Fatalf("created credential = %+v ok=%v err=%v (must start disabled)", cred, ok, err)
	}

	newHash, _ := HashPassword(ctx, "another-long-passphrase-1")
	var userID string
	if err := run(func(tx pgx.Tx) (err error) { userID, err = SetLocalPassword(ctx, tx, name, newHash, at); return }); err != nil || userID != f.userID {
		t.Fatalf("set password: %q %v", userID, err)
	}
	if err := run(func(tx pgx.Tx) error { _, err := SetLocalPassword(ctx, tx, "zt-missing", newHash, at); return err }); !errors.Is(err, ErrLocalCredentialNone) {
		t.Fatalf("set password of unknown account: %v", err)
	}
	var changed bool
	for i, want := range []bool{true, false} { // enabling twice audits once
		if err := run(func(tx pgx.Tx) (err error) { _, changed, err = SetLocalEnabled(ctx, tx, name, true, at); return }); err != nil || changed != want {
			t.Fatalf("enable #%d: changed=%v err=%v", i, changed, err)
		}
	}
	if err := run(func(tx pgx.Tx) (err error) { _, changed, err = SetLocalEnabled(ctx, tx, name, false, at); return }); err != nil || !changed {
		t.Fatalf("disable: changed=%v err=%v", changed, err)
	}
	if err := run(func(tx pgx.Tx) error { _, _, err := SetLocalEnabled(ctx, tx, "zt-missing", true, at); return err }); !errors.Is(err, ErrLocalCredentialNone) {
		t.Fatalf("enable unknown: %v", err)
	}

	rows := f.auditRows(`target_type = 'emergency_account' AND target_id = $1::text`, f.userID)
	var actions []string
	for _, r := range rows {
		actions = append(actions, strings.Fields(r)[0])
		if strings.Contains(r, hash) || strings.Contains(r, newHash) || strings.Contains(r, emergencyPassword) || !strings.Contains(r, `"osUser": "tester"`) || !strings.Contains(r, `"loginName": "`+name+`"`) {
			t.Fatalf("audit row %s", r)
		}
	}
	want := []string{ActionEmergencyAccountCreated, ActionEmergencyAccountPasswordChanged, ActionEmergencyAccountEnabled, ActionEmergencyAccountDisabled}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("actions = %v, want %v", actions, want)
	}
	// A rolled-back transaction leaves neither credential nor audit.
	name2 := "zt" + randHex(6)
	otherUser := f.userID[:len(f.userID)-1] + "b"
	if otherUser == f.userID {
		otherUser = f.userID[:len(f.userID)-1] + "a"
	}
	tx, _ := f.pool.Begin(ctx)
	if err := CreateLocalCredential(ctx, tx, otherUser, name2, hash, at); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback(ctx)
	if _, ok, _ := FindLocalCredential(ctx, f.pool, name2); ok {
		t.Fatal("rolled back credential persisted")
	}
}

func TestLocalCredentialStoresOnlyPHCHash(t *testing.T) {
	f := newLoginFixture(t)
	name := f.newEmergencyAccount(false)
	cred, _, _ := FindLocalCredential(context.Background(), f.pool, name)
	if !strings.HasPrefix(cred.PasswordHash, "$argon2id$") || strings.Contains(cred.PasswordHash, emergencyPassword) {
		t.Fatalf("stored hash %q", cred.PasswordHash)
	}
}
