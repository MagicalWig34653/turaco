package authentication

import (
	"net/http"
	"time"
)

// CookieName returns the session cookie name. The opaque session token is only
// ever accepted from this cookie, never from query strings or other headers.
// Secure deployments use the __Host- prefix, which makes browsers enforce
// Secure, Path=/ and no Domain attribute, so a sibling subdomain cannot toss a
// cookie that shadows the real session. Plain-HTTP local development cannot
// use the prefix.
func CookieName(secure bool) string {
	if secure {
		return "__Host-turaco_session"
	}
	return "turaco_session"
}

// SetCookie writes the session cookie. It is HttpOnly, SameSite=Lax and
// scoped to the whole site; Secure follows the deployment flag.
func SetCookie(w http.ResponseWriter, token string, expires time.Time, secure bool) {
	maxAge := int(time.Until(expires).Seconds())
	if maxAge <= 0 {
		// An already expired session must not produce a persistent cookie.
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName(secure),
		Value:    token,
		Path:     "/",
		Expires:  expires.UTC(),
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearCookie instructs the browser to delete the session cookie.
func ClearCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName(secure),
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0).UTC(),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// tokenFromRequest reads the session token from the cookie only.
// Several cookies with the session name are ambiguous (possible cookie
// tossing), so the request is treated as unauthenticated.
func tokenFromRequest(r *http.Request, secure bool) (string, bool) {
	name := CookieName(secure)
	var value string
	n := 0
	for _, c := range r.Cookies() {
		if c.Name == name {
			n++
			value = c.Value
		}
	}
	if n != 1 || value == "" {
		return "", false
	}
	return value, true
}
