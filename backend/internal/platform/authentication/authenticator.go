// Package authentication provides browser session authentication: the session
// cookie, the authorization.Authenticator built on it, CSRF protection and the
// session HTTP endpoints. Session persistence lives in service.go/store.go.
package authentication

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

// sessionResolver is the consumer-side view of Service.
type sessionResolver interface {
	Authenticate(ctx context.Context, token string) (Session, error)
}

// UserGate reports whether a user may currently hold a session. A session is
// only valid while its user is active.
type UserGate interface {
	IsActive(ctx context.Context, userID string) (bool, error)
}

// PermissionLoader resolves the effective permissions of a user.
type PermissionLoader interface {
	Permissions(ctx context.Context, userID string) (map[string]struct{}, error)
}

// NoPermissions grants nothing. Real permission evaluation arrives in F1
// slice 5, so authenticated sessions currently hold no permissions and
// permission-guarded routes (such as Organization) answer 403.
type NoPermissions struct{}

func (NoPermissions) Permissions(context.Context, string) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}

// SessionAuthenticator implements authorization.Authenticator with the
// session cookie.
type SessionAuthenticator struct {
	sessions    sessionResolver
	users       UserGate
	permissions PermissionLoader
	secure      bool
}

var _ authorization.Authenticator = (*SessionAuthenticator)(nil)

func NewSessionAuthenticator(sessions sessionResolver, users UserGate, permissions PermissionLoader, secureCookie bool) *SessionAuthenticator {
	return &SessionAuthenticator{sessions: sessions, users: users, permissions: permissions, secure: secureCookie}
}

// Authenticate implements authorization.Authenticator.
func (a *SessionAuthenticator) Authenticate(r *http.Request) (authorization.Principal, bool, error) {
	_, p, ok, err := a.Resolve(r)
	return p, ok, err
}

// Resolve returns the valid session and principal of the request. ok=false
// means no valid session; an error is returned only for internal failures.
func (a *SessionAuthenticator) Resolve(r *http.Request) (Session, authorization.Principal, bool, error) {
	token, ok := tokenFromRequest(r, a.secure)
	if !ok {
		return Session{}, authorization.Principal{}, false, nil
	}
	s, err := a.sessions.Authenticate(r.Context(), token)
	if errors.Is(err, ErrInvalidSession) {
		return Session{}, authorization.Principal{}, false, nil
	}
	if err != nil {
		return Session{}, authorization.Principal{}, false, fmt.Errorf("authenticate session: %w", err)
	}
	active, err := a.users.IsActive(r.Context(), s.UserID)
	if err != nil {
		return Session{}, authorization.Principal{}, false, fmt.Errorf("check user active: %w", err)
	}
	if !active {
		return Session{}, authorization.Principal{}, false, nil
	}
	perms, err := a.permissions.Permissions(r.Context(), s.UserID)
	if err != nil {
		return Session{}, authorization.Principal{}, false, fmt.Errorf("load permissions: %w", err)
	}
	return s, authorization.Principal{UserID: s.UserID, Permissions: perms}, true, nil
}

type sessionKey struct{}

// SessionFrom returns the session stored by WithSession.
func SessionFrom(ctx context.Context) (Session, bool) {
	s, ok := ctx.Value(sessionKey{}).(Session)
	return s, ok
}

// WithSession stores the request's valid session in the context for handlers
// that need the session itself. Requests without a valid session pass through
// without one; internal errors answer 500.
func WithSession(a *SessionAuthenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s, _, ok, err := a.Resolve(r)
			if err != nil {
				writeInternal(nil, w, r, err)
				return
			}
			if ok {
				r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, s))
			}
			next.ServeHTTP(w, r)
		})
	}
}
