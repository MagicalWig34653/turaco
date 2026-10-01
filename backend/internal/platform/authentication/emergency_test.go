package authentication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

const emergencyPassword = "break-glass-passphrase-2026"

var testActor = audit.CLIActor("tester")

// fixedUserCreator stands in for organization.LoginAccounts: the user of the
// emergency account is a fixture user id (platform has no FK to users).
type fixedUserCreator struct{ id string }

func (c fixedUserCreator) CreateEmergencyUser(context.Context, pgx.Tx, string, string, audit.Actor) (string, error) {
	return c.id, nil
}

func (f *loginFixture) emergencyAccounts(userID string) *EmergencyAccounts {
	return NewEmergencyAccounts(f.pool, fixedUserCreator{userID}, nil)
}

// newEmergencyAccount stores a credential for the fixture's user.
func (f *loginFixture) newEmergencyAccount(enabled bool) string {
	f.t.Helper()
	ctx := context.Background()
	name := "zt" + randHex(6)
	accounts := f.emergencyAccounts(f.userID)
	if _, err := accounts.CreateAccount(ctx, testActor, name, "Break Glass", emergencyPassword); err != nil {
		f.t.Fatal(err)
	}
	if enabled {
		if _, err := accounts.Enable(ctx, testActor, name); err != nil {
			f.t.Fatal(err)
		}
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
	if _, err := f.emergencyAccounts(otherUser).CreateAccount(context.Background(), testActor, disabled, "Other", emergencyPassword); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM platform.local_credentials WHERE user_id = $1`, otherUser)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM platform.audit_events WHERE target_id = $1::text`, otherUser)
	})
	unknown := "zt" + randHex(6)
	dummy := "zt" + randHex(6)

	cases := []struct{ name, login, password, reason, target string }{
		{"wrong password", enabled, "wrong-wrong-wrong-wrong", "wrong_password", f.userID},
		{"unknown login", unknown, emergencyPassword, "unknown_account", "unknown"},
		{"unknown login, dummy password", dummy, "turaco-dummy-password", "unknown_account", "unknown"},
		{"disabled account, right password", disabled, emergencyPassword, "disabled", otherUser},
	}
	var bodies []string
	for _, c := range cases {
		rec := f.emergency(c.login, c.password)
		if rec.Code != http.StatusUnauthorized || rec.errCode() != "auth.invalid_credentials" || len(rec.Result().Cookies()) != 0 {
			t.Fatalf("%s: status=%d code=%s", c.name, rec.Code, rec.errCode())
		}
		bodies = append(bodies, rec.uniformBody())
		rows := f.auditRows(`action = 'auth.emergency_login.failed' AND target_id = $1 AND metadata->>'reason' = $2 AND metadata->>'clientIp' = $3`, c.target, c.reason, f.ip)
		if len(rows) == 0 || !strings.Contains(rows[0], `"method": "emergency"`) {
			t.Fatalf("%s: audit = %v", c.name, rows)
		}
		if strings.Contains(strings.Join(rows, ""), c.password) || strings.Contains(strings.Join(rows, ""), c.login) {
			t.Fatalf("%s: password or raw login in audit", c.name)
		}
	}
	if all := f.auditRows(`action = 'auth.emergency_login.failed' AND metadata->>'clientIp' = $1`, f.ip); len(all) != len(cases) {
		t.Fatalf("failure audits = %d, want %d", len(all), len(cases))
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
	rows := f.auditRows(`action = 'auth.emergency_login.failed' AND target_id = $1`, f.userID)
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

// Strangers must not be able to lock the break-glass account: only the
// client budget applies, reserved before any hashing.
func TestEmergencyLoginHasNoAccountLockButAClientBudget(t *testing.T) {
	f := newLoginFixture(t, withEmergency(), withClientLimit(4))
	name := f.newEmergencyAccount(true)
	for i := 0; i < 3; i++ {
		if rec := f.emergency(name, "wrong-wrong-wrong-wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i+1, rec.Code)
		}
	}
	// Wrong attempts from other clients do not affect this client or the account.
	for i := 0; i < 8; i++ {
		ip := fmt.Sprintf("203.0.113.%d", i+1)
		f.trackIP(ip)
		rec := f.emergency(name, "wrong-wrong-wrong-wrong", func(r *http.Request) { r.RemoteAddr = ip + ":1" })
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("other client %s: %d", ip, rec.Code)
		}
	}
	if n := f.throttleFailures("acct:" + f.userID); n != 0 {
		t.Fatal("the emergency account must have no per-account counter")
	}
	rec := f.emergency(name, emergencyPassword)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("correct password after wrong ones: %d", rec.Code)
	}
	// The success was refunded: 3 failures only count.
	if n := f.throttleFailures(ClientKey(f.ip)); n != 3 {
		t.Fatalf("client attempts = %d, want 3", n)
	}
	// Now exhaust this client's budget (limit 4): the 4th attempt is allowed, the 5th is refused.
	if rec := f.emergency(name, "wrong-wrong-wrong-wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("4th attempt: %d", rec.Code)
	}
	sleeps := len(f.sleepsSnapshot())
	rec = f.emergency(name, emergencyPassword)
	if rec.Code != http.StatusTooManyRequests || rec.errCode() != "auth.too_many_attempts" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("status=%d code=%s retry=%q", rec.Code, rec.errCode(), rec.Header().Get("Retry-After"))
	}
	if len(f.sleepsSnapshot()) != sleeps {
		t.Fatal("a throttled request is not a credential failure")
	}
}

// holdHashSlots occupies every argon2 slot until the returned release is called.
func holdHashSlots(t *testing.T) (release func()) {
	t.Helper()
	n := cap(hashSlots)
	for i := 0; i < n; i++ {
		hashSlots <- struct{}{}
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			for i := 0; i < n; i++ {
				<-hashSlots
			}
		})
	}
	t.Cleanup(release)
	return release
}

func TestEmergencyLoginHashSlotTimeoutAnswers429WithoutHashing(t *testing.T) {
	f := newLoginFixture(t, withEmergency(), withHashWait(150*time.Millisecond))
	name := f.newEmergencyAccount(true)
	release := holdHashSlots(t)
	defer release()
	started := time.Now()
	rec := f.emergency(name, emergencyPassword)
	if d := time.Since(started); d < 100*time.Millisecond || d > 5*time.Second {
		t.Fatalf("answered after %v, want about the hash wait", d)
	}
	if rec.Code != http.StatusTooManyRequests || rec.errCode() != "auth.too_many_attempts" || rec.Header().Get("Retry-After") != "5" {
		t.Fatalf("status=%d code=%s retry=%q", rec.Code, rec.errCode(), rec.Header().Get("Retry-After"))
	}
	if f.sessionCount() != 0 || len(f.failureAudits()) != 0 {
		t.Fatal("a busy answer must create no session and no failure audit")
	}
	release()
	// Capacity back: the same login now works.
	if rec := f.emergency(name, emergencyPassword); rec.Code != http.StatusNoContent {
		t.Fatalf("after release: %d", rec.Code)
	}
}

func TestVerifyPasswordHashWithinBusy(t *testing.T) {
	hash, err := HashPassword(context.Background(), emergencyPassword)
	if err != nil {
		t.Fatal(err)
	}
	release := holdHashSlots(t)
	defer release()
	if _, err := VerifyPasswordHashWithin(context.Background(), hash, emergencyPassword, 50*time.Millisecond); !errors.Is(err, ErrHashBusy) {
		t.Fatalf("err = %v, want ErrHashBusy", err)
	}
	release()
	if ok, err := VerifyPasswordHashWithin(context.Background(), hash, emergencyPassword, time.Second); err != nil || !ok {
		t.Fatalf("verify after release: %v %v", ok, err)
	}
}

// Regression: a disable that commits between the credential lookup and the
// session creation must stop the login (no session, uniform 401).
func TestEmergencyLoginRacingDisableCreatesNoSession(t *testing.T) {
	f := newLoginFixture(t, withEmergency(), withHashWait(30*time.Second))
	name := f.newEmergencyAccount(true)
	release := holdHashSlots(t)
	defer release()

	done := make(chan loginResponse, 1)
	go func() { done <- f.emergency(name, emergencyPassword) }()
	// The login has reserved its client attempt and read the (still
	// enabled) credential, and now waits for a hash slot.
	deadline := time.Now().Add(5 * time.Second)
	for f.throttleFailures(ClientKey(f.ip)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("login did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)

	changed, _, err := f.emergencyAccounts(f.userID).Disable(context.Background(), testActor, name)
	if err != nil || !changed {
		t.Fatalf("disable: changed=%v err=%v", changed, err)
	}
	release()

	rec := <-done
	if rec.Code != http.StatusUnauthorized || rec.errCode() != "auth.invalid_credentials" || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("status=%d code=%s", rec.Code, rec.errCode())
	}
	if f.sessionCount() != 0 {
		t.Fatal("a session exists although the account was disabled before it was created")
	}
	var lastUsed *time.Time
	_ = f.pool.QueryRow(context.Background(), `SELECT last_used_at FROM platform.local_credentials WHERE login_name = $1`, name).Scan(&lastUsed)
	if lastUsed != nil {
		t.Fatal("last_used_at set by a refused login")
	}
	if rows := f.auditRows(`action = 'auth.emergency_login.succeeded' AND target_id = $1`, f.userID); len(rows) != 0 {
		t.Fatalf("success audited: %v", rows)
	}
	if rows := f.auditRows(`action = 'auth.emergency_login.failed' AND target_id = $1 AND metadata->>'reason' = 'credential_changed'`, f.userID); len(rows) != 1 {
		t.Fatalf("failure audit = %v", rows)
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

func TestEmergencyAccountsLifecycle(t *testing.T) {
	f := newLoginFixture(t)
	ctx := context.Background()
	name := "zt" + randHex(6)
	accounts := f.emergencyAccounts(f.userID)

	if _, err := accounts.CreateAccount(ctx, testActor, "Bad Name", "x", emergencyPassword); !errors.Is(err, ErrInvalidLoginName) {
		t.Fatalf("invalid login name: %v", err)
	}
	if _, err := accounts.CreateAccount(ctx, testActor, name, "x", "short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("short password: %v", err)
	}
	if _, err := accounts.CreateAccount(ctx, audit.Actor{}, name, "x", emergencyPassword); err == nil {
		t.Fatal("an actor is required")
	}
	if userID, err := accounts.CreateAccount(ctx, testActor, name, "x", emergencyPassword); err != nil || userID != f.userID {
		t.Fatalf("create: %q %v", userID, err)
	}
	if _, err := accounts.CreateAccount(ctx, testActor, name, "x", emergencyPassword); !errors.Is(err, ErrLocalCredentialExist) {
		t.Fatalf("duplicate: %v", err)
	}
	cred, ok, err := FindLocalCredential(ctx, f.pool, name)
	if err != nil || !ok || cred.Enabled || cred.UserID != f.userID {
		t.Fatalf("created credential = %+v ok=%v err=%v (must start disabled)", cred, ok, err)
	}
	oldHash := cred.PasswordHash

	// Sessions of the account end with a password change.
	_, sess1, err := f.svc.Create(ctx, f.userID, "emergency", "t")
	if err != nil {
		t.Fatal(err)
	}
	const newPassword = "another-long-passphrase-1"
	revoked, err := accounts.ChangePassword(ctx, testActor, name, newPassword)
	if err != nil || revoked != 1 {
		t.Fatalf("change password: revoked=%d err=%v", revoked, err)
	}
	if _, err := accounts.ChangePassword(ctx, testActor, "zt-missing", newPassword); !errors.Is(err, ErrLocalCredentialNone) {
		t.Fatalf("change password of unknown account: %v", err)
	}
	if _, err := accounts.ChangePassword(ctx, testActor, name, "short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("change to a short password: %v", err)
	}
	cred, _, _ = FindLocalCredential(ctx, f.pool, name)
	if cred.PasswordHash == oldHash {
		t.Fatal("hash unchanged")
	}
	if got := f.auditRows(`action = 'auth.session.revoked' AND target_id = $1`, sess1.ID); len(got) != 1 ||
		!strings.Contains(got[0], "emergency_password_changed") || !strings.Contains(got[0], `"actor": "cli"`) {
		t.Fatalf("session revocation audit = %v", got)
	}

	for i, want := range []bool{true, false} { // enabling twice audits once
		if changed, err := accounts.Enable(ctx, testActor, name); err != nil || changed != want {
			t.Fatalf("enable #%d: changed=%v err=%v", i, changed, err)
		}
	}
	_, sess2, _ := f.svc.Create(ctx, f.userID, "emergency", "t")
	changed, revoked, err := accounts.Disable(ctx, testActor, name)
	if err != nil || !changed || revoked != 1 {
		t.Fatalf("disable: changed=%v revoked=%d err=%v", changed, revoked, err)
	}
	if got := f.auditRows(`action = 'auth.session.revoked' AND target_id = $1`, sess2.ID); len(got) != 1 || !strings.Contains(got[0], "emergency_account_disabled") {
		t.Fatalf("disable revocation audit = %v", got)
	}
	// Disabling again changes nothing but still revokes.
	if changed, _, err := accounts.Disable(ctx, testActor, name); err != nil || changed {
		t.Fatalf("second disable: changed=%v err=%v", changed, err)
	}
	if _, err := accounts.Enable(ctx, testActor, "zt-missing"); !errors.Is(err, ErrLocalCredentialNone) {
		t.Fatalf("enable unknown: %v", err)
	}
	if _, _, err := accounts.Disable(ctx, testActor, "zt-missing"); !errors.Is(err, ErrLocalCredentialNone) {
		t.Fatalf("disable unknown: %v", err)
	}

	rows := f.auditRows(`target_type = 'emergency_account' AND target_id = $1::text`, f.userID)
	var actions []string
	for _, r := range rows {
		actions = append(actions, strings.Fields(r)[0])
		if strings.Contains(r, cred.PasswordHash) || strings.Contains(r, oldHash) || strings.Contains(r, emergencyPassword) || strings.Contains(r, newPassword) ||
			!strings.Contains(r, `"osUser": "tester"`) || !strings.Contains(r, `"actor": "cli"`) || !strings.Contains(r, `"loginName": "`+name+`"`) {
			t.Fatalf("audit row %s", r)
		}
	}
	want := []string{ActionEmergencyAccountCreated, ActionEmergencyAccountPasswordChanged, ActionEmergencyAccountEnabled, ActionEmergencyAccountDisabled}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("actions = %v, want %v", actions, want)
	}
	for i, w := range []string{"auth.emergency_account.created", "auth.emergency_account.password_changed", "auth.emergency_account.enabled", "auth.emergency_account.disabled"} {
		if want[i] != w {
			t.Fatalf("action %d = %q, want %q", i, want[i], w)
		}
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
