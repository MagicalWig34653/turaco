// Package transport exposes the Remote Access HTTP API under /api/v1/remote-access. Every response is
// no-store. Starting a session needs remote_access.start_attended; the launch link appears only in the response
// of POST /launch-handles/exchange (body only, once, never in a URL or header). Reads are scoped by the service:
// initiators see their own sessions, remote_access.view_sessions holders all of them.
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/remoteaccess/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const maxBody = 16 << 10

type handler struct {
	svc    *application.Service
	logger *slog.Logger
}

// Register mounts the Remote Access routes.
func Register(mux *http.ServeMux, svc *application.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	signedIn := authorization.RequireAuthenticated(auth)
	start := authorization.Require(auth, application.PermStart)
	admin := authorization.Require(auth, application.PermAdmin)
	viewSessions := authorization.Require(auth, application.PermViewSessions)
	route := func(pattern string, mw func(http.Handler) http.Handler, fn http.HandlerFunc) {
		mux.Handle(pattern, httpx.NoStore(mw(fn)))
	}
	route("GET /api/v1/remote-access/capabilities", signedIn, h.capabilities)
	route("GET /api/v1/remote-access/sessions", signedIn, h.list)
	route("POST /api/v1/remote-access/sessions", start, h.request)
	route("GET /api/v1/remote-access/sessions/{id}", signedIn, h.get)
	route("POST /api/v1/remote-access/sessions/{id}/launch", start, h.launch)
	route("POST /api/v1/remote-access/sessions/{id}/consent", start, h.consent)
	route("POST /api/v1/remote-access/sessions/{id}/close", signedIn, h.withReason(h.svc.Close))
	route("POST /api/v1/remote-access/sessions/{id}/cancel", signedIn, h.withReason(h.svc.Cancel))
	route("POST /api/v1/remote-access/launch-handles/exchange", start, h.exchange)
	route("GET /api/v1/remote-access/observations/summary", viewSessions, h.observationSummary)
	route("GET /api/v1/remote-access/peer-mappings", signedIn, h.mappings)
	route("PUT /api/v1/remote-access/peer-mappings", admin, h.mapPeer)
	route("DELETE /api/v1/remote-access/peer-mappings", admin, h.unmapPeer)
}

func (h *handler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	var tr *application.InvalidTransitionError
	var ref *application.RefusedError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "remoteaccess.invalid_request", inv.Message)
	case errors.As(err, &tr):
		httpx.WriteError(w, http.StatusConflict, "remoteaccess.invalid_transition", "The operation is not allowed in the current status.")
	case errors.As(err, &ref):
		httpx.WriteError(w, http.StatusConflict, "remoteaccess."+ref.Code, "A remote access policy refuses this request: "+ref.Code+".")
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "remoteaccess.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "remoteaccess.version_conflict", "The record was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrRateLimited):
		httpx.WriteError(w, http.StatusTooManyRequests, "remoteaccess.rate_limited", "Too many remote access requests; try again later.")
	case errors.Is(err, application.ErrHandleUsed):
		httpx.WriteError(w, http.StatusConflict, "remoteaccess.handle_used", "The launch handle was already used.")
	case errors.Is(err, application.ErrHandleExpired):
		httpx.WriteError(w, http.StatusGone, "remoteaccess.handle_expired", "The launch handle or the session expired; request a new launch.")
	case errors.Is(err, application.ErrHandleActive):
		httpx.WriteError(w, http.StatusConflict, "remoteaccess.handle_active", "A launch handle for this session is still valid.")
	case errors.Is(err, application.ErrConsentRecorded):
		httpx.WriteError(w, http.StatusConflict, "remoteaccess.consent_recorded", "Consent was already recorded for this session.")
	case errors.Is(err, application.ErrPeerTaken):
		httpx.WriteError(w, http.StatusConflict, "remoteaccess.peer_taken", "The peer id is already mapped to another device.")
	case errors.Is(err, application.ErrNoEligibleApprover):
		httpx.WriteError(w, http.StatusConflict, "remoteaccess.no_eligible_approver", "The chosen approver cannot approve this session.")
	case errors.Is(err, application.ErrLaunchFailed):
		httpx.WriteError(w, http.StatusBadGateway, "remoteaccess.launch_failed", "The provider could not prepare the launch; the session was ended.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "remoteaccess.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "remoteaccess request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func principal(r *http.Request) application.Principal {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Principal{UserID: p.UserID, View: p.Has(application.PermView), Start: p.Has(application.PermStart),
		ViewSessions: p.Has(application.PermViewSessions), Admin: p.Has(application.PermAdmin)}
}

func caller(w http.ResponseWriter, r *http.Request) application.Caller {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: httpx.RequestID(w)}
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst, maxBody); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "remoteaccess.invalid_request", "The request body is not valid JSON for this operation.")
		return false
	}
	return true
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func tsPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := ts(*t)
	return &s
}
