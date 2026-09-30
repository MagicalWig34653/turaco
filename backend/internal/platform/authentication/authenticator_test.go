package authentication

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

type fakeSessions struct {
	sessions map[string]Session // token -> session
	err      error
	revoked  []string
	revErr   error
}

func (f *fakeSessions) Authenticate(_ context.Context, token string) (Session, error) {
	if f.err != nil {
		return Session{}, f.err
	}
	s, ok := f.sessions[token]
	if !ok {
		return Session{}, ErrInvalidSession
	}
	return s, nil
}

func (f *fakeSessions) Revoke(_ context.Context, sessionID, _, _ string) error {
	f.revoked = append(f.revoked, sessionID)
	return f.revErr
}

type fakeGate struct {
	active bool
	err    error
}

func (f fakeGate) IsActive(context.Context, string) (bool, error) { return f.active, f.err }

type fakePerms struct {
	perms map[string]struct{}
	err   error
}

func (f fakePerms) Permissions(context.Context, string) (map[string]struct{}, error) {
	return f.perms, f.err
}

func testSession() Session {
	now := time.Now()
	return Session{ID: "s1", UserID: "u1", AuthMethod: "local", CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(8 * time.Hour)}
}

func cookieReq(method, target, token string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	if token != "" {
		r.AddCookie(&http.Cookie{Name: CookieName(false), Value: token})
	}
	return r
}

func TestSessionAuthenticator(t *testing.T) {
	boom := errors.New("boom")
	perms := map[string]struct{}{"organization.view": {}}
	tests := []struct {
		name     string
		req      *http.Request
		sessions *fakeSessions
		gate     fakeGate
		perms    fakePerms
		wantOK   bool
		wantErr  bool
	}{
		{"no cookie", cookieReq("GET", "/", ""), &fakeSessions{sessions: map[string]Session{"tok": testSession()}}, fakeGate{active: true}, fakePerms{perms: perms}, false, false},
		{"bad token", cookieReq("GET", "/", "nope"), &fakeSessions{sessions: map[string]Session{"tok": testSession()}}, fakeGate{active: true}, fakePerms{perms: perms}, false, false},
		{"expired or revoked", cookieReq("GET", "/", "tok"), &fakeSessions{err: ErrInvalidSession}, fakeGate{active: true}, fakePerms{perms: perms}, false, false},
		{"inactive user", cookieReq("GET", "/", "tok"), &fakeSessions{sessions: map[string]Session{"tok": testSession()}}, fakeGate{active: false}, fakePerms{perms: perms}, false, false},
		{"session error", cookieReq("GET", "/", "tok"), &fakeSessions{err: boom}, fakeGate{active: true}, fakePerms{perms: perms}, false, true},
		{"gate error", cookieReq("GET", "/", "tok"), &fakeSessions{sessions: map[string]Session{"tok": testSession()}}, fakeGate{err: boom}, fakePerms{perms: perms}, false, true},
		{"perms error", cookieReq("GET", "/", "tok"), &fakeSessions{sessions: map[string]Session{"tok": testSession()}}, fakeGate{active: true}, fakePerms{err: boom}, false, true},
		{"active", cookieReq("GET", "/", "tok"), &fakeSessions{sessions: map[string]Session{"tok": testSession()}}, fakeGate{active: true}, fakePerms{perms: perms}, true, false},
		{"token in query ignored", cookieReq("GET", "/?turaco_session=tok", ""), &fakeSessions{sessions: map[string]Session{"tok": testSession()}}, fakeGate{active: true}, fakePerms{perms: perms}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewSessionAuthenticator(tt.sessions, tt.gate, tt.perms, false)
			p, ok, err := a.Authenticate(tt.req)
			if ok != tt.wantOK || (err != nil) != tt.wantErr {
				t.Fatalf("ok=%v err=%v", ok, err)
			}
			if ok && (p.UserID != "u1" || !p.Has("organization.view")) {
				t.Fatalf("principal = %+v", p)
			}
			if !ok && (p.UserID != "" || p.Permissions != nil) {
				t.Fatalf("principal must be zero: %+v", p)
			}
		})
	}
}

func TestTokenIgnoredInHeaders(t *testing.T) {
	a := NewSessionAuthenticator(&fakeSessions{sessions: map[string]Session{"tok": testSession()}}, fakeGate{active: true}, NoPermissions{}, false)
	for _, h := range []string{"Authorization", "X-Session-Token", "X-Turaco-Session"} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set(h, "tok")
		r.Header.Set(h, "Bearer tok")
		if _, ok, _ := a.Authenticate(r); ok {
			t.Fatalf("header %s accepted", h)
		}
	}
}

func TestRequireWithSessionAuthenticator(t *testing.T) {
	tests := []struct {
		name     string
		sessions *fakeSessions
		gate     fakeGate
		perms    fakePerms
		want     int
	}{
		{"unauthenticated", &fakeSessions{err: ErrInvalidSession}, fakeGate{active: true}, fakePerms{}, 401},
		{"internal error", &fakeSessions{err: errors.New("db password=secret")}, fakeGate{active: true}, fakePerms{}, 500},
		{"no permissions", &fakeSessions{sessions: map[string]Session{"tok": testSession()}}, fakeGate{active: true}, fakePerms{perms: map[string]struct{}{}}, 403},
		{"allowed", &fakeSessions{sessions: map[string]Session{"tok": testSession()}}, fakeGate{active: true}, fakePerms{perms: map[string]struct{}{"organization.view": {}}}, 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewSessionAuthenticator(tt.sessions, tt.gate, tt.perms, false)
			h := authorization.Require(a, "organization.view")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, cookieReq("GET", "/", "tok"))
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if tt.want == 500 && (contains(rec.Body.String(), "secret") || contains(rec.Body.String(), "tok\"")) {
				t.Fatalf("leak: %s", rec.Body)
			}
		})
	}
}

func TestWithSession(t *testing.T) {
	a := NewSessionAuthenticator(&fakeSessions{sessions: map[string]Session{"tok": testSession()}}, fakeGate{active: true}, NoPermissions{}, false)
	var got Session
	var found bool
	h := WithSession(a)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got, found = SessionFrom(r.Context()) }))
	h.ServeHTTP(httptest.NewRecorder(), cookieReq("GET", "/", "tok"))
	if !found || got.ID != "s1" {
		t.Fatalf("session = %+v, %v", got, found)
	}
	found = false
	h.ServeHTTP(httptest.NewRecorder(), cookieReq("GET", "/", "bad"))
	if found {
		t.Fatal("session set for invalid token")
	}
	bad := NewSessionAuthenticator(&fakeSessions{err: errors.New("x")}, fakeGate{active: true}, NoPermissions{}, false)
	rec := httptest.NewRecorder()
	WithSession(bad)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("called") })).ServeHTTP(rec, cookieReq("GET", "/", "tok"))
	if rec.Code != 500 {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestNoPermissions(t *testing.T) {
	p, err := NoPermissions{}.Permissions(context.Background(), "u")
	if err != nil || p == nil || len(p) != 0 {
		t.Fatalf("%v %v", p, err)
	}
}
