// Package transport exposes the Security HTTP API. All reads require security.view;
// mutations require security.manage, except accepting risk, which requires security.accept_risk.
package transport

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type handler struct {
	svc    *application.Service
	logger *slog.Logger
}

// Register mounts the Security routes under /api/v1/security.
func Register(mux *http.ServeMux, svc *application.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc, logger}
	view := authorization.Require(auth, application.PermView)
	manage := authorization.Require(auth, application.PermManage)
	accept := authorization.Require(auth, application.PermAcceptRisk)
	reopen := authorization.RequireAny(auth, application.PermManage, application.PermAcceptRisk)
	route := func(pattern string, mw func(http.Handler) http.Handler, fn http.HandlerFunc) {
		mux.Handle(pattern, httpx.NoStore(mw(fn)))
	}
	route("GET /api/v1/security/advisories", view, h.listAdvisories)
	route("POST /api/v1/security/advisories", manage, h.createAdvisory)
	route("POST /api/v1/security/advisories/import", manage, h.importAdvisories)
	route("GET /api/v1/security/advisories/{id}", view, h.getAdvisory)
	route("PATCH /api/v1/security/advisories/{id}", manage, h.updateAdvisory)
	route("PUT /api/v1/security/advisories/{id}/criteria", manage, h.editCriteria)
	route("POST /api/v1/security/advisories/{id}/normalize", manage, h.normalizeCriteria)
	route("POST /api/v1/security/advisories/{id}/start-analysis", manage, h.advisoryOperation)
	route("POST /api/v1/security/advisories/{id}/applicable", manage, h.advisoryOperation)
	route("POST /api/v1/security/advisories/{id}/not-applicable", manage, h.advisoryOperation)
	route("POST /api/v1/security/advisories/{id}/plan-remediation", manage, h.advisoryOperation)
	route("POST /api/v1/security/advisories/{id}/start-remediation", manage, h.advisoryOperation)
	route("POST /api/v1/security/advisories/{id}/resolve", manage, h.advisoryOperation)
	route("POST /api/v1/security/advisories/{id}/archive", manage, h.advisoryOperation)
	route("GET /api/v1/security/advisories/{id}/findings", view, h.advisoryFindings)
	route("GET /api/v1/security/advisories/{id}/summary", view, h.summary)
	route("GET /api/v1/security/advisories/{id}/transitions", view, h.advisoryTransitions)
	route("GET /api/v1/security/findings", view, h.listFindings)
	route("GET /api/v1/security/findings/{id}", view, h.getFinding)
	route("POST /api/v1/security/findings/{id}/investigate", manage, h.findingOperation)
	route("POST /api/v1/security/findings/{id}/accept", manage, h.findingOperation)
	route("POST /api/v1/security/findings/{id}/accept-risk", accept, h.findingOperation)
	route("POST /api/v1/security/findings/{id}/false-positive", manage, h.findingOperation)
	route("POST /api/v1/security/findings/{id}/plan-remediation", manage, h.findingOperation)
	route("POST /api/v1/security/findings/{id}/start-remediation", manage, h.findingOperation)
	route("POST /api/v1/security/findings/{id}/reopen", reopen, h.findingOperation)
	route("GET /api/v1/security/findings/{id}/transitions", view, h.findingTransitions)
}

func principal(r *http.Request) application.Principal {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Principal{UserID: p.UserID, View: p.Has(application.PermView), Manage: p.Has(application.PermManage), AcceptRisk: p.Has(application.PermAcceptRisk), EndpointsView: p.Has("endpoints.view") || p.Has("endpoints.manage")}
}
func caller(w http.ResponseWriter, r *http.Request) application.Caller {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: httpx.RequestID(w)}
}
func (h *handler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	var tr *application.InvalidTransitionError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, 400, "security.invalid_request", inv.Message)
	case errors.As(err, &tr):
		httpx.WriteError(w, 409, "security.invalid_transition", "The operation is not allowed in the current status.")
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, 404, "security.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, 403, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, 409, "security.version_conflict", "The record was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, 400, "security.invalid_cursor", "The cursor is invalid.")
	case errors.Is(err, application.ErrDuplicate):
		httpx.WriteError(w, 409, "security.duplicate", "An advisory with this source and external ID already exists.")
	case errors.Is(err, application.ErrReferenceInvalid):
		httpx.WriteError(w, 400, "security.invalid_reference", "A referenced software product does not exist.")
	default:
		h.logger.ErrorContext(r.Context(), "security request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, 500, "platform.internal_error", "An internal error occurred.")
	}
}
