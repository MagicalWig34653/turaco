package authentication

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

// sessionStore is the consumer-side view of Service used by the endpoints.
type sessionStore interface {
	sessionResolver
	Revoke(ctx context.Context, sessionID, actorID, correlationID string) error
}

// SessionNameLoader returns the session user's own display and given name.
// Names are presentation only; they never affect authentication.
type SessionNameLoader interface {
	SessionNames(ctx context.Context, userID string) (displayName, givenName string, err error)
}

type handler struct {
	sessions sessionStore
	auth     *SessionAuthenticator
	names    SessionNameLoader
	secure   bool
	logger   *slog.Logger
}

type sessionResponse struct {
	UserID      string   `json:"userId"`
	AuthMethod  string   `json:"authMethod"`
	ExpiresAt   string   `json:"expiresAt"`
	Permissions []string `json:"permissions"`
	DisplayName string   `json:"displayName,omitempty"`
	GivenName   string   `json:"givenName,omitempty"`
}

// Register mounts the session endpoints under /api/v1/auth. names may be nil;
// the session response then carries no names.
func Register(mux *http.ServeMux, sessions sessionStore, auth *SessionAuthenticator, names SessionNameLoader, secureCookie bool, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{sessions: sessions, auth: auth, names: names, secure: secureCookie, logger: logger}
	mux.Handle("GET /api/v1/auth/session", httpx.NoStore(http.HandlerFunc(h.getSession)))
	// The whole API is also wrapped by RequireSameOrigin in main; keeping it
	// here makes logout safe even if mounted elsewhere.
	mux.Handle("POST /api/v1/auth/logout", httpx.NoStore(RequireSameOrigin(http.HandlerFunc(h.logout))))
}

func (h *handler) getSession(w http.ResponseWriter, r *http.Request) {
	s, p, ok, err := h.auth.Resolve(r)
	if err != nil {
		writeInternal(h.logger, w, r, err)
		return
	}
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "platform.unauthenticated", "Authentication is required.")
		return
	}
	expires := s.IdleExpiresAt
	if s.AbsoluteExpiresAt.Before(expires) {
		expires = s.AbsoluteExpiresAt
	}
	perms := make([]string, 0, len(p.Permissions))
	for name := range p.Permissions {
		perms = append(perms, name)
	}
	sort.Strings(perms)
	resp := sessionResponse{
		UserID:      s.UserID,
		AuthMethod:  s.AuthMethod,
		ExpiresAt:   expires.UTC().Format(time.RFC3339),
		Permissions: perms,
	}
	if h.names != nil {
		// A name lookup failure must not break the session: the UI falls back
		// to a neutral greeting.
		display, given, err := h.names.SessionNames(r.Context(), s.UserID)
		if err != nil {
			h.logger.WarnContext(r.Context(), "session names unavailable", "request_id", httpx.RequestID(w), "error", err)
		} else {
			resp.DisplayName, resp.GivenName = display, given
		}
	}
	httpx.JSON(w, http.StatusOK, resp)
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	// Logout revokes the session even if its user has since become inactive,
	// so it uses the session service directly instead of the user gate.
	if token, ok := tokenFromRequest(r, h.secure); ok {
		s, err := h.sessions.Authenticate(r.Context(), token)
		switch {
		case err == nil:
			if err := h.sessions.Revoke(r.Context(), s.ID, s.UserID, httpx.RequestID(w)); err != nil {
				writeInternal(h.logger, w, r, err)
				return
			}
		case errors.Is(err, ErrInvalidSession):
			// Already invalid: nothing to revoke.
		default:
			writeInternal(h.logger, w, r, err)
			return
		}
	}
	ClearCookie(w, h.secure)
	w.WriteHeader(http.StatusNoContent)
}

// writeInternal logs the cause (never credentials) and answers a generic 500.
func writeInternal(logger *slog.Logger, w http.ResponseWriter, r *http.Request, err error) {
	if logger == nil {
		logger = slog.Default()
	}
	logger.ErrorContext(r.Context(), "session request failed", "request_id", httpx.RequestID(w), "error", err)
	httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
}
