package authentication

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOIDC is an identity provider that returns what the test configures.
type fakeOIDC struct {
	mu       sync.Mutex
	identity VerifiedIdentity
	err      error
	gotCode  string
	gotNonce string
	gotVer   string
	started  []string
	exchange int
}

func (p *fakeOIDC) AuthorizeURL(state, nonce, challenge string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.started = append(p.started, state)
	return "https://login.example.test/authorize?state=" + url.QueryEscape(state) + "&nonce=" + url.QueryEscape(nonce) + "&code_challenge=" + url.QueryEscape(challenge)
}

func (p *fakeOIDC) Exchange(_ context.Context, code, verifier, nonce string) (VerifiedIdentity, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.exchange++
	p.gotCode, p.gotVer, p.gotNonce = code, verifier, nonce
	return p.identity, p.err
}

type fakeEntraDirectory struct {
	users map[string]string // tenant/object -> user id
}

func (d fakeEntraDirectory) FindEntraUser(_ context.Context, tenantID, objectID string) (string, bool, error) {
	id, ok := d.users[tenantID+"/"+objectID]
	return id, ok, nil
}

const (
	entraTenant = "11111111-2222-3333-4444-555555555555"
	entraObject = "0f0f0f0f-0000-1111-2222-333333333333"
)

func withEntra(p *fakeOIDC, dir fakeEntraDirectory, maxAge time.Duration, mods ...func(*EntraLoginDeps)) fixtureOption {
	return func(f *loginFixture) {
		f.entra = &EntraLoginDeps{Provider: p, Identities: dir, SessionMaxAge: maxAge}
		for _, m := range mods {
			m(f.entra)
		}
	}
}

func (f *loginFixture) get(path string, mod ...func(*http.Request)) loginResponse {
	f.t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	r.RemoteAddr = f.ip + ":4711"
	for _, m := range mod {
		m(r)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, r)
	return loginResponse{rec}
}

// startEntra runs the start endpoint and returns the state, the nonce and the binding cookie.
func (f *loginFixture) startEntra(returnTo string) (state, nonce string, binding *http.Cookie) {
	f.t.Helper()
	path := "/api/v1/auth/entra/start"
	if returnTo != "" {
		path += "?returnTo=" + url.QueryEscape(returnTo)
	}
	rec := f.get(path)
	if rec.Code != http.StatusFound {
		f.t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		f.t.Fatal(err)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == oidcBindingCookieName(false) {
			binding = c
		}
	}
	if binding == nil {
		f.t.Fatal("no binding cookie")
	}
	return loc.Query().Get("state"), loc.Query().Get("nonce"), binding
}

func (f *loginFixture) callback(state, code string, binding *http.Cookie) loginResponse {
	f.t.Helper()
	path := "/api/v1/auth/entra/callback?state=" + url.QueryEscape(state) + "&code=" + url.QueryEscape(code)
	return f.get(path, func(r *http.Request) {
		if binding != nil {
			r.AddCookie(binding)
		}
	})
}

func loginErrorCode(rec loginResponse) string {
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || u.Path != "/login" {
		return "?" + rec.Header().Get("Location")
	}
	return u.Query().Get("error")
}

func newEntraFixture(t *testing.T, p *fakeOIDC, mods ...func(*EntraLoginDeps)) *loginFixture {
	t.Helper()
	// userID is known only after the fixture exists, so the directory is filled afterwards.
	dir := fakeEntraDirectory{users: map[string]string{}}
	f := newLoginFixture(t, withEmergency(), withEntra(p, dir, 8*time.Hour, mods...))
	dir.users[entraTenant+"/"+entraObject] = f.userID
	f.trackKey(ClientKey("entra/" + f.ip))
	f.trackKey("ip:entra/" + f.ip)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM platform.oidc_login_transactions`)
	})
	return f
}

func TestEntraMethodsReportsAvailability(t *testing.T) {
	f := newEntraFixture(t, &fakeOIDC{})
	rec := f.get("/api/v1/auth/methods")
	if !strings.Contains(rec.Body.String(), `"entra":true`) {
		t.Fatalf("%s", rec.Body.String())
	}
	g := newLoginFixture(t)
	if rec := g.get("/api/v1/auth/methods"); !strings.Contains(rec.Body.String(), `"entra":false`) {
		t.Fatalf("%s", rec.Body.String())
	}
	if rec := g.get("/api/v1/auth/entra/start"); rec.Code != http.StatusNotFound {
		t.Fatalf("disabled start: %d", rec.Code)
	}
}

func TestEntraSuccessCreatesSessionAndRedirectsToReturnTo(t *testing.T) {
	p := &fakeOIDC{identity: VerifiedIdentity{TenantID: entraTenant, ObjectID: entraObject}}
	f := newEntraFixture(t, p)
	state, nonce, binding := f.startEntra("/service-desk?x=1")
	rec := f.callback(state, "the-code", binding)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/service-desk?x=1" {
		t.Fatalf("%d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec.cookieToken(false) == "" || f.sessionCount() != 1 {
		t.Fatal("no session")
	}
	if p.gotCode != "the-code" || p.gotNonce != nonce || p.gotVer == "" {
		t.Fatalf("exchange got %q %q %q", p.gotCode, p.gotNonce, p.gotVer)
	}
	var method string
	var absolute time.Duration
	if err := f.pool.QueryRow(context.Background(), `SELECT auth_method, absolute_expires_at - created_at FROM platform.sessions WHERE user_id = $1`, f.userID).Scan(&method, &absolute); err != nil {
		t.Fatal(err)
	}
	if method != "entra" || absolute > 8*time.Hour+time.Minute {
		t.Fatalf("session method %s lifetime %v", method, absolute)
	}
	// The binding cookie is cleared.
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == oidcBindingCookieName(false) && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("binding cookie not cleared")
	}
}

func TestEntraSessionCapSettingLowersLifetime(t *testing.T) {
	p := &fakeOIDC{identity: VerifiedIdentity{TenantID: entraTenant, ObjectID: entraObject}}
	f := newEntraFixture(t, p)
	f.entra.SessionCap = func(context.Context) time.Duration { return 2 * time.Hour }
	state, _, binding := f.startEntra("")
	if rec := f.callback(state, "c", binding); rec.Code != http.StatusFound {
		t.Fatal(rec.Code)
	}
	var absolute time.Duration
	_ = f.pool.QueryRow(context.Background(), `SELECT absolute_expires_at - created_at FROM platform.sessions WHERE user_id = $1`, f.userID).Scan(&absolute)
	if absolute > 2*time.Hour+time.Minute {
		t.Fatalf("lifetime %v", absolute)
	}
}

func TestEntraTransactionIsSingleUse(t *testing.T) {
	p := &fakeOIDC{identity: VerifiedIdentity{TenantID: entraTenant, ObjectID: entraObject}}
	f := newEntraFixture(t, p)
	state, _, binding := f.startEntra("")
	if rec := f.callback(state, "c", binding); rec.Code != http.StatusFound || loginErrorCode(rec) == "entra_failed" {
		t.Fatal("first use must succeed")
	}
	rec := f.callback(state, "c", binding)
	if loginErrorCode(rec) != "entra_failed" || p.exchange != 1 {
		t.Fatalf("replay: %s, exchanges %d", loginErrorCode(rec), p.exchange)
	}
}

func TestEntraStateAndBindingFailuresAreUniformAndConsumeTheTransaction(t *testing.T) {
	p := &fakeOIDC{identity: VerifiedIdentity{TenantID: entraTenant, ObjectID: entraObject}}
	f := newEntraFixture(t, p)
	state, _, binding := f.startEntra("")
	// Without the browser binding cookie (another browser) the sign-in fails ...
	if rec := f.callback(state, "c", nil); loginErrorCode(rec) != "entra_failed" {
		t.Fatalf("no cookie: %s", loginErrorCode(rec))
	}
	// ... and the transaction is gone even for the right browser afterwards.
	if rec := f.callback(state, "c", binding); loginErrorCode(rec) != "entra_failed" {
		t.Fatalf("after failure: %s", loginErrorCode(rec))
	}
	// A wrong binding value fails too.
	state2, _, binding2 := f.startEntra("")
	wrong := &http.Cookie{Name: binding2.Name, Value: binding2.Value + "x"}
	if rec := f.callback(state2, "c", wrong); loginErrorCode(rec) != "entra_failed" {
		t.Fatalf("wrong cookie: %s", loginErrorCode(rec))
	}
	for _, bad := range []string{"", "unknown-state", strings.Repeat("a", 200)} {
		if rec := f.callback(bad, "c", binding); loginErrorCode(rec) != "entra_failed" {
			t.Fatalf("state %q: %s", bad, loginErrorCode(rec))
		}
	}
	if p.exchange != 0 || f.sessionCount() != 0 {
		t.Fatalf("exchange %d sessions %d", p.exchange, f.sessionCount())
	}
	if len(f.failureAudits())+len(f.unknownFailureAudits(f.ip)) == 0 {
		t.Fatal("failures must be audited")
	}
}

func TestEntraExpiredTransactionIsRefused(t *testing.T) {
	p := &fakeOIDC{identity: VerifiedIdentity{TenantID: entraTenant, ObjectID: entraObject}}
	f := newEntraFixture(t, p)
	state, _, binding := f.startEntra("")
	if _, err := f.pool.Exec(context.Background(), `UPDATE platform.oidc_login_transactions SET created_at = '1970-01-01'`); err != nil {
		t.Fatal(err)
	}
	// The handler clock of the fixture is frozen at 1970-01-12, so the stored time is set before it by more than the TTL.
	if rec := f.callback(state, "c", binding); loginErrorCode(rec) != "entra_failed" || p.exchange != 0 {
		t.Fatalf("%s exchanges %d", loginErrorCode(rec), p.exchange)
	}
}

func TestEntraFailureCodesAreGenericAndAudited(t *testing.T) {
	cases := map[string]struct {
		prov   *fakeOIDC
		code   string
		reason string
	}{
		"invalid token": {&fakeOIDC{err: ErrOIDCInvalid}, "entra_failed", "invalid_token"},
		"tenant":        {&fakeOIDC{err: ErrOIDCTenantNotAllowed}, "entra_failed", "tenant_not_allowed"},
		"guest error":   {&fakeOIDC{err: ErrOIDCGuestRefused}, "entra_failed", "guest_refused"},
		"guest flag":    {&fakeOIDC{identity: VerifiedIdentity{TenantID: entraTenant, ObjectID: entraObject, Guest: true}}, "entra_failed", "guest_refused"},
		"unavailable":   {&fakeOIDC{err: ErrOIDCUnavailable}, "entra_unavailable", "provider_unavailable"},
		"not linked":    {&fakeOIDC{identity: VerifiedIdentity{TenantID: entraTenant, ObjectID: "ffffffff-0000-1111-2222-333333333333", Email: "anna@example.org"}}, "entra_not_linked", "not_linked"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newEntraFixture(t, tc.prov)
			state, _, binding := f.startEntra("")
			rec := f.callback(state, "c", binding)
			if rec.Code != http.StatusFound || loginErrorCode(rec) != tc.code {
				t.Fatalf("%d %s", rec.Code, rec.Header().Get("Location"))
			}
			if rec.cookieToken(false) != "" || f.sessionCount() != 0 {
				t.Fatal("a failed sign-in must not create a session")
			}
			rows := f.unknownFailureAudits(f.ip)
			if len(rows) == 0 || !strings.Contains(rows[len(rows)-1], tc.reason) || !strings.Contains(rows[len(rows)-1], `"method": "entra"`) && !strings.Contains(rows[len(rows)-1], `"method":"entra"`) {
				t.Fatalf("audit %v", rows)
			}
			for _, row := range rows {
				if strings.Contains(row, "anna@example.org") || strings.Contains(row, "the-code") {
					t.Fatalf("audit leaks claims: %s", row)
				}
			}
		})
	}
}

func TestEntraInactiveUserIsRefused(t *testing.T) {
	p := &fakeOIDC{identity: VerifiedIdentity{TenantID: entraTenant, ObjectID: entraObject}}
	f := newEntraFixture(t, p)
	f.locker.active[f.userID] = false
	state, _, binding := f.startEntra("")
	rec := f.callback(state, "c", binding)
	if loginErrorCode(rec) != "entra_failed" || f.sessionCount() != 0 {
		t.Fatalf("%s sessions %d", loginErrorCode(rec), f.sessionCount())
	}
	if len(f.failureAudits()) == 0 {
		t.Fatal("not audited against the user")
	}
}

func TestEntraProviderErrorParameterEndsTheAttempt(t *testing.T) {
	p := &fakeOIDC{identity: VerifiedIdentity{TenantID: entraTenant, ObjectID: entraObject}}
	f := newEntraFixture(t, p)
	state, _, binding := f.startEntra("")
	rec := f.get("/api/v1/auth/entra/callback?error=access_denied&state="+url.QueryEscape(state), func(r *http.Request) { r.AddCookie(binding) })
	if loginErrorCode(rec) != "entra_failed" || p.exchange != 0 {
		t.Fatalf("%s %d", loginErrorCode(rec), p.exchange)
	}
}

func TestSafeReturnTo(t *testing.T) {
	for in, want := range map[string]string{
		"": "/", "/": "/", "/my-work": "/my-work", "/a?b=c#d": "/a?b=c#d",
		"//evil.example": "/", "https://evil.example": "/", "/\\evil": "/", "javascript:alert(1)": "/", "evil": "/",
		"/ok\nX": "/", "/\x00": "/", "/%2f%2fevil": "/%2f%2fevil", strings.Repeat("/a", 400): "/",
	} {
		if got := safeReturnTo(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestEntraStartRedirectsToProviderWithPKCEAndBinding(t *testing.T) {
	p := &fakeOIDC{}
	f := newEntraFixture(t, p)
	rec := f.get("/api/v1/auth/entra/start?returnTo=//evil.example")
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "https://login.example.test/authorize?") {
		t.Fatalf("%d %s", rec.Code, rec.Header().Get("Location"))
	}
	var c *http.Cookie
	for _, k := range rec.Result().Cookies() {
		if k.Name == oidcBindingCookieName(false) {
			c = k
		}
	}
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.MaxAge != 600 {
		t.Fatalf("binding cookie %+v", c)
	}
	// Only hashes of state and binding are stored; the open-redirect target was replaced by "/".
	var stored, returnTo string
	if err := f.pool.QueryRow(context.Background(), `SELECT encode(state_hash,'hex'), return_to FROM platform.oidc_login_transactions ORDER BY created_at DESC LIMIT 1`).Scan(&stored, &returnTo); err != nil {
		t.Fatal(err)
	}
	if returnTo != "/" || stored == p.started[0] {
		t.Fatalf("returnTo %q, stored %q", returnTo, stored)
	}
	if secure := (&loginHandler{cfg: LoginConfig{SecureCookie: true}}); oidcBindingCookieName(secure.cfg.SecureCookie) != "__Host-turaco_oidc" {
		t.Fatal("secure deployments use the __Host- prefix")
	}
}

// fakeAnchors and fakeProvisioner record their calls and return what the test configures.
type fakeAnchors struct {
	user  *string
	err   error
	calls []string
}

func (a *fakeAnchors) LinkBySourceAnchor(_ context.Context, tenantID, objectID, anchor, _ string) (string, error) {
	a.calls = append(a.calls, tenantID+"/"+objectID+"/"+anchor)
	if a.err != nil {
		return "", a.err
	}
	return *a.user, nil
}

type fakeProvisioner struct {
	user  *string
	err   error
	calls []string
}

func (p *fakeProvisioner) ProvisionEmployee(_ context.Context, tenantID, objectID, name, email, _ string) (string, error) {
	p.calls = append(p.calls, tenantID+"/"+objectID+"/"+name+"/"+email)
	if p.err != nil {
		return "", p.err
	}
	return *p.user, nil
}

const entraAnchor = "aabbccdd-0011-2233-4455-66778899aabb"

func newIdentity() VerifiedIdentity {
	return VerifiedIdentity{TenantID: entraTenant, ObjectID: "ffffffff-0000-1111-2222-333333333333", DisplayName: "Anna Beispiel", Email: "anna@example.org", SourceAnchor: entraAnchor}
}

// signInUnlinked runs a sign-in of an identity that has no link and returns the response and the audit rows.
func signInUnlinked(t *testing.T, p *fakeOIDC, mods ...func(*EntraLoginDeps)) (*loginFixture, loginResponse) {
	t.Helper()
	f := newEntraFixture(t, p, mods...)
	state, _, binding := f.startEntra("")
	return f, f.callback(state, "c", binding)
}

func TestEntraHybridMatchLinksAndSignsIn(t *testing.T) {
	var uid string
	anchors := &fakeAnchors{user: &uid}
	prov := &fakeProvisioner{user: &uid}
	p := &fakeOIDC{identity: newIdentity()}
	f := newEntraFixture(t, p, func(d *EntraLoginDeps) {
		d.HomeTenantID, d.Anchors, d.Provisioner = entraTenant, anchors, prov
		d.Provisioning = func(context.Context) string { return EntraProvisioningAuto }
	})
	uid = f.userID
	state, _, binding := f.startEntra("")
	rec := f.callback(state, "c", binding)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" || f.sessionCount() != 1 {
		t.Fatalf("%d %s sessions %d", rec.Code, rec.Header().Get("Location"), f.sessionCount())
	}
	if len(anchors.calls) != 1 || anchors.calls[0] != entraTenant+"/ffffffff-0000-1111-2222-333333333333/"+entraAnchor {
		t.Fatalf("anchor calls %v", anchors.calls)
	}
	if len(prov.calls) != 0 {
		t.Fatalf("a matched person must not be provisioned: %v", prov.calls)
	}
}

func TestEntraHybridMatchIsNotAttemptedOutsideItsPreconditions(t *testing.T) {
	cases := map[string]struct {
		mutate func(*VerifiedIdentity)
		deps   func(d *EntraLoginDeps)
	}{
		"no anchor":      {func(v *VerifiedIdentity) { v.SourceAnchor = "" }, nil},
		"other tenant":   {func(v *VerifiedIdentity) { v.TenantID = "99999999-2222-3333-4444-555555555555" }, nil},
		"no home tenant": {nil, func(d *EntraLoginDeps) { d.HomeTenantID = "" }},
		"not configured": {nil, func(d *EntraLoginDeps) { d.Anchors = nil }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var uid string
			anchors := &fakeAnchors{user: &uid}
			id := newIdentity()
			if tc.mutate != nil {
				tc.mutate(&id)
			}
			f, rec := signInUnlinked(t, &fakeOIDC{identity: id}, func(d *EntraLoginDeps) {
				d.HomeTenantID, d.Anchors = entraTenant, anchors
				if tc.deps != nil {
					tc.deps(d)
				}
			})
			uid = f.userID
			if loginErrorCode(rec) != "entra_not_linked" || f.sessionCount() != 0 || len(anchors.calls) != 0 {
				t.Fatalf("%s sessions %d anchor calls %v", loginErrorCode(rec), f.sessionCount(), anchors.calls)
			}
		})
	}
}

func TestEntraResolutionRefusalsAreUniformAuditedAndNeverProvision(t *testing.T) {
	cases := map[string]struct {
		anchorErr error
		provErr   error
		reason    string
	}{
		"anchor ambiguous": {anchorErr: ErrAnchorAmbiguous, reason: "source_anchor_ambiguous"},
		"anchor refused":   {anchorErr: ErrAnchorRefused, reason: "source_anchor_refused"},
		"email collision":  {anchorErr: ErrAnchorNoMatch, provErr: ErrProvisionEmailConflict, reason: "email_conflict"},
		"unusable profile": {anchorErr: ErrAnchorNoMatch, provErr: ErrProvisionRefused, reason: "provisioning_refused"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var uid string
			anchors := &fakeAnchors{user: &uid, err: tc.anchorErr}
			prov := &fakeProvisioner{user: &uid, err: tc.provErr}
			f, rec := signInUnlinked(t, &fakeOIDC{identity: newIdentity()}, func(d *EntraLoginDeps) {
				d.HomeTenantID, d.Anchors, d.Provisioner = entraTenant, anchors, prov
				d.Provisioning = func(context.Context) string { return EntraProvisioningAuto }
			})
			if loginErrorCode(rec) != "entra_not_linked" || f.sessionCount() != 0 {
				t.Fatalf("%s sessions %d", loginErrorCode(rec), f.sessionCount())
			}
			rows := f.unknownFailureAudits(f.ip)
			if len(rows) == 0 || !strings.Contains(rows[len(rows)-1], tc.reason) {
				t.Fatalf("audit %v", rows)
			}
			for _, row := range rows {
				if strings.Contains(row, "anna@example.org") || strings.Contains(row, entraAnchor) {
					t.Fatalf("audit leaks claims: %s", row)
				}
			}
			if tc.anchorErr != ErrAnchorNoMatch && len(prov.calls) != 0 {
				t.Fatalf("an anchor refusal must not fall through to provisioning: %v", prov.calls)
			}
		})
	}
}

func TestEntraProvisioningRules(t *testing.T) {
	auto := func(context.Context) string { return EntraProvisioningAuto }
	linkOnly := func(context.Context) string { return "link_only" }
	cases := map[string]struct {
		setting func(context.Context) string
		mutate  func(*VerifiedIdentity)
	}{
		"link_only is the default": {linkOnly, nil},
		"setting unavailable":      {nil, nil},
		"other tenant":             {auto, func(v *VerifiedIdentity) { v.TenantID = "99999999-2222-3333-4444-555555555555" }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var uid string
			prov := &fakeProvisioner{user: &uid}
			id := newIdentity()
			id.SourceAnchor = ""
			if tc.mutate != nil {
				tc.mutate(&id)
			}
			f, rec := signInUnlinked(t, &fakeOIDC{identity: id}, func(d *EntraLoginDeps) {
				d.HomeTenantID, d.Provisioner, d.Provisioning = entraTenant, prov, tc.setting
			})
			if loginErrorCode(rec) != "entra_not_linked" || len(prov.calls) != 0 || f.sessionCount() != 0 {
				t.Fatalf("%s calls %v", loginErrorCode(rec), prov.calls)
			}
		})
	}
	t.Run("auto creates the session of the provisioned user", func(t *testing.T) {
		var uid string
		prov := &fakeProvisioner{user: &uid}
		id := newIdentity()
		id.SourceAnchor = ""
		p := &fakeOIDC{identity: id}
		f := newEntraFixture(t, p, func(d *EntraLoginDeps) { d.HomeTenantID, d.Provisioner, d.Provisioning = entraTenant, prov, auto })
		uid = f.userID
		state, _, binding := f.startEntra("")
		rec := f.callback(state, "c", binding)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" || f.sessionCount() != 1 {
			t.Fatalf("%d %s sessions %d", rec.Code, rec.Header().Get("Location"), f.sessionCount())
		}
		if len(prov.calls) != 1 || prov.calls[0] != entraTenant+"/ffffffff-0000-1111-2222-333333333333/Anna Beispiel/anna@example.org" {
			t.Fatalf("provisioner calls %v", prov.calls)
		}
	})
}
