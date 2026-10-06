package authentication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func contains(s, sub string) bool { return strings.Contains(s, sub) }

type env struct {
	mux      *http.ServeMux
	sessions *fakeSessions
	logs     *bytes.Buffer
}

func newEnv(secure bool, active bool, perms map[string]struct{}) env {
	fs := &fakeSessions{sessions: map[string]Session{"tok-secret": testSession()}}
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	auth := NewSessionAuthenticator(fs, fakeGate{active: active}, fakePerms{perms: perms}, secure)
	mux := http.NewServeMux()
	Register(mux, fs, auth, nil, secure, logger)
	return env{mux, fs, logs}
}

func logoutReq(token string) *http.Request {
	r := cookieReq("POST", "/api/v1/auth/logout", token)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	return r
}

func TestGetSession(t *testing.T) {
	e := newEnv(false, true, map[string]struct{}{"b.perm": {}, "a.perm": {}})
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, cookieReq("GET", "/api/v1/auth/session", "tok-secret"))
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cc=%q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	var got sessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	s := testSession()
	if got.UserID != "u1" || got.AuthMethod != "local" || len(got.Permissions) != 2 || got.Permissions[0] != "a.perm" || got.Permissions[1] != "b.perm" {
		t.Fatalf("got %+v", got)
	}
	exp, err := time.Parse(time.RFC3339, got.ExpiresAt)
	if err != nil || !exp.Equal(s.IdleExpiresAt.Truncate(time.Second)) {
		t.Fatalf("expiresAt = %q (%v), want idle %v", got.ExpiresAt, err, s.IdleExpiresAt)
	}
	if contains(rec.Body.String(), "tok-secret") {
		t.Fatal("token in body")
	}
}

func TestGetSessionEmptyPermissionsIsArray(t *testing.T) {
	e := newEnv(false, true, map[string]struct{}{})
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, cookieReq("GET", "/api/v1/auth/session", "tok-secret"))
	if !contains(rec.Body.String(), `"permissions":[]`) {
		t.Fatalf("body = %s", rec.Body)
	}
}

func TestGetSessionExpiresAtUsesAbsoluteWhenEarlier(t *testing.T) {
	e := newEnv(false, true, nil)
	s := testSession()
	s.AbsoluteExpiresAt = s.IdleExpiresAt.Add(-10 * time.Minute)
	e.sessions.sessions["tok-secret"] = s
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, cookieReq("GET", "/api/v1/auth/session", "tok-secret"))
	var got sessionResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	exp, _ := time.Parse(time.RFC3339, got.ExpiresAt)
	if !exp.Equal(s.AbsoluteExpiresAt.Truncate(time.Second)) {
		t.Fatalf("expiresAt = %s", got.ExpiresAt)
	}
}

func TestGetSessionRejections(t *testing.T) {
	tests := []struct {
		name   string
		e      env
		req    *http.Request
		want   int
		mutate func(e env)
	}{
		{"no cookie", newEnv(false, true, nil), cookieReq("GET", "/api/v1/auth/session", ""), 401, nil},
		{"bad token", newEnv(false, true, nil), cookieReq("GET", "/api/v1/auth/session", "bad"), 401, nil},
		{"inactive user", newEnv(false, false, nil), cookieReq("GET", "/api/v1/auth/session", "tok-secret"), 401, nil},
		{"query param ignored", newEnv(false, true, nil), cookieReq("GET", "/api/v1/auth/session?turaco_session=tok-secret", ""), 401, nil},
		{"revoked", newEnv(false, true, nil), cookieReq("GET", "/api/v1/auth/session", "tok-secret"), 401, func(e env) { e.sessions.sessions = nil }},
		{"internal error", newEnv(false, true, nil), cookieReq("GET", "/api/v1/auth/session", "tok-secret"), 500, func(e env) { e.sessions.err = errors.New("db down") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.mutate != nil {
				tt.mutate(tt.e)
			}
			rec := httptest.NewRecorder()
			tt.e.mux.ServeHTTP(rec, tt.req)
			if rec.Code != tt.want || rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d cc=%q", rec.Code, rec.Header().Get("Cache-Control"))
			}
			body := rec.Body.String()
			if tt.want == 401 && !contains(body, "platform.unauthenticated") {
				t.Fatalf("body = %s", body)
			}
			if tt.want == 500 && (!contains(body, "platform.internal_error") || contains(body, "db down")) {
				t.Fatalf("body = %s", body)
			}
			if contains(body, "tok-secret") || contains(tt.e.logs.String(), "tok-secret") {
				t.Fatal("token leaked")
			}
		})
	}
}

func TestLogoutRevokesCookieSessionAndClears(t *testing.T) {
	for _, secure := range []bool{true, false} {
		e := newEnv(secure, true, nil)
		e.sessions.sessions["other"] = Session{ID: "s2", UserID: "u2"}
		rec := httptest.NewRecorder()
		rec.Header().Set("X-Request-ID", "req-1")
		req := logoutReq("tok-secret")
		req.Header.Del("Cookie")
		req.AddCookie(&http.Cookie{Name: CookieName(secure), Value: "tok-secret"})
		e.mux.ServeHTTP(rec, req)
		if rec.Code != 204 || len(e.sessions.revoked) != 1 || e.sessions.revoked[0] != "s1" {
			t.Fatalf("status=%d revoked=%v", rec.Code, e.sessions.revoked)
		}
		cs := rec.Result().Cookies()
		if len(cs) != 1 || cs[0].Name != CookieName(secure) || cs[0].MaxAge >= 0 || !cs[0].HttpOnly || cs[0].Secure != secure || cs[0].SameSite != http.SameSiteLaxMode {
			t.Fatalf("cookies = %+v", cs)
		}
		if rec.Body.Len() != 0 || contains(e.logs.String(), "tok-secret") {
			t.Fatal("unexpected body or token in logs")
		}
	}
}

func TestLogoutRevokesEvenForInactiveUser(t *testing.T) {
	e := newEnv(false, false, nil)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, logoutReq("tok-secret"))
	if rec.Code != 204 || len(e.sessions.revoked) != 1 {
		t.Fatalf("status=%d revoked=%v", rec.Code, e.sessions.revoked)
	}
}

func TestLogoutIdempotentWithoutSession(t *testing.T) {
	for _, token := range []string{"", "bad"} {
		e := newEnv(false, true, nil)
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, logoutReq(token))
		cs := rec.Result().Cookies()
		if rec.Code != 204 || len(e.sessions.revoked) != 0 || len(cs) != 1 || cs[0].MaxAge >= 0 {
			t.Fatalf("token=%q status=%d revoked=%v cookies=%+v", token, rec.Code, e.sessions.revoked, cs)
		}
	}
}

func TestLogoutInternalErrors(t *testing.T) {
	e := newEnv(false, true, nil)
	e.sessions.revErr = errors.New("db down tok-secret")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, logoutReq("tok-secret"))
	if rec.Code != 500 || contains(rec.Body.String(), "db down") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	e = newEnv(false, true, nil)
	e.sessions.err = errors.New("db down")
	rec = httptest.NewRecorder()
	e.mux.ServeHTTP(rec, logoutReq("tok-secret"))
	if rec.Code != 500 {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestLogoutCSRF(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"cross-site origin", map[string]string{"Origin": "https://evil.example"}, 403},
		{"cross-site fetch metadata", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"missing both", nil, 403},
		{"same origin", map[string]string{"Origin": "http://example.com"}, 204},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(false, true, nil)
			r := cookieReq("POST", "/api/v1/auth/logout", "tok-secret")
			for k, v := range tt.headers {
				r.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			e.mux.ServeHTTP(rec, r)
			if rec.Code != tt.want {
				t.Fatalf("status = %d", rec.Code)
			}
			if tt.want == 403 && len(e.sessions.revoked) != 0 {
				t.Fatal("revoked despite CSRF rejection")
			}
		})
	}
}

func TestLogoutRejectsGET(t *testing.T) {
	e := newEnv(false, true, nil)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, cookieReq("GET", "/api/v1/auth/logout", "tok-secret"))
	if rec.Code != http.StatusMethodNotAllowed || len(e.sessions.revoked) != 0 {
		t.Fatalf("status=%d", rec.Code)
	}
}

type fakeNames struct {
	display, given string
	err            error
}

func (f fakeNames) SessionNames(context.Context, string) (string, string, error) {
	return f.display, f.given, f.err
}

func TestGetSessionNames(t *testing.T) {
	tests := []struct {
		name               string
		names              SessionNameLoader
		wantDisplay, given string
	}{
		{"with names", fakeNames{display: "Lena Hoffmann", given: "Lena"}, "Lena Hoffmann", "Lena"},
		{"lookup error omits names", fakeNames{err: errors.New("boom")}, "", ""},
		{"no loader", nil, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeSessions{sessions: map[string]Session{"tok-secret": testSession()}}
			auth := NewSessionAuthenticator(fs, fakeGate{active: true}, fakePerms{}, false)
			mux := http.NewServeMux()
			Register(mux, fs, auth, tc.names, false, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, cookieReq("GET", "/api/v1/auth/session", "tok-secret"))
			var got sessionResponse
			if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
			}
			if got.DisplayName != tc.wantDisplay || got.GivenName != tc.given {
				t.Fatalf("got %+v", got)
			}
			if tc.wantDisplay == "" && contains(rec.Body.String(), "displayName") {
				t.Fatalf("empty name serialized: %s", rec.Body)
			}
		})
	}
}
