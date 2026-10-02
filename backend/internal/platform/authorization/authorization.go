// Package authorization provides the request contract: the authenticated
// Principal, the Authenticator that resolves it and the Require middleware.
// Roles, assignments and the evaluation of effective permissions live in
// authorization/roles; this package must not depend on them.
//
// Until an Authenticator is configured, DenyAll rejects every request (default deny).
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

// HasAny reports whether the principal holds at least one of the permissions.
func (p Principal) HasAny(permissions ...string) bool {
	for _, permission := range permissions {
		if p.Has(permission) {
			return true
		}
	}
	return false
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
	return RequireAny(auth, permission)
}

// RequireAny is Require for routes that several permissions open, for example
// a view that is scoped differently per permission; the handler then decides
// what each permission may see. At least one permission is required and all
// must be registered.
func RequireAny(auth Authenticator, permissions ...string) func(http.Handler) http.Handler {
	if len(permissions) == 0 {
		panic("authorization: RequireAny needs at least one permission")
	}
	for _, permission := range permissions {
		if !registered(permission) {
			panic(fmt.Sprintf("authorization: unregistered permission %q", permission))
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok, err := auth.Authenticate(r)
			if err != nil {
				slog.ErrorContext(r.Context(), "authentication failed", "request_id", httpx.RequestID(w), "error", err)
				httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
				return
			}
			if !ok {
				httpx.WriteError(w, http.StatusUnauthorized, "platform.unauthenticated", "Authentication is required.")
				return
			}
			if !principal.HasAny(permissions...) {
				httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
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
