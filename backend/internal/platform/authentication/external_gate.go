package authentication

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

// ErrorCodeNotAvailable is the API error code of a route an external account may not call. The answer is 404, the
// same as for a route that does not exist, so the allow-list reveals nothing about the application.
const ErrorCodeNotAvailable = "access.not_available"

type resolvedKey struct{}

type resolved struct {
	session   Session
	principal authorization.Principal
}

// ConfineExternal returns middleware that confines external accounts (ADR-0034 point 4) to the routes allowed
// accepts. Requests without a valid session pass through unchanged (the route answers 401 itself). For a request
// with a valid session the gate resolves the principal once and hands it down in the request context, so the
// route's own authentication does not repeat the work. allowed receives the request method and path as received;
// a path with an escaped separator never matches.
func ConfineExternal(a *SessionAuthenticator, allowed func(method, path string) bool, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
				next.ServeHTTP(w, r)
				return
			}
			if _, has := tokenFromRequest(r, a.secure); !has {
				next.ServeHTTP(w, r)
				return
			}
			s, p, ok, err := a.Resolve(r)
			if err != nil {
				writeInternal(logger, w, r, err)
				return
			}
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			if p.External && (r.URL.RawPath != "" || !allowed(r.Method, r.URL.Path)) {
				w.Header().Set("Cache-Control", "no-store")
				httpx.WriteError(w, http.StatusNotFound, ErrorCodeNotAvailable, "The requested resource was not found.")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), resolvedKey{}, resolved{session: s, principal: p})))
		})
	}
}
