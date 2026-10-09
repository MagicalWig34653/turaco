package authentication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

const goodLocalPassword = "correct horse battery staple 42"

type fakeLocalDir struct {
	byEmail map[string]string
	info    map[string]LocalAccountInfo
}

func (d *fakeLocalDir) FindLocalAccount(_ context.Context, identifier string) (string, bool, error) {
	id, ok := d.byEmail[strings.ToLower(identifier)]
	return id, ok, nil
}

func (d *fakeLocalDir) LocalAccountInfo(_ context.Context, _ pgx.Tx, userID string) (LocalAccountInfo, error) {
	return d.info[userID], nil
}

// localFixture is a login fixture whose user is a local account with an issued invitation.
type localFixture struct {
	*loginFixture
	dir   *fakeLocalDir
	email string
	creds *LocalCredentials
}

func newLocalFixture(t *testing.T, enabled bool, opts ...fixtureOption) *localFixture {
	t.Helper()
	lf := &localFixture{dir: &fakeLocalDir{byEmail: map[string]string{}, info: map[string]LocalAccountInfo{}}, creds: NewLocalCredentials(nil)}
	opts = append(opts, func(f *loginFixture) {
		f.cfg.LocalEnabled = enabled
		f.localDir = lf.dir
	})
	lf.loginFixture = newLoginFixture(t, opts...)
	lf.email = "pat" + randHex(4) + "@example.test"
	lf.dir.byEmail[lf.email] = lf.userID
	lf.dir.info[lf.userID] = LocalAccountInfo{Exists: true, Local: true, Active: true, DisplayName: "Pat Example", Email: lf.email}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = lf.pool.Exec(ctx, `DELETE FROM platform.credential_tokens WHERE user_id = $1`, lf.userID)
		_, _ = lf.pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE target_id = $1`, lf.userID)
	})
	return lf
}

func (lf *localFixture) issue(purpose string) string {
	lf.t.Helper()
	var token string
	err := pgx.BeginFunc(context.Background(), lf.pool, func(tx pgx.Tx) error {
		if _, err := lf.creds.EnsureLocalCredential(context.Background(), tx, lf.userID); err != nil {
			return err
		}
		var err error
		token, _, err = lf.creds.IssueToken(context.Background(), tx, lf.userID, purpose, audit.CLIActor("test"), "corr-local-"+randHex(3))
		return err
	})
	if err != nil {
		lf.t.Fatal(err)
	}
	return token
}

func (lf *localFixture) redeem(token, password string) loginResponse {
	lf.t.Helper()
	body, _ := json.Marshal(map[string]string{"token": token, "password": password})
	return lf.post("/api/v1/auth/credential-tokens/redeem", string(body))
}

func (lf *localFixture) localLogin(identifier, password string) loginResponse {
	lf.t.Helper()
	body, _ := json.Marshal(map[string]string{"identifier": identifier, "password": password})
	return lf.post("/api/v1/auth/local-login", string(body))
}

func (lf *localFixture) activate() {
	lf.t.Helper()
	if rec := lf.redeem(lf.issue(PurposeInvitation), goodLocalPassword); rec.Code != http.StatusNoContent {
		lf.t.Fatalf("activate: %d %s", rec.Code, rec.Body)
	}
}

func TestLocalAccountsAreOffByDefault(t *testing.T) {
	lf := newLocalFixture(t, false)
	for _, path := range []string{"/api/v1/auth/local-login", "/api/v1/auth/credential-tokens/redeem"} {
		if rec := lf.post(path, `{"identifier":"a@b.c","password":"x","token":"y"}`); rec.Code != http.StatusNotFound || rec.errCode() != "auth.method_unavailable" {
			t.Errorf("%s while disabled: %d %s", path, rec.Code, rec.Body)
		}
	}
	rec := httptest.NewRecorder()
	lf.handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/auth/methods", nil))
	if !strings.Contains(rec.Body.String(), `"local":false`) {
		t.Errorf("methods: %s", rec.Body)
	}
}

func TestInvitationRedeemAndLocalLogin(t *testing.T) {
	lf := newLocalFixture(t, true, withEmergency())
	token := lf.issue(PurposeInvitation)

	// Nothing signs in before a password exists, and the answers are uniform.
	unknown := lf.localLogin("nobody"+randHex(3)+"@example.test", goodLocalPassword)
	before := lf.localLogin(lf.email, goodLocalPassword)
	if before.Code != 401 || unknown.Code != 401 || before.uniformBody() != unknown.uniformBody() {
		t.Fatalf("login before activation: %d %s / %d %s", before.Code, before.uniformBody(), unknown.Code, unknown.uniformBody())
	}

	// The policy rejects weak passwords without consuming the token.
	for _, weak := range []string{"short1!", "passwordpassword", "aaaaaaaaaaaaaaaa", "Pat Example 123", strings.Split(lf.email, "@")[0] + "xyz"} {
		if rec := lf.redeem(token, weak); rec.Code != 400 || rec.errCode() != "auth.password_policy" {
			t.Errorf("weak password %q: %d %s", weak, rec.Code, rec.Body)
		}
	}
	rec := lf.redeem(token, goodLocalPassword)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Referrer-Policy") != "no-referrer" || rec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("redeem: %d %s headers=%v (it must never log in)", rec.Code, rec.Body, rec.Header())
	}
	// Single use.
	if again := lf.redeem(token, goodLocalPassword+"x"); again.Code != 400 || again.errCode() != "auth.invalid_token" {
		t.Errorf("second redeem: %d %s", again.Code, again.Body)
	}
	if wrong := lf.localLogin(lf.email, "wrong password entirely"); wrong.Code != 401 {
		t.Errorf("wrong password: %d", wrong.Code)
	}
	ok := lf.localLogin(strings.ToUpper(lf.email), goodLocalPassword)
	if ok.Code != http.StatusNoContent || ok.cookieToken(false) == "" {
		t.Fatalf("login: %d %s", ok.Code, ok.Body)
	}
	var method string
	if err := lf.pool.QueryRow(context.Background(), `SELECT auth_method FROM platform.sessions WHERE user_id = $1 ORDER BY created_at DESC LIMIT 1`, lf.userID).Scan(&method); err != nil || method != "local-account" {
		t.Errorf("session method %q %v", method, err)
	}
	// Kind separation (R3): the credential of a local account does not authenticate at the emergency endpoint.
	var loginName string
	_ = lf.pool.QueryRow(context.Background(), `SELECT login_name FROM platform.local_credentials WHERE user_id = $1`, lf.userID).Scan(&loginName)
	em := lf.post("/api/v1/auth/emergency-login", `{"login":"`+loginName+`","password":"`+goodLocalPassword+`"}`)
	if em.Code != 401 {
		t.Errorf("a local credential at the emergency endpoint: %d %s", em.Code, em.Body)
	}
	// A reset needs an activated account and replaces the password; sessions end with it.
	reset := lf.issue(PurposeReset)
	if inv := lf.redeem(lf.issue(PurposeInvitation), goodLocalPassword+"2"); inv.Code != 400 {
		t.Errorf("an invitation for an activated account must not redeem: %d", inv.Code)
	}
	_ = reset
	reset = lf.issue(PurposeReset)
	if rec := lf.redeem(reset, "a completely new passphrase 9"); rec.Code != http.StatusNoContent {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body)
	}
	var live int
	_ = lf.pool.QueryRow(context.Background(), `SELECT count(*) FROM platform.sessions WHERE user_id = $1 AND revoked_at IS NULL`, lf.userID).Scan(&live)
	if live != 0 {
		t.Errorf("setting a password must revoke sessions, %d live", live)
	}
	if old := lf.localLogin(lf.email, goodLocalPassword); old.Code != 401 {
		t.Errorf("old password after reset: %d", old.Code)
	}
	if n := lf.localLogin(lf.email, "a completely new passphrase 9"); n.Code != http.StatusNoContent {
		t.Errorf("new password: %d", n.Code)
	}
}

func TestTokenRulesAreUniformAndSingleUse(t *testing.T) {
	lf := newLocalFixture(t, true)
	body := func(rec loginResponse) string { return rec.uniformBody() }
	garbage := lf.redeem(strings.Repeat("A", tokenLength), goodLocalPassword)
	if garbage.Code != 400 || garbage.errCode() != "auth.invalid_token" {
		t.Fatalf("unknown token: %d %s", garbage.Code, garbage.Body)
	}
	if bad := lf.redeem("short", goodLocalPassword); body(bad) != body(garbage) {
		t.Errorf("malformed token is distinguishable: %s", bad.Body)
	}

	// A newer token of the same purpose invalidates the older one.
	old := lf.issue(PurposeInvitation)
	newer := lf.issue(PurposeInvitation)
	if rec := lf.redeem(old, goodLocalPassword); body(rec) != body(garbage) {
		t.Errorf("superseded token: %d %s", rec.Code, rec.Body)
	}
	// Expired.
	if _, err := lf.pool.Exec(context.Background(), `UPDATE platform.credential_tokens SET created_at = now() - interval '9 days', expires_at = now() - interval '1 day' WHERE token_hash = $1`, HashToken(newer)); err != nil {
		t.Fatal(err)
	}
	if rec := lf.redeem(newer, goodLocalPassword); body(rec) != body(garbage) {
		t.Errorf("expired token: %d %s", rec.Code, rec.Body)
	}
	// A reset for a never-activated account is not redeemable (use the invitation).
	if rec := lf.redeem(lf.issue(PurposeReset), goodLocalPassword); body(rec) != body(garbage) {
		t.Errorf("reset before activation: %d %s", rec.Code, rec.Body)
	}
	// A deactivated, directory-linked or vanished account: the token is dead.
	tok := lf.issue(PurposeInvitation)
	lf.dir.info[lf.userID] = LocalAccountInfo{Exists: true, Local: true, Active: false}
	if rec := lf.redeem(tok, goodLocalPassword); body(rec) != body(garbage) {
		t.Errorf("inactive account: %d %s", rec.Code, rec.Body)
	}
	lf.dir.info[lf.userID] = LocalAccountInfo{Exists: true, Local: false, Active: true}
	if rec := lf.redeem(tok, goodLocalPassword); body(rec) != body(garbage) {
		t.Errorf("directory-linked account: %d %s", rec.Code, rec.Body)
	}
	// Only the hash is stored.
	var n int
	_ = lf.pool.QueryRow(context.Background(), `SELECT count(*) FROM platform.credential_tokens WHERE token_hash = $1`, []byte(tok)).Scan(&n)
	if n != 0 {
		t.Error("the raw token is stored")
	}
	// The token endpoint is protected against cross-site requests like every unsafe auth endpoint.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/auth/credential-tokens/redeem", strings.NewReader(`{"token":"x","password":"y"}`))
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	lf.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site redeem: %d", rec.Code)
	}
}

func TestLocalLoginLocksAfterRepeatedFailures(t *testing.T) {
	lf := newLocalFixture(t, true)
	lf.activate()
	for i := 0; i < maxLocalFailures; i++ {
		if rec := lf.localLogin(lf.email, "wrong password number "+randHex(2)); rec.Code != 401 {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
		// The throttle is separate from the account lock: keep this test about the lock.
		_, _ = lf.pool.Exec(context.Background(), `DELETE FROM platform.auth_throttle WHERE key = ANY($1)`, lf.keys)
	}
	if rec := lf.localLogin(lf.email, goodLocalPassword); rec.Code != 401 {
		t.Fatalf("the correct password while locked: %d", rec.Code)
	}
	var locked bool
	_ = lf.pool.QueryRow(context.Background(), `SELECT locked_until > now() FROM platform.local_credentials WHERE user_id = $1`, lf.userID).Scan(&locked)
	var audited int
	_ = lf.pool.QueryRow(context.Background(), `SELECT count(*) FROM platform.audit_events WHERE target_id = $1 AND action = $2`, lf.userID, ActionLocalLoginLocked).Scan(&audited)
	if !locked || audited != 1 {
		t.Errorf("locked=%v audited=%d", locked, audited)
	}
	// After the lock ends the account works again and the counter restarts.
	_, _ = lf.pool.Exec(context.Background(), `UPDATE platform.local_credentials SET locked_until = now() - interval '1 second' WHERE user_id = $1`, lf.userID)
	_, _ = lf.pool.Exec(context.Background(), `DELETE FROM platform.auth_throttle WHERE key = ANY($1)`, lf.keys)
	if rec := lf.localLogin(lf.email, goodLocalPassword); rec.Code != http.StatusNoContent {
		t.Errorf("after the lock: %d %s", rec.Code, rec.Body)
	}
}

func TestLocalSessionsStopWorkingWhenTheSwitchIsOff(t *testing.T) {
	sess := testSession()
	sess.AuthMethod = methodLocal
	sessions := &fakeSessions{sessions: map[string]Session{"tok": sess}}
	req := func() *http.Request { return cookieReq("GET", "/x", "tok") }
	on := NewSessionAuthenticator(sessions, fakeGate{active: true}, fakePerms{perms: map[string]struct{}{}}, false).WithLocalLogin(true)
	if _, ok, err := on.Authenticate(req()); err != nil || !ok {
		t.Errorf("enabled: ok=%v err=%v", ok, err)
	}
	off := NewSessionAuthenticator(sessions, fakeGate{active: true}, fakePerms{perms: map[string]struct{}{}}, false).WithLocalLogin(false)
	if _, ok, err := off.Authenticate(req()); err != nil || ok {
		t.Errorf("disabled: ok=%v err=%v (existing local sessions must be rejected)", ok, err)
	}
}

func TestLocalPasswordPolicy(t *testing.T) {
	for pw, ok := range map[string]bool{
		"correct horse battery staple": true,
		"Zebra-lamp-orbit-91":          true,
		"eleven char":                  false, // 11 characters
		"passwordpassword":             false,
		"P@ssw0rd1234":                 false,
		"abababababababab":             false,
		"qwertzuiop123":                false,
		"1234567890123456":             false,
		"Marlene-Musterfrau-1":         false, // contains the display name
	} {
		err := ValidateLocalPassword(pw, "Marlene Musterfrau", "marlene@example.test", "l-abc")
		if (err == nil) != ok {
			t.Errorf("ValidateLocalPassword(%q) = %v, want ok=%v", pw, err, ok)
		}
	}
	// The emergency policy is separate (16 characters).
	if ValidatePasswordPolicy("correct horse") == nil {
		t.Error("the emergency policy must keep its 16 character minimum")
	}
	if h, err := HashLocalPassword(context.Background(), goodLocalPassword); err != nil || !strings.HasPrefix(h, "$argon2id$") {
		t.Fatalf("hash: %q %v", h, err)
	} else if okv, err := VerifyLocalPasswordHash(context.Background(), h, goodLocalPassword, time.Second); err != nil || !okv {
		t.Errorf("verify: %v %v", okv, err)
	}
}
