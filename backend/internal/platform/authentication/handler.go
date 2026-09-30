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

type handler struct {
	sessions sessionStore
	auth     *SessionAuthenticator
	secure   bool
	logger   *slog.Logger
}

type sessionResponse struct {
	UserID      string   `json:"userId"`
	AuthMethod  string   `json:"authMethod"`
	ExpiresAt   string   `json:"expiresAt"`
	Permissions []string `json:"permissions"`
}

// Register mounts the session endpoints under /api/v1/auth.
func Register(mux *http.ServeMux, sessions sessionStore, auth *SessionAuthenticator, secureCookie bool, logger *slog.Logger) {
	h := &handler{sessions: sessions, auth: auth, secure: secureCookie, logger: logger}
	mux.HandleFunc("GET /api/v1/auth/session", h.getSession)
	// The whole API is also wrapped by RequireSameOrigin in main; keeping it
	// here makes logout safe even if mounted elsewhere.
	mux.Handle("POST /api/v1/auth/logout", RequireSameOrigin(http.HandlerFunc(h.logout)))
}

func (h *handler) getSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s, p, ok, err := h.auth.Resolve(r)
	if err != nil {
		writeInternal(h.logger, w, r, err)
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "platform.unauthenticated", "Authentication is required.")
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
	httpx.JSON(w, http.StatusOK, sessionResponse{
		UserID:      s.UserID,
		AuthMethod:  s.AuthMethod,
		ExpiresAt:   expires.UTC().Format(time.RFC3339),
		Permissions: perms,
	})
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// Logout revokes the session even if its user has since become inactive,
	// so it uses the session service directly instead of the user gate.
	if token, ok := tokenFromRequest(r, h.secure); ok {
		s, err := h.sessions.Authenticate(r.Context(), token)
		switch {
		case err == nil:
			if err := h.sessions.Revoke(r.Context(), s.ID, s.UserID, w.Header().Get("X-Request-ID")); err != nil {
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

func writeError(w http.ResponseWriter, status int, code, message string) {
	httpx.JSON(w, status, httpx.ErrorEnvelope{Error: httpx.APIError{Code: code, Message: message, RequestID: w.Header().Get("X-Request-ID")}})
}

// writeInternal logs the cause (never credentials) and answers a generic 500.
func writeInternal(logger *slog.Logger, w http.ResponseWriter, r *http.Request, err error) {
	if logger == nil {
		logger = slog.Default()
	}
	logger.ErrorContext(r.Context(), "session request failed", "request_id", w.Header().Get("X-Request-ID"), "error", err)
	writeError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
}
