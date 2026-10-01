package authentication

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

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
