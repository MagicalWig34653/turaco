// Package transport exposes the Presence HTTP API under /api/v1/presence. Responses are no-store. Authorization
// is decided in the application layer from the principal's Presence permissions; the route only requires a
// signed-in User. With the startup gate off only GET /presence/status is mounted.
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/presence/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const maxBody = 16 << 10

type handler struct {
	svc    *application.Service
	logger *slog.Logger
}

// Register mounts the Presence routes.
func Register(mux *http.ServeMux, svc *application.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	signedIn := authorization.RequireAuthenticated(auth)
	route := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, httpx.NoStore(signedIn(fn))) }
	route("GET /api/v1/presence/status", h.status)
	if !svc.ModuleEnabled() {
		return
	}
	route("GET /api/v1/presence/settings", h.getSettings)
	route("PUT /api/v1/presence/settings", h.putSettings)
	route("POST /api/v1/presence/settings/purge", h.purge)
	route("GET /api/v1/presence/me/entries", h.myEntries)
	route("POST /api/v1/presence/entries", h.create)
	route("POST /api/v1/presence/entries/{id}/reschedule", h.reschedule)
	route("POST /api/v1/presence/entries/{id}/change-location", h.changeLocation)
	route("POST /api/v1/presence/entries/{id}/change-recurrence", h.changeRecurrence)
	route("POST /api/v1/presence/entries/{id}/cancel", h.cancel)
	route("GET /api/v1/presence/entries", h.entries)
	route("GET /api/v1/presence/availability", h.availability)
	route("GET /api/v1/presence/availability/window", h.availabilityWindow)
	route("GET /api/v1/presence/teams/{id}/coverage", h.coverage)
	route("GET /api/v1/presence/teams/{id}/minimum", h.getMinimum)
	route("PUT /api/v1/presence/teams/{id}/minimum", h.putMinimum)
}

func (h *handler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	var rec *application.InvalidRecurrenceError
	var win *application.WindowError
	var tr *application.InvalidTransitionError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "presence.invalid_request", inv.Message)
	case errors.As(err, &rec):
		httpx.WriteError(w, http.StatusBadRequest, "presence.invalid_recurrence", rec.Message)
	case errors.As(err, &win):
		httpx.WriteError(w, http.StatusBadRequest, "presence.window_too_large", win.Message)
	case errors.As(err, &tr):
		httpx.WriteError(w, http.StatusConflict, "presence.invalid_transition", "The operation is not allowed in the current status.")
	case errors.Is(err, application.ErrTooManyEntries):
		httpx.WriteError(w, http.StatusUnprocessableEntity, "presence.too_many_entries", "Too many entries match; narrow the window or the selection.")
	case errors.Is(err, application.ErrDisabled):
		httpx.WriteError(w, http.StatusNotFound, "presence.disabled", "Workforce Presence is not enabled.")
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "presence.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "presence.not_permitted", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrReadOnly):
		httpx.WriteError(w, http.StatusConflict, "presence.read_only", "Entries from an external source cannot be changed here.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "presence.version_conflict", "The record was changed by someone else; reload and try again.")
	default:
		h.logger.ErrorContext(r.Context(), "presence request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func principal(r *http.Request) application.Principal {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Principal{UserID: p.UserID, ManageOwn: p.Has(application.PermManageOwn),
		ViewAvailability: p.Has(application.PermViewAvailability), ViewEntries: p.Has(application.PermViewEntries),
		ManageEntries: p.Has(application.PermManageEntries), ManageTeams: p.Has(application.PermManageTeams), Admin: p.Has(application.PermAdmin)}
}

func caller(w http.ResponseWriter, r *http.Request) application.Caller {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: httpx.RequestID(w)}
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst, maxBody); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "presence.invalid_request", "The request body is not valid JSON for this operation.")
		return false
	}
	return true
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func tsPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := ts(*t)
	return &s
}

// timeParam parses an RFC 3339 instant or a YYYY-MM-DD date (midnight UTC) from a query parameter.
func timeParam(r *http.Request, name string) (time.Time, error) {
	v := r.URL.Query().Get(name)
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.DateOnly, v); err == nil {
		return t, nil
	}
	return time.Time{}, &application.InvalidInputError{Message: name + " must be an RFC 3339 time or a date"}
}

func window(r *http.Request) (from, to time.Time, err error) {
	if from, err = timeParam(r, "from"); err != nil {
		return
	}
	to, err = timeParam(r, "to")
	return
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
