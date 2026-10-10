package microsoft

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/providerstatus"
)

// fakeGraph serves the token endpoint and a few Graph resources. The payload shapes follow
// https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-client-creds-grant-flow (token answer and
// error answer, read 2026-10-10) and https://learn.microsoft.com/en-us/graph/throttling (429 with Retry-After,
// read 2026-10-10) and https://learn.microsoft.com/en-us/graph/paging (@odata.nextLink, read 2026-10-10).
type fakeGraph struct {
	srv        *httptest.Server
	tokenCalls atomic.Int32
	throttle   atomic.Int32 // remaining 429 answers
	expiresIn  int
	tokenCode  int
	revoke     atomic.Int32 // remaining 401 answers from Graph
	lastForm   atomic.Value
}

func newFakeGraph(t *testing.T) (*fakeGraph, *Graph, *[]time.Duration) {
	t.Helper()
	f := &fakeGraph{expiresIn: 3599, tokenCode: 200}
	mux := http.NewServeMux()
	mux.HandleFunc("/tenant-1/oauth2/v2.0/token", func(w http.ResponseWriter, r *http.Request) {
		f.tokenCalls.Add(1)
		_ = r.ParseForm()
		f.lastForm.Store(r.PostForm)
		if f.tokenCode != 200 {
			w.WriteHeader(f.tokenCode)
			_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"AADSTS7000215: Invalid client secret provided. Secret=s3cr3t","error_codes":[7000215]}`))
			return
		}
		_, _ = w.Write([]byte(`{"token_type":"Bearer","expires_in":` + itoa(f.expiresIn) + `,"access_token":"tok-` + itoa(int(f.tokenCalls.Load())) + `"}`))
	})
	mux.HandleFunc("/v1.0/ping", func(w http.ResponseWriter, r *http.Request) {
		if f.throttle.Add(-1) >= 0 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"TooManyRequests","innerError":{"code":"429","request-id":"94fb3b52-452a-4535-a601-69e0a90e3aa2"},"message":"Please retry again later."}}`))
			return
		}
		if f.revoke.Add(-1) >= 0 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"auth":"` + r.Header.Get("Authorization") + `","unknownField":{"x":1}}`))
	})
	mux.HandleFunc("/v1.0/forbidden", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("request-id", "94fb3b52-452a-4535-a601-69e0a90e3aa2")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"Authorization_RequestDenied","message":"Insufficient privileges for tenant contoso"}}`))
	})
	mux.HandleFunc("/v1.0/items", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`{"value":[{"id":"c"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"value":[{"id":"a"},{"id":"b"}],"@odata.nextLink":"` + f.srv.URL + `/v1.0/items?page=2"}`))
	})
	mux.HandleFunc("/v1.0/evil", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"value":[],"@odata.nextLink":"https://evil.example/v1.0/steal"}`))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)

	dir := t.TempDir()
	secretFile := filepath.Join(dir, "secret")
	if err := os.WriteFile(secretFile, []byte("s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cred, err := NewSecretCredential(secretFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	var sleeps []time.Duration
	now := time.Now()
	g, err := NewGraph(GraphConfig{
		TenantID: "tenant-1", ClientID: "client-1", Credential: cred,
		BaseURL: f.srv.URL, AuthorityBase: f.srv.URL, Transport: f.srv.Client().Transport, AllowInsecureHTTP: true,
		Now:   func() time.Time { return now },
		Sleep: func(_ context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return f, g, &sleeps
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestGraphAcquiresAndCachesTokenAndSendsClientCredentials(t *testing.T) {
	f, g, _ := newFakeGraph(t)
	var out struct {
		OK   bool   `json:"ok"`
		Auth string `json:"auth"`
	}
	for i := 0; i < 3; i++ {
		if err := g.Get(context.Background(), "/v1.0/ping", &out); err != nil {
			t.Fatal(err)
		}
	}
	if !out.OK || out.Auth != "Bearer tok-1" {
		t.Fatalf("answer %+v", out)
	}
	if n := f.tokenCalls.Load(); n != 1 {
		t.Fatalf("token endpoint called %d times, want 1 (cached)", n)
	}
	form := f.lastForm.Load().(url.Values)
	if form["grant_type"][0] != "client_credentials" || form["scope"][0] != "https://graph.microsoft.com/.default" ||
		form["client_id"][0] != "client-1" || form["client_secret"][0] != "s3cr3t" {
		t.Fatalf("token request %v", form)
	}
	if st := g.Status(); st.State != providerstatus.Verified || st.LastSuccessAt == nil {
		t.Fatalf("status %+v", st)
	}
}

func TestGraphStatusIsUnverifiedUntilACallSucceeded(t *testing.T) {
	_, g, _ := newFakeGraph(t)
	if st := g.Status(); st.State != providerstatus.Unverified {
		t.Fatalf("initial status %+v", st)
	}
}

func TestGraphRefreshesAnExpiringToken(t *testing.T) {
	f, g, _ := newFakeGraph(t)
	f.expiresIn = 120 // shorter than the 5 minute skew: every call needs a new token
	for i := 0; i < 2; i++ {
		if err := g.Get(context.Background(), "/v1.0/ping", nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.tokenCalls.Load(); n != 2 {
		t.Fatalf("token calls %d", n)
	}
}

func TestGraphHonoursRetryAfterOn429(t *testing.T) {
	f, g, sleeps := newFakeGraph(t)
	f.throttle.Store(2)
	if err := g.Get(context.Background(), "/v1.0/ping", nil); err != nil {
		t.Fatal(err)
	}
	if len(*sleeps) != 2 || (*sleeps)[0] != 7*time.Second || (*sleeps)[1] != 7*time.Second {
		t.Fatalf("sleeps %v", *sleeps)
	}
}

func TestGraphReturnsTransientErrorForLongRetryAfterWithoutWaiting(t *testing.T) {
	f, g, sleeps := newFakeGraph(t)
	f.throttle.Store(10)
	g.maxWait = 5 * time.Second // Retry-After 7s is longer
	err := g.Get(context.Background(), "/v1.0/ping", nil)
	var te *TransientError
	if !errors.As(err, &te) || te.Status != 429 || te.RetryAfter != 7*time.Second || len(*sleeps) != 0 {
		t.Fatalf("err %v sleeps %v", err, *sleeps)
	}
	if st := g.Status(); st.State != providerstatus.Failing || st.LastErrorCode != "http_429" {
		t.Fatalf("status %+v", st)
	}
}

func TestGraphGivesUpAfterMaxAttempts(t *testing.T) {
	f, g, sleeps := newFakeGraph(t)
	f.throttle.Store(100)
	err := g.Get(context.Background(), "/v1.0/ping", nil)
	var te *TransientError
	if !errors.As(err, &te) || len(*sleeps) != defaultMaxAttempts-1 {
		t.Fatalf("err %v sleeps %v", err, *sleeps)
	}
}

func TestGraphRetriesOnceAfter401WithAFreshToken(t *testing.T) {
	f, g, _ := newFakeGraph(t)
	f.revoke.Store(1)
	var out struct{ Auth string }
	if err := g.Get(context.Background(), "/v1.0/ping", &out); err != nil {
		t.Fatal(err)
	}
	if f.tokenCalls.Load() != 2 || out.Auth != "Bearer tok-2" {
		t.Fatalf("tokens %d auth %q", f.tokenCalls.Load(), out.Auth)
	}
	f.revoke.Store(5)
	if err := g.Get(context.Background(), "/v1.0/ping", nil); !IsGraphStatus(err, 401) {
		t.Fatalf("second 401 must be returned: %v", err)
	}
}

func TestGraphErrorCarriesCodeAndRequestIDButNoMessage(t *testing.T) {
	_, g, _ := newFakeGraph(t)
	err := g.Get(context.Background(), "/v1.0/forbidden", nil)
	var ge *GraphError
	if !errors.As(err, &ge) || ge.Status != 403 || ge.Code != "authorization_requestdenied" || ge.RequestID == "" {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), "contoso") || strings.Contains(err.Error(), "Insufficient") {
		t.Fatalf("error leaks the Graph message: %v", err)
	}
	if st := g.Status(); st.State != providerstatus.Failing || st.LastErrorCode != "http_403" {
		t.Fatalf("status %+v", st)
	}
}

func TestGraphTokenFailureDoesNotLeakSecretOrDescription(t *testing.T) {
	f, g, _ := newFakeGraph(t)
	f.tokenCode = 401
	err := g.Get(context.Background(), "/v1.0/ping", nil)
	var ae *AuthError
	if !errors.As(err, &ae) || ae.Code != "invalid_client" || !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), "s3cr3t") || strings.Contains(err.Error(), "AADSTS") {
		t.Fatalf("error leaks: %v", err)
	}
	if st := g.Status(); st.State != providerstatus.Failing || st.LastErrorCode != "auth_invalid_client" {
		t.Fatalf("status %+v", st)
	}
}

func TestGraphListFollowsNextLinkOnTheGraphHostOnly(t *testing.T) {
	_, g, _ := newFakeGraph(t)
	var ids []string
	err := g.List(context.Background(), "/v1.0/items", nil, func(raw json.RawMessage) error {
		var v struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(raw, &v)
		ids = append(ids, v.ID)
		return nil
	})
	if err != nil || strings.Join(ids, ",") != "a,b,c" {
		t.Fatalf("ids %v err %v", ids, err)
	}
	err = g.List(context.Background(), "/v1.0/evil", nil, func(json.RawMessage) error { return nil })
	if !errors.Is(err, ErrNextLinkRejected) {
		t.Fatalf("foreign nextLink must be rejected: %v", err)
	}
}

func TestGraphListCaps(t *testing.T) {
	_, g, _ := newFakeGraph(t)
	g.maxItems = 2
	err := g.List(context.Background(), "/v1.0/items", nil, func(json.RawMessage) error { return nil })
	if !errors.Is(err, ErrTooManyResults) {
		t.Fatalf("item cap: %v", err)
	}
	g.maxItems, g.maxPages = 100, 1
	err = g.List(context.Background(), "/v1.0/items", nil, func(json.RawMessage) error { return nil })
	if !errors.Is(err, ErrTooManyResults) {
		t.Fatalf("page cap: %v", err)
	}
}

func TestGraphRefusesAbsoluteTargetsOnOtherHosts(t *testing.T) {
	_, g, _ := newFakeGraph(t)
	for _, target := range []string{"https://evil.example/v1.0/x", "//evil.example/x", "http://127.0.0.1:1/x"} {
		if err := g.Get(context.Background(), target, nil); !errors.Is(err, ErrNextLinkRejected) {
			t.Errorf("%s: %v", target, err)
		}
	}
}
