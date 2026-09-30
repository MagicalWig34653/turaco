// Package authorization provides the request principal and permission checks.
//
// Authentication providers are wired in by later F1 slices. Until an
// Authenticator is configured, DenyAll rejects every request (default deny).
package authorization

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/permissions"
)

// Principal is the authenticated caller with its effective permissions.
type Principal struct {
	UserID      string
	Permissions map[string]struct{}
}

// Has reports whether the principal holds the named permission.
func (p Principal) Has(permission string) bool {
	_, ok := p.Permissions[permission]
	return ok
}

// Authenticator resolves the caller of a request. It returns ok=false when the
// request carries no valid credentials and an error only for internal failures.
type Authenticator interface {
	Authenticate(r *http.Request) (principal Principal, ok bool, err error)
}

// DenyAll is the default Authenticator: no request is authenticated.
type DenyAll struct{}

func (DenyAll) Authenticate(*http.Request) (Principal, bool, error) { return Principal{}, false, nil }

type contextKey struct{}

// WithPrincipal stores the principal in the context.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}

// PrincipalFrom returns the principal stored by Require.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(contextKey{}).(Principal)
	return p, ok
}

// Require returns middleware that authenticates the request and enforces the
// named permission. It panics at construction time if the permission is not
// registered, so typos fail at startup instead of silently denying or allowing.
func Require(auth Authenticator, permission string) func(http.Handler) http.Handler {
	if !registered(permission) {
		panic(fmt.Sprintf("authorization: unregistered permission %q", permission))
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok, err := auth.Authenticate(r)
			if err != nil {
				slog.ErrorContext(r.Context(), "authentication failed", "request_id", w.Header().Get("X-Request-ID"), "error", err)
				httpx.JSON(w, http.StatusInternalServerError, httpx.ErrorEnvelope{Error: httpx.APIError{Code: "platform.internal_error", Message: "An internal error occurred.", RequestID: w.Header().Get("X-Request-ID")}})
				return
			}
			if !ok {
				httpx.JSON(w, http.StatusUnauthorized, httpx.ErrorEnvelope{Error: httpx.APIError{Code: "platform.unauthenticated", Message: "Authentication is required.", RequestID: w.Header().Get("X-Request-ID")}})
				return
			}
			if !principal.Has(permission) {
				httpx.JSON(w, http.StatusForbidden, httpx.ErrorEnvelope{Error: httpx.APIError{Code: "platform.forbidden", Message: "You do not have permission to perform this action.", RequestID: w.Header().Get("X-Request-ID")}})
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}
}

func registered(name string) bool {
	for _, p := range permissions.Registry {
		if p.Name == name {
			return true
		}
	}
	return false
}
