package authentication

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const (
	loginPassword = "Sup3r-Secret-Directory-Pw!"
	loginDN       = "CN=Alice,OU=Users,DC=example,DC=test"
)

type fakeDirectory struct {
	mu       sync.Mutex
	accounts map[string]DirectoryAccount
	calls    int
	err      error
}

func (d *fakeDirectory) FindDirectoryAccount(_ context.Context, providerKey, identifier string) (DirectoryAccount, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	if d.err != nil {
		return DirectoryAccount{}, false, d.err
	}
	// Resolves like organization.LoginAccounts: DOMAIN\ stripped, trimmed, lower-cased.
	a, ok := d.accounts[providerKey+"|"+NormalizeIdentifier(identifier)]
	return a, ok, nil
}

type fakeVerifier struct {
	mu        sync.Mutex
	passwords map[string]string // dn -> password
	calls     int
	err       error
	lastDN    string
}

func (v *fakeVerifier) VerifyPassword(_ context.Context, dn, password string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls++
	v.lastDN = dn
	if v.err != nil {
		return v.err
	}
	if password == "" || v.passwords[dn] != password {
		return ErrInvalidCredentials
	}
	return nil
}

type fakeLocker struct {
	mu     sync.Mutex
	active map[string]bool
	err    error
	// hook, when set, runs inside the session transaction before the answer.
	hook func(ctx context.Context, tx pgx.Tx) error
}

func (l *fakeLocker) LockActiveUser(ctx context.Context, tx pgx.Tx, userID string) (bool, error) {
	l.mu.Lock()
	active, err, hook := l.active[userID], l.err, l.hook
	l.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, tx); err != nil {
			return false, err
		}
	}
	return active, err
}

type loginFixture struct {
	t        *testing.T
	pool     *pgxpool.Pool
	handler  http.Handler
	svc      *Service
	dir      *fakeDirectory
	ver      *fakeVerifier
	locker   *fakeLocker
	logs     *bytes.Buffer
	userID   string
	ident    string // directory identifier of the test user
	ip       string
	mu       sync.Mutex
	sleeps   []time.Duration
	deps     LoginDeps
	cfg      LoginConfig
	keys     []string
	ips      []string
	withPass bool
	throttle ThrottleConfig
}

type fixtureOption func(*loginFixture)

func withoutPasswordLogin() fixtureOption { return func(f *loginFixture) { f.withPass = false } }
func withEmergency() fixtureOption        { return func(f *loginFixture) { f.cfg.EmergencyEnabled = true } }
func withClientLimit(n int) fixtureOption {
	return func(f *loginFixture) { f.throttle.ClientLimit = n }
}
func withHashWait(d time.Duration) fixtureOption {
	return func(f *loginFixture) { f.cfg.EmergencyHashWait = d }
}
func withTrustedProxies(cidrs ...string) fixtureOption {
	return func(f *loginFixture) {
		for _, c := range cidrs {
			f.cfg.TrustedProxies = append(f.cfg.TrustedProxies, netip.MustParsePrefix(c))
		}
	}
}

func newLoginFixture(t *testing.T, opts ...fixtureOption) *loginFixture {
	t.Helper()
	pool := dbtest.Pool(t)
	ctx := context.Background()
	for _, table := range []string{"platform.sessions", "platform.auth_throttle", "platform.local_credentials"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil || !exists {
			dbtest.Unavailable(t, table+" missing; run make migrate")
		}
	}
	f := &loginFixture{t: t, pool: pool, withPass: true, logs: &bytes.Buffer{}}
	if err := pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&f.userID); err != nil {
		t.Fatal(err)
	}
	suffix := randHex(5)
	f.ident = "alice" + suffix
	f.ip = fmt.Sprintf("10.%d.%d.%d", randByte(), randByte(), randByte())
	f.dir = &fakeDirectory{accounts: map[string]DirectoryAccount{
		"ad|" + f.ident:                   {UserID: f.userID, DistinguishedName: loginDN + suffix},
		"ad|" + f.ident + "@example.test": {UserID: f.userID, DistinguishedName: loginDN + suffix},
	}}
	f.ver = &fakeVerifier{passwords: map[string]string{loginDN + suffix: loginPassword}}
	f.locker = &fakeLocker{active: map[string]bool{f.userID: true}}
	f.cfg = LoginConfig{ProviderKey: "ad"}
	for _, o := range opts {
		o(f)
	}
	logger := slog.New(slog.NewJSONHandler(f.logs, nil))
	f.svc = NewService(pool, Config{IdleTimeout: 30 * time.Minute, AbsoluteTimeout: 8 * time.Hour}, nil)
	f.deps = LoginDeps{
		Pool: pool, Sessions: f.svc, Throttle: NewThrottle(pool, f.throttle, nil), Logger: logger,
		Now:   func() time.Time { return time.Unix(1_000_000, 0) }, // frozen: every failure "took" 0 ms
		Sleep: func(_ context.Context, d time.Duration) { f.mu.Lock(); f.sleeps = append(f.sleeps, d); f.mu.Unlock() },
	}
	if f.withPass {
		f.deps.Directory, f.deps.Verifier, f.deps.Users = f.dir, f.ver, f.locker
	} else {
		f.deps.Users = f.locker // emergency login still needs the user gate
		f.cfg.ProviderKey = ""
	}
	mux := http.NewServeMux()
	RegisterLogin(mux, f.deps, f.cfg)
	f.handler = httpx.Middleware(logger, mux)
	f.trackKey(IdentifierKey(f.ident))
	f.trackKey(AccountKey(f.userID))
	f.trackKey(ClientKey(f.ip))
	f.trackIP(f.ip)
	t.Cleanup(f.cleanup)
	return f
}

func randByte() int { b := make([]byte, 1); _, _ = rand.Read(b); return int(b[0]) }

func (f *loginFixture) trackKey(k string) { f.keys = append(f.keys, k) }

// trackIP registers a client address whose audit rows the test removes.
func (f *loginFixture) trackIP(ip string) {
	f.mu.Lock()
	f.ips = append(f.ips, ip)
	f.mu.Unlock()
	f.trackKey(ClientKey(ip))
}

func (f *loginFixture) cleanup() {
	ctx := context.Background()
	_, _ = f.pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE target_id = ANY($1) OR target_id = $2::text OR target_id IN (SELECT id::text FROM platform.sessions WHERE user_id = $2::uuid) OR metadata->>'clientIp' = ANY($3)`, f.keys, f.userID, f.ips)
	_, _ = f.pool.Exec(ctx, `DELETE FROM platform.sessions WHERE user_id = $1`, f.userID)
	_, _ = f.pool.Exec(ctx, `DELETE FROM platform.auth_throttle WHERE key = ANY($1)`, f.keys)
	_, _ = f.pool.Exec(ctx, `DELETE FROM platform.local_credentials WHERE user_id = $1`, f.userID)
}

func (f *loginFixture) sleepsSnapshot() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.sleeps...)
}

type loginResponse struct {
	*httptest.ResponseRecorder
}

func (r loginResponse) errCode() string {
	var env httpx.ErrorEnvelope
	_ = json.Unmarshal(r.Body.Bytes(), &env)
	return env.Error.Code
}

// uniformBody returns the response body without the request id.
func (r loginResponse) uniformBody() string {
	var env httpx.ErrorEnvelope
	_ = json.Unmarshal(r.Body.Bytes(), &env)
	env.Error.RequestID = ""
	b, _ := json.Marshal(env)
	return string(b)
}

func (r loginResponse) cookieToken(secure bool) string {
	for _, c := range r.Result().Cookies() {
		if c.Name == CookieName(secure) && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

func (f *loginFixture) post(path, body string, mod ...func(*http.Request)) loginResponse {
	f.t.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.RemoteAddr = f.ip + ":4711"
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Content-Type", "application/json")
	for _, m := range mod {
		m(r)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, r)
	return loginResponse{rec}
}

func (f *loginFixture) login(identifier, password string, mod ...func(*http.Request)) loginResponse {
	f.t.Helper()
	f.mu.Lock()
	f.keys = append(f.keys, IdentifierKey(identifier))
	f.mu.Unlock()
	b, _ := json.Marshal(map[string]string{"identifier": identifier, "password": password})
	return f.post("/api/v1/auth/login", string(b), mod...)
}

func (f *loginFixture) auditRows(where string, args ...any) []string {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT action || ' ' || target_type || ' ' || target_id || ' ' || coalesce(actor_id::text,'-') || ' ' || metadata::text FROM platform.audit_events WHERE `+where+` ORDER BY id`, args...)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// failureAudits are the failed-login audits of the fixture's user.
func (f *loginFixture) failureAudits() []string {
	return f.auditRows(`target_type = 'user' AND target_id = $1 AND action LIKE '%.failed'`, f.userID)
}

// unknownFailureAudits are the failed-login audits of unresolved logins from
// the given client address.
func (f *loginFixture) unknownFailureAudits(ip string) []string {
	return f.auditRows(`target_type = 'login' AND target_id = 'unknown' AND action LIKE '%.failed' AND metadata->>'clientIp' = $1`, ip)
}

func (f *loginFixture) sessionCount() int {
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM platform.sessions WHERE user_id = $1`, f.userID).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// throttleFailures is the attempts counter of key.
func (f *loginFixture) throttleFailures(key string) int {
	n, _ := failures(f.t, f.pool, key)
	return n
}

func cookieHeader(token string) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: CookieName(false), Value: token}) }
}

func TestLoginSuccess(t *testing.T) {
	f := newLoginFixture(t)
	// Some earlier failures must be forgotten after a success.
	for i := 0; i < 2; i++ {
		f.login(f.ident, "wrong")
	}
	if n := f.throttleFailures(AccountKey(f.userID)); n != 2 {
		t.Fatalf("setup: attempts = %d", n)
	}
	rec := f.login(strings.ToUpper(f.ident), loginPassword)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	token := rec.cookieToken(false)
	if token == "" {
		t.Fatal("no session cookie")
	}
	c := rec.Result().Cookies()[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Fatalf("cookie attributes: %+v", c)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("login responses must not be cached")
	}
	sess, err := f.svc.Authenticate(context.Background(), token)
	if err != nil || sess.UserID != f.userID || sess.AuthMethod != "ldap" {
		t.Fatalf("session = %+v, %v", sess, err)
	}
	if f.ver.lastDN == "" || !strings.HasPrefix(f.ver.lastDN, loginDN) {
		t.Fatalf("verified DN = %q", f.ver.lastDN)
	}
	if n := f.throttleFailures(AccountKey(f.userID)); n != 0 {
		t.Fatalf("account counter not cleared: %d", n)
	}
	// The successful attempt is given back to the client budget.
	if n := f.throttleFailures(ClientKey(f.ip)); n != 2 {
		t.Fatalf("client attempts = %d, want 2 (the two failures only)", n)
	}
	if got := f.auditRows(`action = 'auth.session.created' AND target_id = $1`, sess.ID); len(got) != 1 || !strings.Contains(got[0], `"ldap"`) {
		t.Fatalf("session audit = %v", got)
	}
	if len(f.sleepsSnapshot()) != 2 { // only the two failures above were delayed
		t.Fatal("successful logins are not delayed")
	}
}

func TestLoginCredentialFailuresAreUniform(t *testing.T) {
	f := newLoginFixture(t)
	suffix := strings.TrimPrefix(f.ident, "alice")

	// disabled/deleted/ambiguous accounts are "not found" in the directory
	// contract; an inactive user is refused when the session is created.
	inactiveID := f.userID[:len(f.userID)-1] + "f"
	if inactiveID == f.userID {
		inactiveID = f.userID[:len(f.userID)-1] + "e"
	}
	f.dir.accounts["ad|inactive"+suffix] = DirectoryAccount{UserID: inactiveID, DistinguishedName: loginDN + suffix}
	f.trackKey(AccountKey(inactiveID))
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.sessions WHERE user_id = $1`, inactiveID)
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE target_id = $1`, inactiveID)
	})

	cases := []struct {
		name       string
		identifier string
		password   string
		reason     string
		userID     string // "" = unresolved
		wantVerify bool
	}{
		{"wrong password", f.ident, "not-the-password", "invalid_password", f.userID, true},
		{"unknown account", "nobody" + suffix, loginPassword, "unknown_account", "", false},
		{"inactive user", "inactive" + suffix, loginPassword, "user_inactive", inactiveID, true},
	}
	var bodies []string
	for _, c := range cases {
		before := f.ver.calls
		rec := f.login(c.identifier, c.password)
		if rec.Code != http.StatusUnauthorized || rec.errCode() != "auth.invalid_credentials" {
			t.Fatalf("%s: status=%d code=%s", c.name, rec.Code, rec.errCode())
		}
		if rec.cookieToken(false) != "" || len(rec.Result().Cookies()) != 0 {
			t.Fatalf("%s: cookie set on failure", c.name)
		}
		if (f.ver.calls > before) != c.wantVerify {
			t.Fatalf("%s: verifier called = %v", c.name, f.ver.calls > before)
		}
		bodies = append(bodies, rec.uniformBody())
		var rows []string
		if c.userID != "" {
			rows = f.auditRows(`target_type = 'user' AND target_id = $1 AND action = 'auth.login.failed'`, c.userID)
		} else {
			rows = f.unknownFailureAudits(f.ip)
		}
		if len(rows) != 1 || !strings.Contains(rows[0], `"reason": "`+c.reason+`"`) || !strings.Contains(rows[0], `"method": "ldap"`) ||
			!strings.Contains(rows[0], `"clientIp": "`+f.ip+`"`) || !strings.Contains(rows[0], `"actor": "login"`) {
			t.Fatalf("%s: audit = %v", c.name, rows)
		}
		if strings.Contains(rows[0], c.identifier) || strings.Contains(rows[0], c.password) {
			t.Fatalf("%s: raw identifier or password in audit: %s", c.name, rows[0])
		}
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] {
			t.Fatalf("credential failure bodies differ:\n%s\n%s", bodies[0], b)
		}
	}
	if f.sessionCount() != 0 {
		t.Fatal("sessions created by failed logins")
	}
	// Every attempt is counted for the client.
	if n := f.throttleFailures(ClientKey(f.ip)); n != 3 {
		t.Fatalf("client attempts = %d, want 3", n)
	}
}

func TestLoginFailureHasMinimumResponseTime(t *testing.T) {
	f := newLoginFixture(t)
	f.login(f.ident, "wrong")
	f.login("nobody"+f.ident, "wrong")
	got := f.sleepsSnapshot()
	if len(got) != 2 || got[0] != 400*time.Millisecond || got[1] != 400*time.Millisecond {
		t.Fatalf("sleeps = %v, want two delays of 400ms (frozen clock)", got)
	}
	// Partial elapsed time is subtracted.
	now := time.Unix(1_000_000, 0)
	calls := 0
	f.deps.Now = func() time.Time {
		calls++
		if calls%2 == 0 {
			return now.Add(150 * time.Millisecond)
		}
		return now
	}
	f.sleeps = nil
	mux := http.NewServeMux()
	RegisterLogin(mux, f.deps, f.cfg)
	f.handler = mux
	f.login(f.ident, "wrong")
	if got := f.sleepsSnapshot(); len(got) != 1 || got[0] > 400*time.Millisecond || got[0] < 100*time.Millisecond {
		t.Fatalf("sleeps = %v", got)
	}
}

func TestLoginFailureTakesAtLeast400msRealTime(t *testing.T) {
	f := newLoginFixture(t)
	f.deps.Now, f.deps.Sleep = nil, nil
	mux := http.NewServeMux()
	RegisterLogin(mux, f.deps, f.cfg)
	f.handler = mux
	started := time.Now()
	rec := f.login("nobody"+f.ident, "wrong")
	if d := time.Since(started); d < 400*time.Millisecond {
		t.Fatalf("failure answered after %v", d)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestLoginEmptyPasswordIsRejectedBeforeAnyLookup(t *testing.T) {
	f := newLoginFixture(t)
	rec := f.post("/api/v1/auth/login", fmt.Sprintf(`{"identifier":%q,"password":""}`, f.ident))
	if rec.Code != http.StatusBadRequest || rec.errCode() != "auth.invalid_request" {
		t.Fatalf("status=%d code=%s", rec.Code, rec.errCode())
	}
	if f.dir.calls != 0 || f.ver.calls != 0 {
		t.Fatalf("directory/verifier called (%d/%d) for an empty password", f.dir.calls, f.ver.calls)
	}
	if f.throttleFailures(ClientKey(f.ip)) != 0 || len(f.failureAudits()) != 0 {
		t.Fatal("input validation errors must not count as failed logins")
	}
}

func TestLoginRequestValidation(t *testing.T) {
	f := newLoginFixture(t)
	big := strings.Repeat("a", 9<<10)
	tests := []struct {
		name string
		body string
	}{
		{"empty body", ``},
		{"not json", `identifier=a&password=b`},
		{"empty identifier", `{"identifier":"","password":"x"}`},
		{"blank identifier", `{"identifier":"   ","password":"x"}`},
		{"missing password", `{"identifier":"a"}`},
		{"unknown field", `{"identifier":"a","password":"x","admin":true}`},
		{"wrong type", `{"identifier":1,"password":"x"}`},
		{"trailing data", `{"identifier":"a","password":"x"} {}`},
		{"trailing garbage", `{"identifier":"a","password":"x"}x`},
		{"identifier too long", fmt.Sprintf(`{"identifier":%q,"password":"x"}`, strings.Repeat("a", 257))},
		{"password too long", fmt.Sprintf(`{"identifier":"a","password":%q}`, strings.Repeat("a", 1025))},
		{"body over 8 KiB", fmt.Sprintf(`{"identifier":"a","password":%q}`, big)},
		{"NUL in password", `{"identifier":"a","password":"x\u0000y"}`},
		{"NUL in identifier", `{"identifier":"a\u0000b","password":"x"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.post("/api/v1/auth/login", tt.body)
			if rec.Code != http.StatusBadRequest || rec.errCode() != "auth.invalid_request" {
				t.Fatalf("status=%d code=%s", rec.Code, rec.errCode())
			}
		})
	}
	if f.dir.calls != 0 || f.ver.calls != 0 {
		t.Fatal("invalid requests reached the directory")
	}
	// Boundary: exactly 256 bytes identifier and 1024 bytes password are accepted by validation.
	rec := f.login(strings.Repeat("a", 256), strings.Repeat("p", 1024))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("boundary request status = %d", rec.Code)
	}
	f.trackKey(IdentifierKey(strings.Repeat("a", 256)))
}

func TestLoginRequiresSameOrigin(t *testing.T) {
	f := newLoginFixture(t)
	b := fmt.Sprintf(`{"identifier":%q,"password":%q}`, f.ident, loginPassword)
	for name, mod := range map[string]func(*http.Request){
		"cross-site":  func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"no headers":  func(r *http.Request) { r.Header.Del("Sec-Fetch-Site") },
		"bad origin":  func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"cross-site2": func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") },
	} {
		rec := f.post("/api/v1/auth/login", b, mod)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d", name, rec.Code)
		}
	}
	if f.ver.calls != 0 {
		t.Fatal("cross-origin request reached the verifier")
	}
}

func TestLoginThrottleLocksAccountBeforeVerification(t *testing.T) {
	f := newLoginFixture(t)
	for i := 0; i < 5; i++ {
		if rec := f.login(f.ident, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d", i+1, rec.Code)
		}
	}
	verCalls := f.ver.calls
	// The correct password is refused while locked, without verification.
	rec := f.login(f.ident, loginPassword)
	if rec.Code != http.StatusTooManyRequests || rec.errCode() != "auth.too_many_attempts" {
		t.Fatalf("status=%d code=%s", rec.Code, rec.errCode())
	}
	ra := rec.Header().Get("Retry-After")
	var secs int
	if _, err := fmt.Sscan(ra, &secs); err != nil || secs < 890 || secs > 900 {
		t.Fatalf("Retry-After = %q, want about 900", ra)
	}
	if f.ver.calls != verCalls {
		t.Fatal("locked request reached the verifier")
	}
	if rec.cookieToken(false) != "" {
		t.Fatal("cookie set while locked")
	}
	if len(f.failureAudits()) != 5 {
		t.Fatalf("throttled requests must not be audited: %d audits, want 5", len(f.failureAudits()))
	}
	if !strings.Contains(f.logs.String(), "login throttled") {
		t.Fatal("throttled request not logged")
	}
	// The same identifier in another case is the same account.
	if rec := f.login(strings.ToUpper(f.ident), loginPassword); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("case variant status = %d", rec.Code)
	}
	// Unknown identifiers are throttled too, so locking does not reveal accounts.
	unknown := "ghost" + f.ident
	for i := 0; i < 5; i++ {
		f.login(unknown, "wrong")
	}
	if rec := f.login(unknown, "wrong"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("unknown identifier not locked: %d", rec.Code)
	}
	// A different identifier from the same client is still served.
	if rec := f.login("other"+f.ident, "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("other identifier status = %d", rec.Code)
	}
}

// Every spelling of an identifier shares one budget: a resolved account has
// one account key, an unresolved identifier one normalized identifier key.
func TestLoginIdentifierVariantsShareOneBudget(t *testing.T) {
	f := newLoginFixture(t)
	variants := []string{f.ident, `X\` + f.ident, `x\ ` + f.ident, strings.ToUpper(f.ident), f.ident + "@example.test"}
	for i, v := range variants {
		if rec := f.login(v, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("variant %d %q: status = %d", i, v, rec.Code)
		}
	}
	if f.ver.calls != 5 {
		t.Fatalf("verifier calls = %d, want 5", f.ver.calls)
	}
	for _, v := range variants {
		if rec := f.login(v, loginPassword); rec.Code != http.StatusTooManyRequests {
			t.Errorf("variant %q after the budget: status = %d, want 429", v, rec.Code)
		}
	}
	if f.ver.calls != 5 || f.sessionCount() != 0 {
		t.Fatalf("verifier calls = %d sessions = %d after lock", f.ver.calls, f.sessionCount())
	}

	// Unresolved spellings share the normalized identifier key.
	ghost := "ghost" + f.ident
	ghostVariants := []string{ghost, `X\` + ghost, `x\ ` + ghost, strings.ToUpper(ghost), "  " + ghost}
	for _, v := range ghostVariants {
		if rec := f.login(v, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("unknown variant %q: status = %d", v, rec.Code)
		}
	}
	for _, v := range ghostVariants {
		if rec := f.login(v, "wrong"); rec.Code != http.StatusTooManyRequests {
			t.Errorf("unknown variant %q after the budget: status = %d, want 429", v, rec.Code)
		}
	}
}

// 60 simultaneous wrong passwords from 60 different clients reach the
// verifier at most five times: the reservation is atomic.
func TestLoginParallelWrongPasswordsAreBoundedByTheAccountBudget(t *testing.T) {
	f := newLoginFixture(t)
	const n = 60
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		ip := fmt.Sprintf("198.51.100.%d", i+1)
		f.trackIP(ip)
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, _ := json.Marshal(map[string]string{"identifier": f.ident, "password": "wrong"})
			r := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(string(b)))
			r.RemoteAddr = ip + ":4711"
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, r)
			codes[i] = rec.Code
		}()
	}
	wg.Wait()
	if f.ver.calls > 5 {
		t.Fatalf("verifier called %d times, want at most 5", f.ver.calls)
	}
	unauthorized, limited := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusUnauthorized:
			unauthorized++
		case http.StatusTooManyRequests:
			limited++
		default:
			t.Fatalf("unexpected status %d", c)
		}
	}
	if unauthorized != 5 || limited != n-5 {
		t.Fatalf("401=%d 429=%d, want 5 and %d", unauthorized, limited, n-5)
	}
}

func TestLoginThrottleLocksClient(t *testing.T) {
	f := newLoginFixture(t)
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("spray%d-%s", i, f.ident)
		if rec := f.login(id, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d", i+1, rec.Code)
		}
	}
	dirCalls, verCalls := f.dir.calls, f.ver.calls
	rec := f.login(f.ident, loginPassword)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("status=%d retry-after=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if f.ver.calls != verCalls || f.dir.calls != dirCalls {
		t.Fatal("a locked client reached the directory lookup or the verifier")
	}
	// Another client is unaffected.
	f.trackIP("192.0.2.77")
	rec = f.login(f.ident, loginPassword, func(r *http.Request) { r.RemoteAddr = "192.0.2.77:1" })
	if rec.Code != http.StatusNoContent {
		t.Fatalf("other client status = %d", rec.Code)
	}
}

// The client budget is per IPv4 address but per /64 for IPv6.
func TestLoginClientBudgetBucketsIPv6By64(t *testing.T) {
	f := newLoginFixture(t, withClientLimit(4))
	from := func(addr string) func(*http.Request) {
		f.trackIP(addr)
		return func(r *http.Request) { r.RemoteAddr = "[" + addr + "]:1" }
	}
	prefix := fmt.Sprintf("2001:db8:%x:%x", randByte(), randByte())
	addrs := []string{prefix + "::1", prefix + "::2", prefix + ":1:2:3:4", prefix + ":ffff:ffff:ffff:ffff"}
	for i, a := range addrs {
		if rec := f.login(fmt.Sprintf("v6-%d-%s", i, f.ident), "wrong", from(a)); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d from %s: status = %d", i, a, rec.Code)
		}
	}
	if rec := f.login("v6-last-"+f.ident, "wrong", from(prefix+":9::9")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a fifth address of the same /64: status = %d, want 429", rec.Code)
	}
	other := fmt.Sprintf("2001:db8:%x:%x::1", randByte()+256, randByte())
	if rec := f.login("v6-other-"+f.ident, "wrong", from(other)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("another /64: status = %d", rec.Code)
	}
}

func TestLoginClientIPBehindTrustedProxy(t *testing.T) {
	f := newLoginFixture(t, withTrustedProxies("10.0.0.0/8"))
	real := "198.51.100." + fmt.Sprint(randByte())
	f.trackIP(real)
	f.login(f.ident, "wrong", func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "6.6.6.6, "+real)
	})
	if n := f.throttleFailures(ClientKey(real)); n != 1 {
		t.Fatalf("failures for the right-most untrusted hop = %d", n)
	}
	if n := f.throttleFailures(ClientKey("6.6.6.6")); n != 0 {
		t.Fatal("forged X-Forwarded-For entry was used")
	}
	rows := f.failureAudits()
	if len(rows) != 1 || !strings.Contains(rows[0], `"clientIp": "`+real+`"`) {
		t.Fatalf("audit = %v", rows)
	}
}

func TestLoginProviderUnavailable(t *testing.T) {
	f := newLoginFixture(t)
	f.ver.err = fmt.Errorf("ldap: connect: %w", ErrProviderUnavailable)
	rec := f.login(f.ident, loginPassword)
	if rec.Code != http.StatusServiceUnavailable || rec.errCode() != "auth.provider_unavailable" {
		t.Fatalf("status=%d code=%s", rec.Code, rec.errCode())
	}
	if len(f.failureAudits()) != 0 {
		t.Fatal("an outage must not be audited as a failed login")
	}
	// The password was never checked: the reserved attempts are refunded so
	// retries during an outage cannot lock the account afterwards.
	if f.throttleFailures(AccountKey(f.userID)) != 0 || f.throttleFailures(ClientKey(f.ip)) != 0 {
		t.Fatal("reserved attempts must be refunded on an outage")
	}
	for i := 0; i < 8; i++ {
		f.login(f.ident, loginPassword)
	}
	f.ver.err = nil
	if rec := f.login(f.ident, loginPassword); rec.Code != http.StatusNoContent {
		t.Fatalf("login after outage status=%d code=%s; outage retries must not lock", rec.Code, rec.errCode())
	}
	if strings.Contains(f.logs.String(), loginPassword) {
		t.Fatal("password logged")
	}
}

func TestLoginInternalErrorsAreGeneric(t *testing.T) {
	f := newLoginFixture(t)
	f.dir.err = errors.New("db password=hunter2")
	rec := f.login(f.ident, loginPassword)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
}

func TestLoginNotConfigured(t *testing.T) {
	f := newLoginFixture(t, withoutPasswordLogin())
	rec := f.login(f.ident, loginPassword)
	if rec.Code != http.StatusNotFound || rec.errCode() != "auth.method_unavailable" {
		t.Fatalf("status=%d code=%s", rec.Code, rec.errCode())
	}
	if f.throttleFailures(ClientKey(f.ip)) != 0 {
		t.Fatal("unconfigured login touched the throttle")
	}
}

func TestLoginRevokesExistingSession(t *testing.T) {
	f := newLoginFixture(t)
	first := f.login(f.ident, loginPassword).cookieToken(false)
	if first == "" {
		t.Fatal("first login failed")
	}
	second := f.login(f.ident, loginPassword, cookieHeader(first))
	newToken := second.cookieToken(false)
	if second.Code != http.StatusNoContent || newToken == "" || newToken == first {
		t.Fatalf("second login: status=%d token=%q", second.Code, newToken)
	}
	if _, err := f.svc.Authenticate(context.Background(), first); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("old session still valid: %v", err)
	}
	if _, err := f.svc.Authenticate(context.Background(), newToken); err != nil {
		t.Fatalf("new session invalid: %v", err)
	}
	rows := f.auditRows(`action = 'auth.session.revoked' AND target_id IN (SELECT id::text FROM platform.sessions WHERE user_id = $1)`, f.userID)
	if len(rows) != 1 || !strings.Contains(rows[0], "replaced_by_login") || !strings.Contains(rows[0], `"replacedByUserId": "`+f.userID+`"`) ||
		!strings.Contains(rows[0], `"actor": "login"`) {
		t.Fatalf("revocation audit = %v", rows)
	}
	// A stale or foreign cookie does not break login.
	stale := f.login(f.ident, loginPassword, cookieHeader(first))
	if stale.Code != http.StatusNoContent {
		t.Fatalf("login with a stale cookie: %d", stale.Code)
	}
}

func TestLoginFailureKeepsExistingSession(t *testing.T) {
	f := newLoginFixture(t)
	first := f.login(f.ident, loginPassword).cookieToken(false)
	if rec := f.login(f.ident, "wrong", cookieHeader(first)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	if _, err := f.svc.Authenticate(context.Background(), first); err != nil {
		t.Fatalf("a failed login must not end the current session: %v", err)
	}
}

func TestLoginAuditNeverContainsSecretsOrRawIdentifiers(t *testing.T) {
	f := newLoginFixture(t)
	long := "nobody-" + f.ident + strings.Repeat("x", 200)
	f.login(f.ident, loginPassword+"-wrong")
	f.login(long, loginPassword+"-wrong")
	f.login(f.ident, loginPassword)

	rows := f.auditRows(`target_id = $1 OR target_id = 'unknown' AND metadata->>'clientIp' = $2 OR target_id IN (SELECT id::text FROM platform.sessions WHERE user_id = $1::uuid)`, f.userID, f.ip)
	if len(rows) != 3 {
		t.Fatalf("audit rows = %v", rows)
	}
	for _, r := range rows {
		if strings.Contains(r, loginPassword) || strings.Contains(r, f.ident) || strings.Contains(r, long[:20]) || strings.Contains(r, `"password"`) || strings.Contains(r, `"identifier"`) {
			t.Fatalf("audit row contains a secret or raw identifier: %s", r)
		}
	}
	if len(f.unknownFailureAudits(f.ip)) != 1 {
		t.Fatalf("unresolved failure must target login/unknown: %v", f.unknownFailureAudits(f.ip))
	}
	if strings.Contains(f.logs.String(), loginPassword) {
		t.Fatal("password in logs")
	}
}

func TestRegisterLoginRequiresUsers(t *testing.T) {
	f := newLoginFixture(t)
	defer func() {
		if recover() == nil {
			t.Fatal("RegisterLogin without Users must panic")
		}
	}()
	d := f.deps
	d.Users = nil
	RegisterLogin(http.NewServeMux(), d, f.cfg)
}

// A user row locked by a status change must not hang the login: the session
// transaction gives up after the lock timeout and answers 503.
func TestLoginLockTimeoutAnswers503(t *testing.T) {
	f := newLoginFixture(t)
	ctx := context.Background()
	const lockKey = 7_245_001
	holder, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lockKey); err != nil {
		t.Fatal(err)
	}
	f.locker.hook = func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lockKey)
		return err
	}
	started := time.Now()
	rec := f.login(f.ident, loginPassword)
	if rec.Code != http.StatusServiceUnavailable || rec.errCode() != "auth.temporarily_unavailable" {
		t.Fatalf("status=%d code=%s", rec.Code, rec.errCode())
	}
	if d := time.Since(started); d < 4*time.Second || d > 15*time.Second {
		t.Fatalf("answered after %v, want about the 5s lock timeout", d)
	}
	if f.sessionCount() != 0 || rec.cookieToken(false) != "" {
		t.Fatal("session created despite the lock timeout")
	}
}

func TestAuthMethodsAndKerberos(t *testing.T) {
	get := func(f *loginFixture, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	f := newLoginFixture(t)
	rec := get(f, "/api/v1/auth/methods")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"emergency":false,"kerberos":false,"password":true}` || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("methods = %d %s", rec.Code, rec.Body)
	}
	f2 := newLoginFixture(t, withoutPasswordLogin(), withEmergency())
	if body := strings.TrimSpace(get(f2, "/api/v1/auth/methods").Body.String()); body != `{"emergency":true,"kerberos":false,"password":false}` {
		t.Fatalf("methods = %s", body)
	}
	rec = get(f, "/api/v1/auth/kerberos")
	var env httpx.ErrorEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if rec.Code != http.StatusNotFound || env.Error.Code != "auth.method_unavailable" {
		t.Fatalf("kerberos = %d %s", rec.Code, rec.Body)
	}
}
