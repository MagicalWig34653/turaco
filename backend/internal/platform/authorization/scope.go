package authorization

import (
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/permissions"
)

// Location scope (ADR-0040, F16 design L4). Principal.Permissions and Has keep meaning "held globally"; a permission
// held only through Location-scoped role assignments appears in Principal.Scopes and is read only through ScopeOf.
// Code that has not adopted scope therefore never sees a scoped right (fail closed by construction).

// LocationScope is where a caller holds one permission: everywhere (Global) or at the listed Locations. Locations is
// the expanded set (each scoped root and all its descendants in the current Location tree), sorted and without
// duplicates. The zero value allows nothing.
type LocationScope struct {
	Global    bool
	Locations []string
}

// Allows reports whether the scope covers an object at locationID. An object without Location (empty id) is covered
// only by a global scope.
func (s LocationScope) Allows(locationID string) bool {
	if s.Global {
		return true
	}
	if locationID == "" {
		return false
	}
	_, found := slices.BinarySearch(s.Locations, locationID)
	return found
}

// Any reports whether the scope covers at least one Location.
func (s LocationScope) Any() bool { return s.Global || len(s.Locations) > 0 }

// trustedColumn is the shape of a column reference a module may pass to SQL: an identifier, optionally qualified by
// a table alias. It is never user input; the check catches programming mistakes.
var trustedColumn = regexp.MustCompile(`^[a-z_][a-z0-9_]*(\.[a-z_][a-z0-9_]*)?$`)

// SQL returns a predicate for a module's trusted query fragment with "?" placeholders (platform/query convention):
// "TRUE" for a global scope, "FALSE" for an empty one, otherwise "<column> = ANY(?::text[]::uuid[])" with the
// expanded Location ids as the one argument. A NULL column never matches, so objects without Location stay
// invisible to scoped-only callers. It panics on a column that is not a plain (optionally qualified) identifier.
func (s LocationScope) SQL(column string) (string, []any) {
	if !trustedColumn.MatchString(column) {
		panic(fmt.Sprintf("authorization: LocationScope.SQL: untrusted column %q", column))
	}
	if s.Global {
		return "TRUE", nil
	}
	if len(s.Locations) == 0 {
		return "FALSE", nil
	}
	return column + " = ANY(?::text[]::uuid[])", []any{slices.Clone(s.Locations)}
}

// ScopeOf returns where the principal holds the permission: Global when it is held globally (Has), otherwise the
// expanded Location set of the scoped assignments (possibly empty). A non-scopable permission never has a scoped
// entry, because the evaluator does not fill one.
func (p Principal) ScopeOf(permission string) LocationScope {
	if p.Has(permission) {
		return LocationScope{Global: true}
	}
	s, ok := p.Scopes[permission]
	if !ok || s.Global {
		// A Global entry in Scopes is not a global holding; only Permissions is.
		return LocationScope{}
	}
	return LocationScope{Locations: slices.Clone(s.Locations)}
}

// HasAnywhere reports whether the principal holds the permission globally or at least at one Location. It is for
// navigation-style decisions in scope-adopting code; a decision about one object uses ScopeOf(p).Allows.
func (p Principal) HasAnywhere(permission string) bool { return p.ScopeOf(permission).Any() }

// Access is a User's evaluated access: the global set (exactly what Permissions has always meant) and the scoped
// map permission -> expanded Location set for scopable permissions not held globally.
type Access struct {
	Permissions map[string]struct{}
	Scopes      map[string]LocationScope
}

// isScopable is permissions.Scopable; tests replace it to exercise RequireScoped before any permission is scopable.
var isScopable = permissions.Scopable

// RequireScoped is RequireAny for routes of a module that enforces Location scope: it admits a caller who holds one
// of the permissions globally or at least at one Location. The handler must then restrict every read and write with
// Principal.ScopeOf. It panics at construction when a permission is not registered or not scopable, so a route can
// never admit scoped holders of a permission whose module has not adopted scope.
func RequireScoped(auth Authenticator, perms ...string) func(http.Handler) http.Handler {
	if len(perms) == 0 {
		panic("authorization: RequireScoped needs at least one permission")
	}
	for _, permission := range perms {
		if !registered(permission) {
			panic(fmt.Sprintf("authorization: unregistered permission %q", permission))
		}
		if !isScopable(permission) {
			panic(fmt.Sprintf("authorization: RequireScoped with non-scopable permission %q", permission))
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
			admitted := false
			for _, permission := range perms {
				if principal.HasAnywhere(permission) {
					admitted = true
					break
				}
			}
			if !admitted {
				httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
				return
			}
			if needsStepUp(r, principal, perms) {
				httpx.WriteErrorDetails(w, http.StatusForbidden, "auth.step_up_required", "Confirm with Microsoft before this action.", map[string]any{"hintUrl": "/api/v1/auth/entra/start?stepUp=1"})
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}
}
