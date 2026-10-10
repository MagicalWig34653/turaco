package authentication

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const testEndSession = "https://login.microsoftonline.com/11111111-2222-3333-4444-555555555555/oauth2/v2.0/logout"

func TestEntraLogoutRedirectDependsOnModeMethodAndMarker(t *testing.T) {
	f := newLoginFixture(t)
	ctx := context.Background()
	mk := func(method string, shared bool) Session {
		tok, sess, err := f.svc.CreateLogin(ctx, LoginSession{UserID: f.userID, AuthMethod: method, CorrelationID: "corr", Locker: f.locker})
		if err != nil || tok == "" {
			t.Fatal(err)
		}
		if shared {
			if _, err := f.pool.Exec(ctx, `UPDATE platform.sessions SET shared_workstation = true WHERE id = $1::uuid`, sess.ID); err != nil {
				t.Fatal(err)
			}
		}
		return sess
	}
	entraShared, entraPlain, ldapShared := mk("entra", true), mk("entra", false), mk("ldap", true)
	for _, tc := range []struct {
		name string
		mode string
		s    Session
		want bool
	}{
		{"shared_only shared", "shared_only", entraShared, true},
		{"shared_only plain", "shared_only", entraPlain, false},
		{"default is shared_only", "", entraShared, true},
		{"default plain", "", entraPlain, false},
		{"always plain", "always", entraPlain, true},
		{"never shared", "never", entraShared, false},
		{"other method", "always", ldapShared, false},
	} {
		l := NewEntraLogout(f.pool, testEndSession, "https://turaco.example.org/login", func(context.Context) string { return tc.mode })
		got, err := l.LogoutRedirect(ctx, tc.s)
		if err != nil || (got != "") != tc.want {
			t.Errorf("%s: %q %v", tc.name, got, err)
		}
		if got != "" {
			u, _ := url.Parse(got)
			if u.Host != "login.microsoftonline.com" || u.Query().Get("post_logout_redirect_uri") != "https://turaco.example.org/login" {
				t.Errorf("%s: url %s", tc.name, got)
			}
		}
	}
}

func TestLogoutAnswersWithRedirectOnlyWhenTheHookSaysSo(t *testing.T) {
	f := newLoginFixture(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		shared   bool
		wantCode int
	}{{"shared", true, http.StatusOK}, {"plain", false, http.StatusNoContent}} {
		tok, sess, err := f.svc.CreateLogin(ctx, LoginSession{UserID: f.userID, AuthMethod: "entra", CorrelationID: "c", Locker: f.locker})
		if err != nil {
			t.Fatal(err)
		}
		if tc.shared {
			_, _ = f.pool.Exec(ctx, `UPDATE platform.sessions SET shared_workstation = true WHERE id = $1::uuid`, sess.ID)
		}
		mux := http.NewServeMux()
		Register(mux, f.svc, NewSessionAuthenticator(f.svc, nil, nil, false), nil, false, nil,
			WithLogoutRedirector(NewEntraLogout(f.pool, testEndSession, "https://turaco.example.org/login", nil)))
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/v1/auth/logout", strings.NewReader(""))
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.AddCookie(&http.Cookie{Name: CookieName(false), Value: tok})
		httpx.Middleware(slog.New(slog.NewTextHandler(io.Discard, nil)), mux).ServeHTTP(rec, req)
		if rec.Code != tc.wantCode {
			t.Fatalf("%s: %d %s", tc.name, rec.Code, rec.Body.String())
		}
		if tc.shared && !strings.Contains(rec.Body.String(), "login.microsoftonline.com") {
			t.Errorf("%s: body %s", tc.name, rec.Body.String())
		}
		if _, err := f.svc.Authenticate(ctx, tok); err == nil {
			t.Errorf("%s: the session must be revoked", tc.name)
		}
	}
}
