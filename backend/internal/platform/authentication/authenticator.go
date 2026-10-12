// Package authentication provides browser session authentication: the session
// cookie, the authorization.Authenticator built on it, CSRF protection and the
// session HTTP endpoints. Session persistence lives in service.go/store.go.
package authentication

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

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

// AccountState is what a request needs to know about its user: whether the account may hold a session and whether
// it is a restricted external account (ADR-0034 point 4).
type AccountState struct {
	Active   bool
	External bool
}

// AccountGate is the richer UserGate: one read answers both questions. A UserGate that also implements it is used
// for the Principal's External flag; otherwise every account counts as internal.
type AccountGate interface {
	Account(ctx context.Context, userID string) (AccountState, error)
}

// PermissionLoader resolves the effective permissions of a user.
type PermissionLoader interface {
	Permissions(ctx context.Context, userID string) (map[string]struct{}, error)
}

// AccessLoader is the richer PermissionLoader: it also returns the Location-scoped holdings (ADR-0040). A
// PermissionLoader that implements it fills Principal.Scopes; otherwise the Principal has no scoped holdings.
type AccessLoader interface {
	Access(ctx context.Context, userID string) (authorization.Access, error)
}

// NoPermissions grants nothing. Production wiring uses
// authorization.RolePermissions; NoPermissions is for tests and tools that
// need authenticated sessions without any permission.
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
	// localEnabled mirrors AUTH_LOCAL_LOGIN_ENABLED: while it is off, sessions of local accounts are rejected even if
	// they exist (the switch disables local accounts completely).
	localEnabled bool
	stepUpMaxAge time.Duration
}

func (a *SessionAuthenticator) WithEntraStepUp(maxAge time.Duration) *SessionAuthenticator {
	a.stepUpMaxAge = maxAge
	return a
}

// WithLocalLogin sets whether sessions created by local-account login are accepted.
func (a *SessionAuthenticator) WithLocalLogin(enabled bool) *SessionAuthenticator {
	a.localEnabled = enabled
	return a
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
	if c, ok := r.Context().Value(resolvedKey{}).(resolved); ok {
		return c.session, c.principal, true, nil // resolved earlier in this request by ConfineExternal
	}
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
	if s.AuthMethod == methodLocal && !a.localEnabled {
		return Session{}, authorization.Principal{}, false, nil
	}
	var state AccountState
	if gate, ok := a.users.(AccountGate); ok {
		var err error
		if state, err = gate.Account(r.Context(), s.UserID); err != nil {
			return Session{}, authorization.Principal{}, false, fmt.Errorf("check user active: %w", err)
		}
	} else {
		active, err := a.users.IsActive(r.Context(), s.UserID)
		if err != nil {
			return Session{}, authorization.Principal{}, false, fmt.Errorf("check user active: %w", err)
		}
		state.Active = active
	}
	if !state.Active {
		return Session{}, authorization.Principal{}, false, nil
	}
	var access authorization.Access
	if loader, ok := a.permissions.(AccessLoader); ok {
		if access, err = loader.Access(r.Context(), s.UserID); err != nil {
			return Session{}, authorization.Principal{}, false, fmt.Errorf("load permissions: %w", err)
		}
	} else if access.Permissions, err = a.permissions.Permissions(r.Context(), s.UserID); err != nil {
		return Session{}, authorization.Principal{}, false, fmt.Errorf("load permissions: %w", err)
	}
	return s, authorization.Principal{UserID: s.UserID, Permissions: access.Permissions, Scopes: access.Scopes, SessionID: s.ID, External: state.External,
		EntraSession: s.AuthMethod == methodEntra, EntraAssuredAt: s.AssuranceAt, EntraStepUpMaxAge: a.stepUpMaxAge}, true, nil
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
