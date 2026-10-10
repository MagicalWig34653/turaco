package entra

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/microsoft"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authentication"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
)

const (
	tenantA  = "11111111-2222-3333-4444-555555555555"
	tenantB  = "99999999-2222-3333-4444-555555555555"
	clientID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	objectID = "0f0f0f0f-0000-1111-2222-333333333333"
)

// fakeIdP is an in-process identity provider: it serves the JWKS and the token endpoint and signs ID tokens with
// keys the test controls.
type fakeIdP struct {
	srv       *httptest.Server
	mu        sync.Mutex
	keys      map[string]*rsa.PrivateKey // kid -> key published in the JWKS
	signKid   string
	signKey   *rsa.PrivateKey
	jwksHits  atomic.Int32
	tokenForm url.Values
	respond   func() (int, any) // overrides the token response
	// me answers Graph /me; meAuth records the Authorization header of the last call.
	me      func() (int, any)
	meAuth  string
	meQuery string
	meHits  atomic.Int32
}

func newKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newIdP(t *testing.T) *fakeIdP {
	t.Helper()
	f := &fakeIdP{keys: map[string]*rsa.PrivateKey{}}
	k := newKey(t)
	f.keys["kid1"], f.signKid, f.signKey = k, "kid1", k
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/discovery/v2.0/keys"):
			f.jwksHits.Add(1)
			f.mu.Lock()
			var list []map[string]string
			for kid, key := range f.keys {
				list = append(list, map[string]string{"kty": "RSA", "use": "sig", "kid": kid,
					"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"})
			}
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": list})
		case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
			_ = r.ParseForm()
			f.mu.Lock()
			f.tokenForm = r.PostForm
			resp := f.respond
			f.mu.Unlock()
			if resp != nil {
				code, body := resp()
				w.WriteHeader(code)
				_ = json.NewEncoder(w).Encode(body)
				return
			}
			http.Error(w, "no token configured", http.StatusInternalServerError)
		case strings.HasSuffix(r.URL.Path, "/v1.0/me"):
			f.meHits.Add(1)
			f.mu.Lock()
			f.meAuth, f.meQuery = r.Header.Get("Authorization"), r.URL.RawQuery
			me := f.me
			f.mu.Unlock()
			if me == nil {
				http.NotFound(w, r)
				return
			}
			code, body := me()
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

type secretCred struct{}

func (secretCred) Apply(form url.Values, _, _ string) error {
	form.Set("client_secret", "s3cret")
	return nil
}
func (secretCred) Kind() string                 { return "secret" }
func (secretCred) ExpiresAt() (time.Time, bool) { return time.Time{}, false }

func newProvider(t *testing.T, f *fakeIdP, mode string, allowed ...string) (*Provider, *time.Time) {
	t.Helper()
	u, _ := url.Parse(f.srv.URL)
	client, err := microsoft.New(microsoft.Config{AllowedHosts: []string{u.Host}, Transport: f.srv.Client().Transport, AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.EntraConfig{Enabled: true, TenantMode: mode, TenantID: tenantA, AllowedTenantIDs: allowed, ClientID: clientID,
		RedirectURL: "https://turaco.example.org/api/v1/auth/entra/callback", MaxClockSkew: 2 * time.Minute, SessionMaxAge: 8 * time.Hour}
	now := time.Now()
	p := New(cfg, client, secretCred{}, func() time.Time { return now })
	p.base, p.graphBase = f.srv.URL, f.srv.URL
	return p, &now
}

func (f *fakeIdP) mint(t *testing.T, now time.Time, mutate func(map[string]any)) string {
	return f.mintWith(t, now, "RS256", f.signKid, f.signKey, mutate)
}

func (f *fakeIdP) mintWith(t *testing.T, now time.Time, alg, kid string, key *rsa.PrivateKey, mutate func(map[string]any)) string {
	t.Helper()
	c := map[string]any{
		"iss": "https://login.microsoftonline.com/" + tenantA + "/v2.0", "aud": clientID, "tid": tenantA, "oid": objectID, "ver": "2.0",
		"nonce": "n-1", "iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(time.Hour).Unix(), "name": "Anna Beispiel", "preferred_username": "anna@example.org",
	}
	if mutate != nil {
		mutate(c)
	}
	h, _ := json.Marshal(map[string]string{"alg": alg, "kid": kid, "typ": "JWT"})
	cb, _ := json.Marshal(c)
	signing := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(cb)
	sig := []byte("x")
	if alg == "RS256" {
		d := sha256.Sum256([]byte(signing))
		sig, _ = rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, d[:])
	}
	if alg == "none" {
		return signing + "."
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (f *fakeIdP) serve(token string) {
	f.mu.Lock()
	f.respond = func() (int, any) { return 200, map[string]string{"id_token": token, "token_type": "Bearer"} }
	f.mu.Unlock()
}

func exchange(p *Provider) (authentication.VerifiedIdentity, error) {
	return p.Exchange(context.Background(), "code", "verifier", "n-1")
}

func TestValidTokenYieldsIdentityAndSendsPKCEAndCredential(t *testing.T) {
	f := newIdP(t)
	p, now := newProvider(t, f, config.EntraTenantSingle, tenantA)
	f.serve(f.mint(t, *now, nil))
	id, err := exchange(p)
	if err != nil || id.TenantID != tenantA || id.ObjectID != objectID || id.Guest || id.DisplayName != "Anna Beispiel" || id.Email != "anna@example.org" {
		t.Fatalf("%+v %v", id, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tokenForm.Get("code_verifier") != "verifier" || f.tokenForm.Get("grant_type") != "authorization_code" || f.tokenForm.Get("client_secret") != "s3cret" ||
		f.tokenForm.Get("redirect_uri") != p.cfg.RedirectURL || f.tokenForm.Get("scope") != "openid profile" {
		t.Fatalf("token request %v", f.tokenForm)
	}
}

func TestAuthorizeURL(t *testing.T) {
	f := newIdP(t)
	p, _ := newProvider(t, f, config.EntraTenantSingle, tenantA)
	u, _ := url.Parse(p.AuthorizeURL("st", "no", "ch"))
	q := u.Query()
	if !strings.HasSuffix(u.Path, "/"+tenantA+"/oauth2/v2.0/authorize") || q.Get("code_challenge_method") != "S256" || q.Get("state") != "st" ||
		q.Get("nonce") != "no" || q.Get("code_challenge") != "ch" || q.Get("prompt") != "select_account" || q.Get("scope") != "openid profile" ||
		q.Get("response_type") != "code" || q.Get("redirect_uri") != p.cfg.RedirectURL || strings.Contains(q.Get("scope"), "offline_access") {
		t.Fatalf("%s", u)
	}
	pm, _ := newProvider(t, f, config.EntraTenantMultiRestricted, tenantA, tenantB)
	if u, _ := url.Parse(pm.AuthorizeURL("a", "b", "c")); !strings.Contains(u.Path, "/organizations/") {
		t.Fatalf("multi tenant authority: %s", u.Path)
	}
}

func TestValidationRefusals(t *testing.T) {
	f := newIdP(t)
	p, now := newProvider(t, f, config.EntraTenantSingle, tenantA)
	other := newKey(t)
	cases := map[string]struct {
		token func() string
		want  error
	}{
		"wrong issuer": {func() string {
			return f.mint(t, *now, func(c map[string]any) { c["iss"] = "https://evil.example/" + tenantA + "/v2.0" })
		}, authentication.ErrOIDCInvalid},
		"issuer of other tenant": {func() string {
			return f.mint(t, *now, func(c map[string]any) { c["iss"] = "https://login.microsoftonline.com/" + tenantB + "/v2.0" })
		}, authentication.ErrOIDCInvalid},
		"wrong audience": {func() string { return f.mint(t, *now, func(c map[string]any) { c["aud"] = "other-app" }) }, authentication.ErrOIDCInvalid},
		"two audiences":  {func() string { return f.mint(t, *now, func(c map[string]any) { c["aud"] = []string{clientID, "x"} }) }, authentication.ErrOIDCInvalid},
		"wrong azp":      {func() string { return f.mint(t, *now, func(c map[string]any) { c["azp"] = "other" }) }, authentication.ErrOIDCInvalid},
		"other tenant": {func() string {
			return f.mint(t, *now, func(c map[string]any) {
				c["tid"] = tenantB
				c["iss"] = "https://login.microsoftonline.com/" + tenantB + "/v2.0"
			})
		}, authentication.ErrOIDCTenantNotAllowed},
		"consumer tenant": {func() string {
			return f.mint(t, *now, func(c map[string]any) {
				c["tid"] = consumerTenantGUID
				c["iss"] = "https://login.microsoftonline.com/" + consumerTenantGUID + "/v2.0"
			})
		}, authentication.ErrOIDCTenantNotAllowed},
		"expired": {func() string {
			return f.mint(t, *now, func(c map[string]any) { c["exp"] = now.Add(-10 * time.Minute).Unix() })
		}, authentication.ErrOIDCInvalid},
		"not yet valid": {func() string {
			return f.mint(t, *now, func(c map[string]any) { c["nbf"] = now.Add(10 * time.Minute).Unix() })
		}, authentication.ErrOIDCInvalid},
		"issued in future": {func() string {
			return f.mint(t, *now, func(c map[string]any) { c["iat"] = now.Add(10 * time.Minute).Unix() })
		}, authentication.ErrOIDCInvalid},
		"issued long ago": {func() string {
			return f.mint(t, *now, func(c map[string]any) { c["iat"] = now.Add(-30 * time.Minute).Unix() })
		}, authentication.ErrOIDCInvalid},
		"missing exp":         {func() string { return f.mint(t, *now, func(c map[string]any) { delete(c, "exp") }) }, authentication.ErrOIDCInvalid},
		"wrong nonce":         {func() string { return f.mint(t, *now, func(c map[string]any) { c["nonce"] = "other" }) }, authentication.ErrOIDCInvalid},
		"missing nonce":       {func() string { return f.mint(t, *now, func(c map[string]any) { delete(c, "nonce") }) }, authentication.ErrOIDCInvalid},
		"v1 token":            {func() string { return f.mint(t, *now, func(c map[string]any) { c["ver"] = "1.0" }) }, authentication.ErrOIDCInvalid},
		"oid not a guid":      {func() string { return f.mint(t, *now, func(c map[string]any) { c["oid"] = "anna@example.org" }) }, authentication.ErrOIDCInvalid},
		"missing oid":         {func() string { return f.mint(t, *now, func(c map[string]any) { delete(c, "oid") }) }, authentication.ErrOIDCInvalid},
		"alg none":            {func() string { return f.mintWith(t, *now, "none", "kid1", nil, nil) }, authentication.ErrOIDCInvalid},
		"alg HS256":           {func() string { return f.mintWith(t, *now, "HS256", "kid1", nil, nil) }, authentication.ErrOIDCInvalid},
		"signed by other key": {func() string { return f.mintWith(t, *now, "RS256", "kid1", other, nil) }, authentication.ErrOIDCInvalid},
		"garbage":             {func() string { return "not.a.jwt.at.all" }, authentication.ErrOIDCInvalid},
		"empty kid":           {func() string { return f.mintWith(t, *now, "RS256", "", f.signKey, nil) }, authentication.ErrOIDCInvalid},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f.serve(tc.token())
			if _, err := exchange(p); !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestSkewIsHonoured(t *testing.T) {
	f := newIdP(t)
	p, now := newProvider(t, f, config.EntraTenantSingle, tenantA)
	f.serve(f.mint(t, *now, func(c map[string]any) { c["exp"] = now.Add(-time.Minute).Unix() })) // 1 minute past exp, skew 2 minutes
	if _, err := exchange(p); err != nil {
		t.Fatalf("within skew: %v", err)
	}
}

func TestGuestDetection(t *testing.T) {
	f := newIdP(t)
	p, now := newProvider(t, f, config.EntraTenantSingle, tenantA)
	for name, mutate := range map[string]func(map[string]any){
		"acct claim":  func(c map[string]any) { c["acct"] = 1 },
		"idp differs": func(c map[string]any) { c["idp"] = "https://sts.windows.net/" + tenantB + "/" },
	} {
		f.serve(f.mint(t, *now, mutate))
		id, err := exchange(p)
		if err != nil || !id.Guest {
			t.Errorf("%s: %+v %v", name, id, err)
		}
	}
}

func TestMultiTenantAllowList(t *testing.T) {
	f := newIdP(t)
	p, now := newProvider(t, f, config.EntraTenantMultiRestricted, tenantA, tenantB)
	f.serve(f.mint(t, *now, func(c map[string]any) {
		c["tid"] = tenantB
		c["iss"] = "https://login.microsoftonline.com/" + tenantB + "/v2.0"
	}))
	if id, err := exchange(p); err != nil || id.TenantID != tenantB {
		t.Fatalf("%+v %v", id, err)
	}
	f.serve(f.mint(t, *now, func(c map[string]any) {
		c["tid"] = "77777777-2222-3333-4444-555555555555"
		c["iss"] = "https://login.microsoftonline.com/77777777-2222-3333-4444-555555555555/v2.0"
	}))
	if _, err := exchange(p); !errors.Is(err, authentication.ErrOIDCTenantNotAllowed) {
		t.Fatalf("unlisted tenant: %v", err)
	}
}

func TestUnknownKidRefreshesOnceAndIsRateLimited(t *testing.T) {
	f := newIdP(t)
	p, now := newProvider(t, f, config.EntraTenantSingle, tenantA)
	f.serve(f.mint(t, *now, nil))
	if _, err := exchange(p); err != nil {
		t.Fatal(err)
	}
	if f.jwksHits.Load() != 1 {
		t.Fatalf("first fetch: %d", f.jwksHits.Load())
	}
	// Key rotation: a new key appears under a new kid; one refresh finds it.
	nk := newKey(t)
	f.mu.Lock()
	f.keys["kid2"] = nk
	f.mu.Unlock()
	f.serve(f.mintWith(t, *now, "RS256", "kid2", nk, nil))
	*now = now.Add(6 * time.Minute)
	if _, err := exchange(p); err != nil {
		t.Fatalf("rotated key: %v", err)
	}
	if f.jwksHits.Load() != 2 {
		t.Fatalf("refresh count %d", f.jwksHits.Load())
	}
	// A forged kid causes one refresh at most per five minutes.
	for i := 0; i < 5; i++ {
		f.serve(f.mintWith(t, *now, "RS256", "forged", nk, nil))
		if _, err := exchange(p); !errors.Is(err, authentication.ErrOIDCInvalid) {
			t.Fatalf("forged kid: %v", err)
		}
	}
	if f.jwksHits.Load() > 3 {
		t.Fatalf("forged kids hammered the JWKS endpoint: %d fetches", f.jwksHits.Load())
	}
}

func TestProviderFailuresAreClassified(t *testing.T) {
	f := newIdP(t)
	p, _ := newProvider(t, f, config.EntraTenantSingle, tenantA)
	f.mu.Lock()
	f.respond = func() (int, any) { return 400, map[string]string{"error": "invalid_grant"} }
	f.mu.Unlock()
	if _, err := exchange(p); !errors.Is(err, authentication.ErrOIDCInvalid) {
		t.Errorf("invalid_grant: %v", err)
	}
	f.mu.Lock()
	f.respond = func() (int, any) { return 503, map[string]string{"error": "temporarily_unavailable"} }
	f.mu.Unlock()
	if _, err := exchange(p); !errors.Is(err, authentication.ErrOIDCUnavailable) {
		t.Errorf("503: %v", err)
	}
	f.mu.Lock()
	f.respond = func() (int, any) { return 200, map[string]string{"token_type": "Bearer"} }
	f.mu.Unlock()
	if _, err := exchange(p); !errors.Is(err, authentication.ErrOIDCInvalid) {
		t.Errorf("no id_token: %v", err)
	}
}

func TestJWKSOutageKeepsCachedKeys(t *testing.T) {
	f := newIdP(t)
	p, now := newProvider(t, f, config.EntraTenantSingle, tenantA)
	f.serve(f.mint(t, *now, nil))
	if _, err := exchange(p); err != nil {
		t.Fatal(err)
	}
	// The JWKS endpoint breaks; tokens with a cached kid still validate, an unknown kid reports unavailable.
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer broken.Close()
	p.base = f.srv.URL // token endpoint unchanged
	f.mu.Lock()
	f.keys = map[string]*rsa.PrivateKey{}
	f.mu.Unlock()
	*now = now.Add(6 * time.Minute)
	if _, err := exchange(p); err != nil {
		t.Fatalf("cached key must still work: %v", err)
	}
	nk := newKey(t)
	f.serve(f.mintWith(t, *now, "RS256", "kidX", nk, nil))
	*now = now.Add(6 * time.Minute)
	if _, err := exchange(p); !errors.Is(err, authentication.ErrOIDCUnavailable) {
		t.Fatalf("unknown kid with broken JWKS: %v", err)
	}
}

// serveWithAccessToken answers the token request with an ID token and a Graph access token.
func (f *fakeIdP) serveWithAccessToken(token, access string) {
	f.mu.Lock()
	f.respond = func() (int, any) {
		return 200, map[string]string{"id_token": token, "access_token": access, "token_type": "Bearer"}
	}
	f.mu.Unlock()
}

func (f *fakeIdP) serveMe(code int, body any) {
	f.mu.Lock()
	f.me = func() (int, any) { return code, body }
	f.mu.Unlock()
}

// adGUIDAnchor is the base64 source anchor Entra Connect writes for the objectGUID aabbccdd-0011-2233-4455-66778899aabb:
// Active Directory stores the first three fields little-endian.
const (
	adGUID       = "aabbccdd-0011-2233-4455-66778899aabb"
	adGUIDAnchor = "3cy7qhEAMyJEVWZ3iJmquw=="
)

func newHybridProvider(t *testing.T, f *fakeIdP, mode string, allowed ...string) (*Provider, *time.Time) {
	t.Helper()
	p, now := newProvider(t, f, mode, allowed...)
	p.cfg.LinkDirectoryProviderKey = "ad"
	return p, now
}

func TestDecodeSourceAnchor(t *testing.T) {
	if got := decodeSourceAnchor(adGUIDAnchor); got != adGUID {
		t.Fatalf("decode = %q, want %q", got, adGUID)
	}
	if got := decodeSourceAnchor(strings.TrimRight(adGUIDAnchor, "=")); got != adGUID {
		t.Fatalf("unpadded decode = %q", got)
	}
	for _, bad := range []string{"", "not base64!", "AAAA", base64.StdEncoding.EncodeToString([]byte("a custom anchor value"))} {
		if got := decodeSourceAnchor(bad); got != "" {
			t.Errorf("decodeSourceAnchor(%q) = %q, want empty", bad, got)
		}
	}
}

func TestScopeAddsUserReadOnlyForTheHybridMatch(t *testing.T) {
	f := newIdP(t)
	plain, _ := newProvider(t, f, config.EntraTenantSingle, tenantA)
	if s := plain.scope(); s != "openid profile" {
		t.Fatalf("plain scope %q", s)
	}
	hybrid, _ := newHybridProvider(t, f, config.EntraTenantSingle, tenantA)
	u, _ := url.Parse(hybrid.AuthorizeURL("s", "n", "c"))
	if u.Query().Get("scope") != "openid profile User.Read" {
		t.Fatalf("authorize scope %q", u.Query().Get("scope"))
	}
	f.serveWithAccessToken(f.mint(t, time.Now(), nil), "graph-token")
	hybrid.now = time.Now
	if _, err := exchange(hybrid); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tokenForm.Get("scope") != "openid profile User.Read" {
		t.Fatalf("token scope %q", f.tokenForm.Get("scope"))
	}
}

func TestSourceAnchorFromGraph(t *testing.T) {
	f := newIdP(t)
	p, now := newHybridProvider(t, f, config.EntraTenantSingle, tenantA)
	f.serveWithAccessToken(f.mint(t, *now, nil), "graph-token")
	f.serveMe(200, map[string]any{"onPremisesImmutableId": adGUIDAnchor, "onPremisesSyncEnabled": true})
	id, err := exchange(p)
	if err != nil || id.SourceAnchor != adGUID || id.ObjectID != objectID {
		t.Fatalf("%+v %v", id, err)
	}
	f.mu.Lock()
	auth, q := f.meAuth, f.meQuery
	f.mu.Unlock()
	if auth != "Bearer graph-token" || !strings.Contains(q, "onPremisesImmutableId") || !strings.Contains(q, "onPremisesSyncEnabled") {
		t.Fatalf("graph request auth=%q query=%q", auth, q)
	}
}

func TestSourceAnchorIsEmptyUnlessEverythingHolds(t *testing.T) {
	cases := map[string]struct {
		me     func(f *fakeIdP)
		mutate func(map[string]any)
		access string
		mode   string
		hits   int32
	}{
		"sync disabled": {func(f *fakeIdP) {
			f.serveMe(200, map[string]any{"onPremisesImmutableId": adGUIDAnchor, "onPremisesSyncEnabled": false})
		}, nil, "t", config.EntraTenantSingle, 1},
		"sync unknown": {func(f *fakeIdP) { f.serveMe(200, map[string]any{"onPremisesImmutableId": adGUIDAnchor}) }, nil, "t", config.EntraTenantSingle, 1},
		"cloud only": {func(f *fakeIdP) {
			f.serveMe(200, map[string]any{"onPremisesImmutableId": nil, "onPremisesSyncEnabled": nil})
		}, nil, "t", config.EntraTenantSingle, 1},
		"custom anchor": {func(f *fakeIdP) {
			f.serveMe(200, map[string]any{"onPremisesImmutableId": "custom-anchor", "onPremisesSyncEnabled": true})
		}, nil, "t", config.EntraTenantSingle, 1},
		"graph error":     {func(f *fakeIdP) { f.serveMe(500, map[string]any{}) }, nil, "t", config.EntraTenantSingle, 1},
		"graph forbidden": {func(f *fakeIdP) { f.serveMe(403, map[string]any{}) }, nil, "t", config.EntraTenantSingle, 1},
		"no access token": {func(f *fakeIdP) {
			f.serveMe(200, map[string]any{"onPremisesImmutableId": adGUIDAnchor, "onPremisesSyncEnabled": true})
		}, nil, "", config.EntraTenantSingle, 0},
		"guest": {func(f *fakeIdP) {
			f.serveMe(200, map[string]any{"onPremisesImmutableId": adGUIDAnchor, "onPremisesSyncEnabled": true})
		}, func(c map[string]any) { c["acct"] = 1 }, "t", config.EntraTenantSingle, 0},
		"other tenant": {func(f *fakeIdP) {
			f.serveMe(200, map[string]any{"onPremisesImmutableId": adGUIDAnchor, "onPremisesSyncEnabled": true})
		}, func(c map[string]any) {
			c["tid"] = tenantB
			c["iss"] = "https://login.microsoftonline.com/" + tenantB + "/v2.0"
		}, "t", config.EntraTenantMultiRestricted, 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newIdP(t)
			p, now := newHybridProvider(t, f, tc.mode, tenantA, tenantB)
			tc.me(f)
			f.serveWithAccessToken(f.mint(t, *now, tc.mutate), tc.access)
			id, err := exchange(p)
			if err != nil || id.SourceAnchor != "" {
				t.Fatalf("anchor %q err %v", id.SourceAnchor, err)
			}
			if f.meHits.Load() != tc.hits {
				t.Fatalf("graph calls = %d, want %d", f.meHits.Load(), tc.hits)
			}
		})
	}
}

func TestGraphIsNotCalledWithoutTheHybridConfiguration(t *testing.T) {
	f := newIdP(t)
	p, now := newProvider(t, f, config.EntraTenantSingle, tenantA)
	f.serveWithAccessToken(f.mint(t, *now, nil), "graph-token")
	f.serveMe(200, map[string]any{"onPremisesImmutableId": adGUIDAnchor, "onPremisesSyncEnabled": true})
	id, err := exchange(p)
	if err != nil || id.SourceAnchor != "" || f.meHits.Load() != 0 {
		t.Fatalf("anchor %q err %v graph calls %d", id.SourceAnchor, err, f.meHits.Load())
	}
}

// Membership is affirmative: the optional acct claim must be 0. A token without any guest signal is indeterminate.
func TestMemberConfirmedNeedsTheAcctClaim(t *testing.T) {
	f := newIdP(t)
	p, now := newProvider(t, f, config.EntraTenantSingle, tenantA)
	for name, tc := range map[string]struct {
		mutate func(map[string]any)
		member bool
	}{
		"acct zero":             {func(c map[string]any) { c["acct"] = 0 }, true},
		"no guest signal":       {nil, false},
		"acct zero foreign idp": {func(c map[string]any) { c["acct"] = 0; c["idp"] = "https://sts.windows.net/" + tenantB + "/" }, false},
	} {
		f.serve(f.mint(t, *now, tc.mutate))
		id, err := exchange(p)
		if err != nil || id.MemberConfirmed != tc.member {
			t.Errorf("%s: %+v %v", name, id, err)
		}
	}
}
