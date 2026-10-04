// Package transport exposes the Changes HTTP API under /api/v1. Planning and
// review operations need changes.manage; running a Change (start, complete,
// fail, tasks) needs changes.execute or being the Change's owner; reads need
// changes.view|manage|execute, or being the requester, the owner or an approver
// (anyone else gets 404). The affected resources and the impact view show
// Services only with services.view, Virtual Machines and Locations only with
// infrastructure.view and Assets only with assets.view; other records appear
// with placeholder ids (hidden-N) and "hidden": true.
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/changes/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const (
	permView        = application.PermView
	permManage      = application.PermManage
	permExecute     = application.PermExecute
	permServicesV   = "services.view"
	permServicesM   = "services.manage"
	permInfraView   = "infrastructure.view"
	permInfraManage = "infrastructure.manage"
	permAssetsView  = "assets.view"
	permAssetsMan   = "assets.manage"
	maxBody         = 64 << 10
)

type handler struct {
	svc    *application.Service
	logger *slog.Logger
}

// Register mounts the Changes routes.
func Register(mux *http.ServeMux, svc *application.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	signedIn := authorization.RequireAuthenticated(auth)
	manage := authorization.Require(auth, permManage)
	route := func(pattern string, mw func(http.Handler) http.Handler, fn http.HandlerFunc) {
		mux.Handle(pattern, httpx.NoStore(mw(fn)))
	}
	// Reads and execution are scoped by the service (ownership or permission), not by the route.
	route("GET /api/v1/changes", signedIn, h.list)
	route("POST /api/v1/changes", manage, h.create)
	route("GET /api/v1/changes/{id}", signedIn, h.get)
	route("PATCH /api/v1/changes/{id}", manage, h.update)
	route("POST /api/v1/changes/{id}/affected", manage, h.addAffected)
	route("DELETE /api/v1/changes/{id}/affected/{type}/{resourceId}", manage, h.removeAffected)
	route("POST /api/v1/changes/{id}/submit", manage, h.submit)
	route("POST /api/v1/changes/{id}/assess", manage, h.assess)
	route("POST /api/v1/changes/{id}/schedule", manage, h.schedule)
	route("POST /api/v1/changes/{id}/start", signedIn, h.start)
	route("POST /api/v1/changes/{id}/complete", signedIn, h.complete)
	route("POST /api/v1/changes/{id}/fail", signedIn, h.failChange)
	route("POST /api/v1/changes/{id}/review", manage, h.review)
	route("POST /api/v1/changes/{id}/close", manage, h.closeChange)
	route("POST /api/v1/changes/{id}/cancel", manage, h.cancel)
	route("POST /api/v1/changes/{id}/tasks", signedIn, h.addTask)
	route("GET /api/v1/changes/{id}/impact", signedIn, h.impact)
	route("GET /api/v1/changes/{id}/transitions", signedIn, h.transitions)
}

func (h *handler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	var tr *application.InvalidTransitionError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "changes.invalid_request", inv.Message)
	case errors.As(err, &tr):
		httpx.WriteError(w, http.StatusConflict, "changes.invalid_transition", "The operation is not allowed in the current status.")
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "changes.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "changes.version_conflict", "The record was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrReferenceInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "changes.invalid_reference", "A referenced user, service, virtual machine, asset or location does not exist or cannot be used.")
	case errors.Is(err, application.ErrNoEligibleApprover):
		httpx.WriteError(w, http.StatusConflict, "changes.no_eligible_approver", "The chosen approver cannot approve this change.")
	case errors.Is(err, application.ErrReviewRequired):
		httpx.WriteError(w, http.StatusConflict, "changes.review_required", "An emergency change needs a review before it can be closed.")
	case errors.Is(err, application.ErrOpenTasks):
		httpx.WriteError(w, http.StatusConflict, "changes.tasks_open", "Execution tasks are still open.")
	case errors.Is(err, application.ErrTooMany):
		httpx.WriteError(w, http.StatusConflict, "changes.limit_reached", "The limit for this change has been reached.")
	case errors.Is(err, application.ErrSeparationOfDuties):
		httpx.WriteError(w, http.StatusForbidden, "changes.separation_of_duties", "Separation of duties does not allow you to take this step on this change.")
	case errors.Is(err, application.ErrWindowNotApproved):
		httpx.WriteError(w, http.StatusConflict, "changes.window_not_approved", "The maintenance window lies outside the approved window.")
	case errors.Is(err, application.ErrImpactBusy):
		httpx.WriteError(w, http.StatusTooManyRequests, "changes.impact_busy", "An impact view is already being calculated for you; try again when it has finished.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "changes.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "changes request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func principal(r *http.Request) application.Principal {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Principal{UserID: p.UserID, View: p.Has(permView), Manage: p.Has(permManage), Execute: p.Has(permExecute),
		ServicesView: p.Has(permServicesV) || p.Has(permServicesM),
		InfraView:    p.Has(permInfraView) || p.Has(permInfraManage),
		AssetsView:   p.Has(permAssetsView) || p.Has(permAssetsMan)}
}

func caller(w http.ResponseWriter, r *http.Request) application.Caller {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: httpx.RequestID(w)}
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst, maxBody); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "changes.invalid_request", "The request body is not valid JSON for this operation.")
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

func parseTime(w http.ResponseWriter, raw string) (*time.Time, bool) {
	if raw == "" {
		return nil, true
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "changes.invalid_request", "Times must be RFC 3339 timestamps.")
		return nil, false
	}
	return &t, true
}

func parsePage(w http.ResponseWriter, r *http.Request) (application.Page, bool) {
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "changes.invalid_limit", "The limit must be a positive integer.")
		return application.Page{}, false
	}
	return application.Page{Limit: limit, Cursor: r.URL.Query().Get("cursor")}.Normalize(), true
}

// parseVersion reads the optional expectedVersion query parameter (a
// non-negative integer); a missing value is nil and the service refuses it
// where the version is required.
func parseVersion(w http.ResponseWriter, raw string) (*int, bool) {
	if raw == "" {
		return nil, true
	}
	n := 0
	for _, ch := range raw {
		if ch < '0' || ch > '9' || n > 1<<30 {
			httpx.WriteError(w, http.StatusBadRequest, "changes.invalid_request", "expectedVersion must be a positive integer.")
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
