package authentication

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeKerberos is a KerberosValidator: it knows raw tokens and the principals
// they authenticate; everything else is an invalid ticket.
type fakeKerberos struct {
	mu      sync.Mutex
	tickets map[string]KerberosPrincipal
	calls   int
	last    []byte
	err     error // returned for every call when set
}

func (k *fakeKerberos) Validate(_ context.Context, token []byte) (KerberosPrincipal, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.calls++
	k.last = append([]byte(nil), token...)
	if k.err != nil {
		return KerberosPrincipal{}, k.err
	}
	if p, ok := k.tickets[string(token)]; ok {
		return p, nil
	}
	return KerberosPrincipal{}, fmt.Errorf("%w: fake reason", ErrInvalidTicket)
}

func (k *fakeKerberos) callCount() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.calls
}

const krbRealm = "EXAMPLE.TEST"

// newKerberosFixture registers one valid ticket for the fixture's account.
func newKerberosFixture(t *testing.T, opts ...fixtureOption) (*loginFixture, *fakeKerberos) {
	t.Helper()
	k := &fakeKerberos{}
	f := newLoginFixture(t, append([]fixtureOption{withKerberos(k)}, opts...)...)
	f.trackKey(kerberosClientKey(f.ip))
	k.tickets = map[string]KerberosPrincipal{
		"ticket-alice": {Username: f.ident, Realm: krbRealm},
	}
	return f, k
}

func negotiate(raw string) string {
	return "Negotiate " + base64.StdEncoding.EncodeToString([]byte(raw))
}

func (f *loginFixture) kerberosGet(authorization string, mod ...func(*http.Request)) loginResponse {
	f.t.Helper()
	r := httptest.NewRequest("GET", "/api/v1/auth/kerberos", nil)
	r.RemoteAddr = f.ip + ":4711"
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	for _, m := range mod {
		m(r)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, r)
	return loginResponse{rec}
}

func TestKerberosNotConfigured(t *testing.T) {
	// No validator.
	f := newLoginFixture(t)
	rec := f.kerberosGet(negotiate("ticket-alice"))
	if rec.Code != http.StatusNotFound || rec.errCode() != "auth.method_unavailable" || rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("status=%d code=%s www=%q", rec.Code, rec.errCode(), rec.Header().Get("WWW-Authenticate"))
	}
	if f.throttleFailures(kerberosClientKey(f.ip)) != 0 {
		t.Fatal("unconfigured Kerberos touched the throttle")
	}

	// A validator without the directory mapping cannot log anybody in.
	k := &fakeKerberos{tickets: map[string]KerberosPrincipal{"ticket-alice": {Username: f.ident}}}
	for name, mutate := range map[string]func(*LoginDeps, *LoginConfig){
		"no directory":    func(d *LoginDeps, _ *LoginConfig) { d.Directory = nil },
		"no provider key": func(_ *LoginDeps, c *LoginConfig) { c.ProviderKey = "" },
	} {
		deps, cfg := f.deps, f.cfg
		deps.Kerberos = k
		mutate(&deps, &cfg)
		mux := http.NewServeMux()
		RegisterLogin(mux, deps, cfg)
		for path, want := range map[string]int{"/api/v1/auth/kerberos": http.StatusNotFound, "/api/v1/auth/methods": http.StatusOK} {
			req := httptest.NewRequest("GET", path, nil)
			req.Header.Set("Authorization", negotiate("ticket-alice"))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != want || (path == "/api/v1/auth/methods" && !strings.Contains(rec.Body.String(), `"kerberos":false`)) {
				t.Fatalf("%s %s: status=%d body=%s", name, path, rec.Code, rec.Body)
			}
		}
	}
	if k.callCount() != 0 {
		t.Fatal("validator called although Kerberos is not usable")
	}
}

func TestKerberosMethodsReportsAvailability(t *testing.T) {
	f, _ := newKerberosFixture(t)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/auth/methods", nil))
	if body := strings.TrimSpace(rec.Body.String()); body != `{"emergency":false,"kerberos":true,"local":false,"password":true}` {
		t.Fatalf("methods = %s", body)
	}
	// Kerberos does not need the password verifier.
	f2, _ := newKerberosFixture(t, withoutPasswordLogin())
	rec = httptest.NewRecorder()
	f2.handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/auth/methods", nil))
	if body := strings.TrimSpace(rec.Body.String()); body != `{"emergency":false,"kerberos":true,"local":false,"password":true}` &&
		body != `{"emergency":false,"kerberos":true,"local":false,"password":false}` {
		t.Fatalf("methods = %s", body)
	}
}

func TestKerberosChallengeWithoutCredentials(t *testing.T) {
	f, k := newKerberosFixture(t)
	for name, header := range map[string]string{
		"no header":        "",
		"other scheme":     "Basic YWxpY2U6c2VjcmV0",
		"bearer":           "Bearer abc",
		"bare negotiate":   "Negotiate",
		"empty negotiate":  "Negotiate   ",
		"scheme only dots": "Negotiat",
	} {
		rec := f.kerberosGet(header)
		if rec.Code != http.StatusUnauthorized || rec.errCode() != "auth.invalid_credentials" || rec.Header().Get("WWW-Authenticate") != "Negotiate" {
			t.Fatalf("%s: status=%d code=%s www=%q", name, rec.Code, rec.errCode(), rec.Header().Get("WWW-Authenticate"))
		}
		if len(rec.Result().Cookies()) != 0 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: cookies=%v cache=%q", name, rec.Result().Cookies(), rec.Header().Get("Cache-Control"))
		}
	}
	// The challenge is the normal first round trip: no validation, no
	// throttle attempt, no audit, no delay.
	if k.callCount() != 0 || f.throttleFailures(kerberosClientKey(f.ip)) != 0 || len(f.sleepsSnapshot()) != 0 {
		t.Fatalf("challenge did work: validator=%d attempts=%d sleeps=%v", k.callCount(), f.throttleFailures(kerberosClientKey(f.ip)), f.sleepsSnapshot())
	}
	if rows := f.unknownFailureAudits(f.ip); len(rows) != 0 {
		t.Fatalf("challenge was audited: %v", rows)
	}
}

func TestKerberosSuccessCreatesSession(t *testing.T) {
	f, k := newKerberosFixture(t)
	// Earlier failures from this client stay counted; the success is refunded.
	f.kerberosGet(negotiate("bad-ticket"))
	rec := f.kerberosGet("negotiate " + base64.StdEncoding.EncodeToString([]byte("ticket-alice"))) // scheme is case-insensitive
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if string(k.last) != "ticket-alice" {
		t.Fatalf("validator got %q, want the decoded token", k.last)
	}
	token := rec.cookieToken(false)
	if token == "" {
		t.Fatal("no session cookie")
	}
	c := rec.Result().Cookies()[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Fatalf("cookie attributes: %+v", c)
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("headers: %v", rec.Header())
	}
	sess, err := f.svc.Authenticate(context.Background(), token)
	if err != nil || sess.UserID != f.userID || sess.AuthMethod != "kerberos" {
		t.Fatalf("session = %+v, %v", sess, err)
	}
	if got := f.auditRows(`action = 'auth.session.created' AND target_id = $1`, sess.ID); len(got) != 1 || !strings.Contains(got[0], `"authMethod": "kerberos"`) {
		t.Fatalf("session audit = %v", got)
	}
	if n := f.throttleFailures(kerberosClientKey(f.ip)); n != 1 {
		t.Fatalf("client attempts = %d, want 1 (the failure only)", n)
	}
	if len(f.sleepsSnapshot()) != 1 { // only the failure was delayed
		t.Fatalf("sleeps = %v: successful logins are not delayed", f.sleepsSnapshot())
	}
	// No account throttle key is used for Kerberos.
	if n := f.throttleFailures(AccountKey(f.userID)); n != 0 {
		t.Fatalf("account attempts = %d, want 0", n)
	}
	if f.dir.calls != 1 {
		t.Fatalf("directory lookups = %d, want 1", f.dir.calls)
	}
}

// The principal's user name is looked up as a plain login name in the
// configured provider (the fake directory is keyed by provider and
// case-folded name, like organization.LoginAccounts).
func TestKerberosMapsUsernameThroughTheConfiguredProvider(t *testing.T) {
	f, k := newKerberosFixture(t)
	k.tickets["ticket-upper"] = KerberosPrincipal{Username: strings.ToUpper(f.ident), Realm: krbRealm}
	if rec := f.kerberosGet(negotiate("ticket-upper")); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	// Handler configured for another provider: the account is not found.
	deps, cfg := f.deps, f.cfg
	cfg.ProviderKey = "other"
	mux := http.NewServeMux()
	RegisterLogin(mux, deps, cfg)
	req := httptest.NewRequest("GET", "/api/v1/auth/kerberos", nil)
	req.RemoteAddr = f.ip + ":1"
	req.Header.Set("Authorization", negotiate("ticket-alice"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("other provider: status = %d, want 401", rec.Code)
	}
}

func TestKerberosReplacesExistingSession(t *testing.T) {
	f, _ := newKerberosFixture(t)
	first := f.login(f.ident, loginPassword).cookieToken(false)
	if first == "" {
		t.Fatal("first login failed")
	}
	rec := f.kerberosGet(negotiate("ticket-alice"), cookieHeader(first))
	newToken := rec.cookieToken(false)
	if rec.Code != http.StatusNoContent || newToken == "" || newToken == first {
		t.Fatalf("status=%d token=%q", rec.Code, newToken)
	}
	if _, err := f.svc.Authenticate(context.Background(), first); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("old session still valid: %v", err)
	}
	if _, err := f.svc.Authenticate(context.Background(), newToken); err != nil {
		t.Fatalf("new session invalid: %v", err)
	}
	rows := f.auditRows(`action = 'auth.session.revoked' AND target_id IN (SELECT id::text FROM platform.sessions WHERE user_id = $1)`, f.userID)
	if len(rows) != 1 || !strings.Contains(rows[0], "replaced_by_login") || !strings.Contains(rows[0], `"actor": "login"`) {
		t.Fatalf("revocation audit = %v", rows)
	}
}

func TestKerberosFailuresAreUniformAndAudited(t *testing.T) {
	f, k := newKerberosFixture(t)
	suffix := strings.TrimPrefix(f.ident, "alice")
	inactiveID := f.userID[:len(f.userID)-1] + "f"
	if inactiveID == f.userID {
		inactiveID = f.userID[:len(f.userID)-1] + "e"
	}
	f.dir.accounts["ad|inactive"+suffix] = DirectoryAccount{UserID: inactiveID, DistinguishedName: loginDN + suffix, Username: "inactive" + suffix}
	f.trackKey(AccountKey(inactiveID))
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM platform.audit_events WHERE target_id = $1`, inactiveID)
	})
	k.tickets["ticket-nobody"] = KerberosPrincipal{Username: "nobody" + suffix, Realm: krbRealm}
	k.tickets["ticket-inactive"] = KerberosPrincipal{Username: "inactive" + suffix, Realm: krbRealm}

	cases := []struct {
		name          string
		header        string
		reason        string
		userID        string // "" = unresolved
		wantDirectory bool
	}{
		{"invalid ticket", negotiate("forged"), "invalid_ticket", "", false},
		{"not base64", "Negotiate !!!not-base64!!!", "invalid_ticket", "", false},
		{"unknown account", negotiate("ticket-nobody"), "unknown_account", "", true},
		{"inactive user", negotiate("ticket-inactive"), "user_inactive", inactiveID, true},
	}
	var bodies []string
	seen := map[string]int{} // audits expected per reason and target
	for _, c := range cases {
		seen[c.reason+"|"+c.userID]++
		beforeDir, beforeKrb := f.dir.calls, k.callCount()
		rec := f.kerberosGet(c.header)
		if rec.Code != http.StatusUnauthorized || rec.errCode() != "auth.invalid_credentials" || rec.Header().Get("WWW-Authenticate") != "Negotiate" {
			t.Fatalf("%s: status=%d code=%s www=%q", c.name, rec.Code, rec.errCode(), rec.Header().Get("WWW-Authenticate"))
		}
		if len(rec.Result().Cookies()) != 0 {
			t.Fatalf("%s: cookie set on failure", c.name)
		}
		if (f.dir.calls > beforeDir) != c.wantDirectory {
			t.Fatalf("%s: directory consulted = %v", c.name, f.dir.calls > beforeDir)
		}
		if c.name == "not base64" && k.callCount() != beforeKrb {
			t.Fatal("undecodable token reached the validator")
		}
		bodies = append(bodies, rec.uniformBody())
		var rows []string
		if c.userID != "" {
			rows = f.auditRows(`target_type = 'user' AND target_id = $1 AND action = 'auth.login.failed'`, c.userID)
		} else {
			rows = f.auditRows(`target_type = 'login' AND target_id = 'unknown' AND action = 'auth.login.failed' AND metadata->>'reason' = $1 AND metadata->>'clientIp' = $2`, c.reason, f.ip)
		}
		last := ""
		if len(rows) > 0 {
			last = rows[len(rows)-1]
		}
		if len(rows) != seen[c.reason+"|"+c.userID] || !strings.Contains(last, `"reason": "`+c.reason+`"`) || !strings.Contains(last, `"method": "kerberos"`) ||
			!strings.Contains(last, `"clientIp": "`+f.ip+`"`) || !strings.Contains(last, `"actor": "login"`) {
			t.Fatalf("%s: audit = %v", c.name, rows)
		}
		// Never the token, its decoded bytes or the principal name.
		for _, secret := range []string{strings.TrimPrefix(c.header, "Negotiate "), "forged", "ticket-", "nobody" + suffix, "inactive" + suffix} {
			if strings.Contains(last, secret) {
				t.Fatalf("%s: %q in audit: %s", c.name, secret, last)
			}
		}
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] {
			t.Fatalf("kerberos failure bodies differ:\n%s\n%s", bodies[0], b)
		}
	}
	// Same minimum response time as password failures.
	sleeps := f.sleepsSnapshot()
	if len(sleeps) != len(cases) {
		t.Fatalf("sleeps = %v", sleeps)
	}
	for _, d := range sleeps {
		if d != defaultMinFailureTime {
			t.Fatalf("failure delay = %v, want %v", d, defaultMinFailureTime)
		}
	}
	if f.sessionCount() != 0 {
		t.Fatal("sessions created by failed logins")
	}
	if n := f.throttleFailures(kerberosClientKey(f.ip)); n != len(cases) {
		t.Fatalf("client attempts = %d, want %d", n, len(cases))
	}
	// Failures never create an account budget.
	if f.throttleFailures(AccountKey(f.userID)) != 0 || f.throttleFailures(AccountKey(inactiveID)) != 0 {
		t.Fatal("kerberos failures created account throttle rows")
	}
}

func TestKerberosFailureKeepsExistingSession(t *testing.T) {
	f, _ := newKerberosFixture(t)
	first := f.login(f.ident, loginPassword).cookieToken(false)
	if rec := f.kerberosGet(negotiate("forged"), cookieHeader(first)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	if _, err := f.svc.Authenticate(context.Background(), first); err != nil {
		t.Fatalf("a failed login must not end the current session: %v", err)
	}
}

// A validator that returned a name the directory lookup would reinterpret
// (DOMAIN\user, user@host, instance) must not reach the directory.
func TestKerberosRefusesNamesTheDirectoryWouldReinterpret(t *testing.T) {
	f, k := newKerberosFixture(t)
	for i, name := range []string{`EXAMPLE\` + f.ident, f.ident + "@example.test", f.ident + "/admin", " " + f.ident, f.ident + " ", ""} {
		raw := fmt.Sprintf("ticket-odd-%d", i)
		k.tickets[raw] = KerberosPrincipal{Username: name, Realm: krbRealm}
		before := f.dir.calls
		rec := f.kerberosGet(negotiate(raw))
		if rec.Code != http.StatusUnauthorized || f.dir.calls != before {
			t.Fatalf("%q: status=%d directory consulted=%v", name, rec.Code, f.dir.calls != before)
		}
	}
	if f.sessionCount() != 0 {
		t.Fatal("session created for an unusable name")
	}
}

func TestKerberosInternalErrorsAreGeneric(t *testing.T) {
	f, k := newKerberosFixture(t)
	k.err = errors.New("keytab exploded: secret-detail")
	rec := f.kerberosGet(negotiate("ticket-alice"))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret-detail") || rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("validator failure: status=%d body=%s", rec.Code, rec.Body)
	}
	k.err = nil
	f.dir.err = errors.New("db down: secret-detail")
	rec = f.kerberosGet(negotiate("ticket-alice"))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret-detail") {
		t.Fatalf("directory failure: status=%d body=%s", rec.Code, rec.Body)
	}
	if f.sessionCount() != 0 || len(f.failureAudits()) != 0 {
		t.Fatal("internal errors must not create sessions or credential-failure audits")
	}
}

func TestKerberosSessionLockTimeoutAnswers503(t *testing.T) {
	f, _ := newKerberosFixture(t)
	f.locker.err = ErrTemporarilyUnavailable
	rec := f.kerberosGet(negotiate("ticket-alice"))
	if rec.Code != http.StatusServiceUnavailable || rec.errCode() != "auth.temporarily_unavailable" {
		t.Fatalf("status=%d code=%s", rec.Code, rec.errCode())
	}
	if f.sessionCount() != 0 {
		t.Fatal("session created despite the lock timeout")
	}
}

func TestKerberosHeaderBounds(t *testing.T) {
	f, k := newKerberosFixture(t)
	huge := "Negotiate " + strings.Repeat("A", maxNegotiateHeaderBytes)
	rec := f.kerberosGet(huge)
	if rec.Code != http.StatusBadRequest || rec.errCode() != "auth.invalid_request" {
		t.Fatalf("oversized: status=%d code=%s", rec.Code, rec.errCode())
	}
	// Two Authorization headers are ambiguous.
	rec = f.kerberosGet("", func(r *http.Request) {
		r.Header.Add("Authorization", negotiate("ticket-alice"))
		r.Header.Add("Authorization", negotiate("ticket-alice"))
	})
	if rec.Code != http.StatusBadRequest || rec.errCode() != "auth.invalid_request" {
		t.Fatalf("two headers: status=%d code=%s", rec.Code, rec.errCode())
	}
	if k.callCount() != 0 || f.throttleFailures(kerberosClientKey(f.ip)) != 0 || f.sessionCount() != 0 {
		t.Fatal("rejected headers must not reach the validator or the throttle")
	}
	// A header at the bound is processed (and fails as an invalid ticket).
	atLimit := "Negotiate " + strings.Repeat("A", maxNegotiateHeaderBytes-len("Negotiate ")-2) // valid base64 length
	if rec = f.kerberosGet(atLimit); rec.Code != http.StatusUnauthorized || k.callCount() != 1 {
		t.Fatalf("at limit: status=%d validator calls=%d", rec.Code, k.callCount())
	}
}

func TestKerberosRejectsCrossSiteRequests(t *testing.T) {
	f, k := newKerberosFixture(t)
	for _, site := range []string{"cross-site", "same-site"} {
		rec := f.kerberosGet(negotiate("ticket-alice"), func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", site) })
		if rec.Code != http.StatusForbidden || rec.errCode() != "platform.csrf_rejected" || len(rec.Result().Cookies()) != 0 {
			t.Fatalf("%s: status=%d code=%s", site, rec.Code, rec.errCode())
		}
	}
	if k.callCount() != 0 || f.throttleFailures(kerberosClientKey(f.ip)) != 0 {
		t.Fatal("cross-site request reached the validator or the throttle")
	}
	for _, site := range []string{"same-origin", "none"} {
		rec := f.kerberosGet(negotiate("ticket-alice"), func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", site) })
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s: status=%d", site, rec.Code)
		}
	}
}

func TestKerberosThrottlesTheClientBeforeValidation(t *testing.T) {
	f, k := newKerberosFixture(t, withClientLimit(3))
	for i := 0; i < 3; i++ {
		if rec := f.kerberosGet(negotiate(fmt.Sprintf("forged-%d", i))); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d", i+1, rec.Code)
		}
	}
	calls, dirCalls := k.callCount(), f.dir.calls
	rec := f.kerberosGet(negotiate("ticket-alice"))
	if rec.Code != http.StatusTooManyRequests || rec.errCode() != "auth.too_many_attempts" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("status=%d code=%s retry-after=%q", rec.Code, rec.errCode(), rec.Header().Get("Retry-After"))
	}
	if k.callCount() != calls || f.dir.calls != dirCalls {
		t.Fatal("a locked client reached the validator or the directory")
	}
	// The challenge needs no budget, so a locked client is still told how to
	// authenticate rather than being throttled for the browser's first request.
	if rec := f.kerberosGet(""); rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "Negotiate" {
		t.Fatalf("challenge while locked: status=%d", rec.Code)
	}
	// Another client is unaffected.
	f.trackIP("192.0.2.88")
	rec = f.kerberosGet(negotiate("ticket-alice"), func(r *http.Request) { r.RemoteAddr = "192.0.2.88:1" })
	if rec.Code != http.StatusNoContent {
		t.Fatalf("other client status = %d", rec.Code)
	}
	// Kerberos has its own client budget: broken Kerberos must not lock the
	// password fallback.
	if rec := f.login(f.ident, loginPassword); rec.Code != http.StatusNoContent {
		t.Fatalf("password login from a Kerberos-locked client: status = %d", rec.Code)
	}
}

func TestKerberosSuccessIsRefundedSoSingleSignOnNeverLocksAClient(t *testing.T) {
	f, _ := newKerberosFixture(t, withClientLimit(3))
	for i := 0; i < 10; i++ {
		if rec := f.kerberosGet(negotiate("ticket-alice")); rec.Code != http.StatusNoContent {
			t.Fatalf("login %d: status = %d", i+1, rec.Code)
		}
	}
	if n := f.throttleFailures(kerberosClientKey(f.ip)); n != 0 {
		t.Fatalf("client attempts = %d, want 0", n)
	}
}

func TestParseNegotiate(t *testing.T) {
	long := strings.Repeat("A", maxNegotiateHeaderBytes)
	for _, tc := range []struct {
		name    string
		values  []string
		token   string
		outcome negotiateOutcome
	}{
		{"none", nil, "", negotiateChallenge},
		{"empty value", []string{""}, "", negotiateChallenge},
		{"token", []string{"Negotiate YWJj"}, "YWJj", negotiateToken},
		{"lower case scheme", []string{"negotiate YWJj"}, "YWJj", negotiateToken},
		{"shouting scheme", []string{"NEGOTIATE YWJj"}, "YWJj", negotiateToken},
		{"extra spaces", []string{"  Negotiate   YWJj  "}, "YWJj", negotiateToken},
		{"scheme only", []string{"Negotiate"}, "", negotiateChallenge},
		{"scheme and space", []string{"Negotiate "}, "", negotiateChallenge},
		{"basic", []string{"Basic YWJj"}, "", negotiateChallenge},
		{"prefix of scheme", []string{"NegotiateX YWJj"}, "", negotiateChallenge},
		{"two headers", []string{"Negotiate YWJj", "Negotiate YWJj"}, "", negotiateMalformed},
		{"two headers one empty", []string{"", "Negotiate YWJj"}, "", negotiateMalformed},
		{"oversized", []string{"Negotiate " + long}, "", negotiateMalformed},
		{"at the bound", []string{"Negotiate " + long[:maxNegotiateHeaderBytes-len("Negotiate ")]}, long[:maxNegotiateHeaderBytes-len("Negotiate ")], negotiateToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token, outcome := parseNegotiate(tc.values)
			if token != tc.token || outcome != tc.outcome {
				t.Fatalf("parseNegotiate = %q, %v; want %q, %v", token, outcome, tc.token, tc.outcome)
			}
		})
	}
}

func TestPlainKerberosUsername(t *testing.T) {
	for s, want := range map[string]bool{
		"alice": true, "a.b-c_d$": true, "Ünal": true, "ALICE": true,
		"": false, " alice": false, "alice ": false, `D\alice`: false, "a@b": false, "a/b": false, "a\x00": false, "\xff": false,
		strings.Repeat("a", maxIdentifierBytes+1): false,
	} {
		if got := plainKerberosUsername(s); got != want {
			t.Errorf("plainKerberosUsername(%q) = %v, want %v", s, got, want)
		}
	}
}

func kerberosClientKey(ip string) string {
	return "ip:krb/" + strings.TrimPrefix(ClientKey(ip), "ip:")
}

// Security regression: Unicode case folding (Kelvin sign, dotted I) in the
// directory lookup must not map a different Kerberos principal onto an
// account; only an exact or ASCII-case-insensitive match is accepted.
func TestKerberosRequiresExactUsernameMatch(t *testing.T) {
	f, k := newKerberosFixture(t)
	f.trackKey(kerberosClientKey(f.ip))
	// The fake directory folds like PostgreSQL lower() would for these names.
	kelvin := strings.Replace(f.ident, "a", "\u212a", 1) // not ASCII
	f.dir.accounts["ad|"+NormalizeIdentifier(kelvin)] = f.dir.accounts["ad|"+f.ident]
	k.tickets["ticket-kelvin"] = KerberosPrincipal{Username: kelvin, Realm: krbRealm}
	if rec := f.kerberosGet(negotiate("ticket-kelvin")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("folded non-ASCII principal: status = %d, want 401", rec.Code)
	}
	if f.sessionCount() != 0 {
		t.Fatal("a session was created for a folded principal")
	}
	for _, tc := range []struct {
		principal, synced string
		want              bool
	}{
		{"alice", "alice", true},
		{"ALICE", "alice", true},
		{"Alice", "aLiCe", true},
		{"\u212aate", "kate", false},
		{"kate", "\u212aate", false},
		{"İvan", "ivan", false},
		{"élodie", "élodie", true},
		{"Élodie", "élodie", false},
		{"", "", false},
		{"alice", "alice2", false},
	} {
		if got := kerberosNameMatches(tc.principal, tc.synced); got != tc.want {
			t.Errorf("kerberosNameMatches(%q, %q) = %v, want %v", tc.principal, tc.synced, got, tc.want)
		}
	}
}

// NTLM fallback tokens (non-domain-joined devices) are answered with the
// challenge: not validated, not counted, not audited.
func TestKerberosNTLMTokenIsAChallenge(t *testing.T) {
	f, k := newKerberosFixture(t)
	rec := f.kerberosGet("Negotiate " + base64.StdEncoding.EncodeToString([]byte("NTLMSSP\x00\x01\x00\x00\x00rest")))
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "Negotiate" {
		t.Fatalf("status=%d", rec.Code)
	}
	if k.callCount() != 0 || f.throttleFailures(kerberosClientKey(f.ip)) != 0 || len(f.failureAudits()) != 0 {
		t.Fatal("an NTLM token was validated, counted or audited")
	}
}

func TestKerberosRejectsForeignOrigin(t *testing.T) {
	f, k := newKerberosFixture(t)
	rec := f.kerberosGet(negotiate("ticket-alice"), func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") })
	if rec.Code != http.StatusForbidden || k.callCount() != 0 {
		t.Fatalf("foreign origin: status=%d calls=%d", rec.Code, k.callCount())
	}
	rec = f.kerberosGet(negotiate("ticket-alice"), func(r *http.Request) { r.Header.Set("Origin", "http://"+r.Host) })
	if rec.Code != http.StatusNoContent {
		t.Fatalf("same origin: status=%d", rec.Code)
	}
}
