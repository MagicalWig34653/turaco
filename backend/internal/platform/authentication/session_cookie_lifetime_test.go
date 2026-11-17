package authentication

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// The browser must retain an active session beyond its initial idle deadline;
// the service still rejects inactivity even while that cookie remains present.
func assertSessionCookieLifetime(t *testing.T, f *loginFixture, rec loginResponse, method string) {
	t.Helper()
	ctx := context.Background()
	token := rec.cookieToken(false)
	sess, err := f.svc.Authenticate(ctx, token)
	if err != nil || sess.AuthMethod != method {
		t.Fatalf("session=%+v error=%v", sess, err)
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName(false) {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("session cookie missing")
	}
	if !cookie.Expires.Equal(sess.AbsoluteExpiresAt.Truncate(time.Second)) {
		t.Errorf("cookie expires at %v, want absolute deadline %v (initial idle: %v)", cookie.Expires, sess.AbsoluteExpiresAt, sess.IdleExpiresAt)
	}
	wantAge := int(time.Until(sess.AbsoluteExpiresAt).Seconds())
	if cookie.MaxAge < wantAge-2 || cookie.MaxAge > wantAge+2 {
		t.Errorf("cookie MaxAge=%d, want approximately %d", cookie.MaxAge, wantAge)
	}
	now := sess.CreatedAt.Add(29 * time.Minute)
	f.svc.now = func() time.Time { return now }
	if _, err := f.svc.Authenticate(ctx, token); err != nil {
		t.Fatalf("active before initial idle: %v", err)
	}
	now = sess.CreatedAt.Add(31 * time.Minute)
	if _, err := f.svc.Authenticate(ctx, token); err != nil {
		t.Fatalf("active beyond initial idle: %v", err)
	}
	now = sess.CreatedAt.Add(62 * time.Minute)
	if !cookie.Expires.After(now) {
		t.Error("browser discarded cookie before absolute deadline")
	}
	if _, err := f.svc.Authenticate(ctx, token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("idle session accepted with retained cookie: %v", err)
	}
}

func TestLocalLoginCookieUsesAbsoluteLifetime(t *testing.T) {
	f := newLocalFixture(t, true)
	f.activate()
	rec := f.localLogin(f.email, goodLocalPassword)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	assertSessionCookieLifetime(t, f.loginFixture, rec, "local-account")
}

func TestDirectoryLoginCookieUsesAbsoluteLifetime(t *testing.T) {
	f := newLoginFixture(t)
	rec := f.login(f.ident, loginPassword)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	assertSessionCookieLifetime(t, f, rec, "ldap")
}

func TestEntraLoginCookieUsesAbsoluteLifetime(t *testing.T) {
	f := newEntraFixture(t, &fakeOIDC{identity: VerifiedIdentity{TenantID: entraTenant, ObjectID: entraObject}})
	state, _, binding := f.startEntra("/")
	rec := f.callback(state, "the-code", binding)
	if rec.Code != http.StatusFound {
		t.Fatalf("callback: %d %s", rec.Code, rec.Body)
	}
	assertSessionCookieLifetime(t, f, rec, "entra")
}
