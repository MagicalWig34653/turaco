// Package transport exposes the Planning HTTP API under /api/v1. Mutations
// need planning.manage; reads need planning.view|manage, or being the
// Initiative's owner or approver (anyone else gets 404). Included records of
// types the caller may not see (Changes without changes.view|manage|execute,
// Tasks without tasks.view|manage, Procurement Requests without
// procurement.view|manage, Services without services.view|manage) appear with
// placeholder ids (hidden-N) and "hidden": true. The maintenance calendar
// needs planning.view|manage or changes.view|manage|execute.
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/planning/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const maxBody = 64 << 10

type handler struct {
	svc    *application.Service
	logger *slog.Logger
}

// Register mounts the Planning routes.
func Register(mux *http.ServeMux, svc *application.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	signedIn := authorization.RequireAuthenticated(auth)
	manage := authorization.Require(auth, application.PermManage)
	route := func(pattern string, mw func(http.Handler) http.Handler, fn http.HandlerFunc) {
		mux.Handle(pattern, httpx.NoStore(mw(fn)))
	}
	// Reads are scoped by the service (ownership or permission), not by the route.
	route("GET /api/v1/initiatives", signedIn, h.list)
	route("POST /api/v1/initiatives", manage, h.create)
	route("GET /api/v1/initiatives/{id}", signedIn, h.get)
	route("PATCH /api/v1/initiatives/{id}", manage, h.update)
	route("POST /api/v1/initiatives/{id}/start-planning", manage, h.lifecycle(h.svc.StartPlanning))
	route("POST /api/v1/initiatives/{id}/propose", manage, h.propose)
	route("POST /api/v1/initiatives/{id}/replan", manage, h.withReason(h.svc.Replan))
	route("POST /api/v1/initiatives/{id}/activate", manage, h.lifecycle(h.svc.Activate))
	route("POST /api/v1/initiatives/{id}/hold", manage, h.withReason(h.svc.Hold))
	route("POST /api/v1/initiatives/{id}/resume", manage, h.lifecycle(h.svc.Resume))
	route("POST /api/v1/initiatives/{id}/complete", manage, h.lifecycle(h.svc.Complete))
	route("POST /api/v1/initiatives/{id}/cancel", manage, h.withReason(h.svc.Cancel))
	route("GET /api/v1/initiatives/{id}/transitions", signedIn, h.transitions)
	route("GET /api/v1/initiatives/{id}/items", signedIn, h.items)
	route("POST /api/v1/initiatives/{id}/items", manage, h.addItem)
	route("DELETE /api/v1/initiatives/{id}/items/{type}/{itemId}", manage, h.removeItem)
	route("POST /api/v1/initiatives/{id}/milestones", manage, h.addMilestone)
	route("PATCH /api/v1/initiatives/{id}/milestones/{milestoneId}", manage, h.updateMilestone)
	route("POST /api/v1/initiatives/{id}/milestones/{milestoneId}/complete", manage, h.milestoneOp(h.svc.CompleteMilestone))
	route("POST /api/v1/initiatives/{id}/milestones/{milestoneId}/reopen", manage, h.milestoneOp(h.svc.ReopenMilestone))
	route("POST /api/v1/initiatives/{id}/milestones/{milestoneId}/remove", manage, h.removeMilestone)
	route("GET /api/v1/maintenance-calendar", signedIn, h.calendar)
}

func (h *handler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	var tr *application.InvalidTransitionError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "planning.invalid_request", inv.Message)
	case errors.As(err, &tr):
		httpx.WriteError(w, http.StatusConflict, "planning.invalid_transition", "The operation is not allowed in the current status.")
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "planning.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "planning.version_conflict", "The record was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrReferenceInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "planning.invalid_reference", "A referenced user, change, task, procurement request or service does not exist or cannot be used.")
	case errors.Is(err, application.ErrNoEligibleApprover):
		httpx.WriteError(w, http.StatusConflict, "planning.no_eligible_approver", "The chosen approver cannot approve this initiative.")
	case errors.Is(err, application.ErrTooMany):
		httpx.WriteError(w, http.StatusConflict, "planning.limit_reached", "The limit for this initiative has been reached.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "planning.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "planning request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func principal(r *http.Request) application.Principal {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Principal{UserID: p.UserID, View: p.Has(application.PermView), Manage: p.Has(application.PermManage),
		ChangesView:     p.Has("changes.view") || p.Has("changes.manage") || p.Has("changes.execute"),
		TasksView:       p.Has("tasks.view") || p.Has("tasks.manage"),
		ProcurementView: p.Has("procurement.view") || p.Has("procurement.manage"),
		ServicesView:    p.Has("services.view") || p.Has("services.manage"),
		InfraView:       p.Has("infrastructure.view") || p.Has("infrastructure.manage")}
}

func caller(w http.ResponseWriter, r *http.Request) application.Caller {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: httpx.RequestID(w)}
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst, maxBody); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "planning.invalid_request", "The request body is not valid JSON for this operation.")
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

const dateLayout = "2006-01-02"

func date(t time.Time) string { return t.UTC().Format(dateLayout) }

func datePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := date(*t)
	return &s
}

// parseDate reads an optional YYYY-MM-DD date.
func parseDate(w http.ResponseWriter, field, raw string) (*time.Time, bool) {
	if raw == "" {
		return nil, true
	}
	t, err := time.Parse(dateLayout, raw)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "planning.invalid_request", field+" must be a date (YYYY-MM-DD).")
		return nil, false
	}
	return &t, true
}

func parseTime(w http.ResponseWriter, field, raw string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "planning.invalid_request", field+" must be an RFC 3339 timestamp.")
		return time.Time{}, false
	}
	return t, true
}

func parsePage(w http.ResponseWriter, r *http.Request) (application.Page, bool) {
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "planning.invalid_limit", "The limit must be a positive integer.")
		return application.Page{}, false
	}
	return application.Page{Limit: limit, Cursor: r.URL.Query().Get("cursor")}, true
}

// parseVersion reads the expectedVersion query parameter (a non-negative
// integer); a missing value is nil and the service refuses it.
func parseVersion(w http.ResponseWriter, raw string) (*int, bool) {
	if raw == "" {
		return nil, true
	}
	n := 0
	for _, ch := range raw {
		if ch < '0' || ch > '9' || n > 1<<30 {
			httpx.WriteError(w, http.StatusBadRequest, "planning.invalid_request", "expectedVersion must be a positive integer.")
			return nil, false
		}
		n = n*10 + int(ch-'0')
	}
	return &n, true
}

// versionBody carries the optimistic-concurrency version and a reason code.
type versionBody struct {
	ExpectedVersion *int   `json:"expectedVersion"`
	Reason          string `json:"reason"`
}
