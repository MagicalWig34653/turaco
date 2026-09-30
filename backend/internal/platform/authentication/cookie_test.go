package authentication

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSetCookieAttributes(t *testing.T) {
	for _, secure := range []bool{true, false} {
		rec := httptest.NewRecorder()
		SetCookie(rec, "tok", time.Now().Add(time.Hour), secure)
		c := rec.Result().Cookies()[0]
		if c.Name != CookieName(secure) || c.Value != "tok" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Secure != secure || c.MaxAge <= 0 || c.Expires.IsZero() {
			t.Fatalf("secure=%v cookie = %+v", secure, c)
		}
	}
}

func TestSetCookieExpiredIsNotPersistent(t *testing.T) {
	rec := httptest.NewRecorder()
	SetCookie(rec, "tok", time.Now().Add(-time.Minute), true)
	if c := rec.Result().Cookies()[0]; c.MaxAge >= 0 {
		t.Fatalf("MaxAge = %d", c.MaxAge)
	}
}

func TestClearCookie(t *testing.T) {
	for _, secure := range []bool{true, false} {
		rec := httptest.NewRecorder()
		ClearCookie(rec, secure)
		c := rec.Result().Cookies()[0]
		if c.Name != CookieName(secure) || c.Value != "" || c.MaxAge >= 0 || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Secure != secure {
			t.Fatalf("secure=%v cookie = %+v", secure, c)
		}
	}
}

func TestSecureCookieNameHasHostPrefix(t *testing.T) {
	if CookieName(true) != "__Host-turaco_session" || CookieName(false) != "turaco_session" {
		t.Fatalf("names = %q, %q", CookieName(true), CookieName(false))
	}
}

func TestTokenFromRequestRejectsAmbiguousOrWrongCookies(t *testing.T) {
	tests := []struct {
		name    string
		cookies []*http.Cookie
		secure  bool
		want    bool
	}{
		{"single", []*http.Cookie{{Name: CookieName(true), Value: "a"}}, true, true},
		{"duplicate is ambiguous", []*http.Cookie{{Name: CookieName(true), Value: "a"}, {Name: CookieName(true), Value: "b"}}, true, false},
		{"plain name ignored in secure mode", []*http.Cookie{{Name: CookieName(false), Value: "a"}}, true, false},
		{"empty value", []*http.Cookie{{Name: CookieName(false), Value: ""}}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			for _, c := range tt.cookies {
				r.AddCookie(c)
			}
			if _, ok := tokenFromRequest(r, tt.secure); ok != tt.want {
				t.Fatalf("ok = %v, want %v", ok, tt.want)
			}
		})
	}
}
