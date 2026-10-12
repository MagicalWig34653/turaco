package authentication

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

// RequireSameOriginExcept is RequireSameOrigin with exact-path exemptions for
// routes that never read the session cookie (bearer-token and PKCE endpoints
// of the Turaco MCP server, F12 A-D). Such a route carries no ambient
// authority, so a cross-site request gains nothing. A path is exempt only when
// it matches exactly and is not escaped.
func RequireSameOriginExcept(paths ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		guarded := RequireSameOrigin(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, p := range paths {
				if r.URL.RawPath == "" && r.URL.Path == p {
					next.ServeHTTP(w, r)
					return
				}
			}
			guarded.ServeHTTP(w, r)
		})
	}
}

// RequireSameOrigin protects cookie-authenticated unsafe requests against
// cross-site request forgery. It fails closed: an unsafe request that carries
// neither Sec-Fetch-Site nor Origin is rejected.
func RequireSameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if !sameOrigin(r) {
			httpx.WriteError(w, http.StatusForbidden, "platform.csrf_rejected", "The request origin is not allowed.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request) bool {
	site := r.Header.Get("Sec-Fetch-Site")
	origin := r.Header.Get("Origin")
	if site == "" && origin == "" {
		return false
	}
	if site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
			return false
		}
	}
	return true
}
