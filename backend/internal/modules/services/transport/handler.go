// Package transport exposes the Services HTTP API under /api/v1. Reads need
// services.view or services.manage, writes services.manage. The impact view
// shows Virtual Machine and Location names only with infrastructure.view and
// Asset references only with assets.view.
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/services/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const (
	permView        = "services.view"
	permManage      = "services.manage"
	permInfraView   = "infrastructure.view"
	permInfraManage = "infrastructure.manage"
	permAssetsView  = "assets.view"
	permAssetsMan   = "assets.manage"
	maxBody         = 64 << 10
)

type handler struct {
	app    *application.App
	logger *slog.Logger
}

// Register mounts the Services routes.
func Register(mux *http.ServeMux, app *application.App, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{app: app, logger: logger}
	read := authorization.RequireAny(auth, permView, permManage)
	write := authorization.Require(auth, permManage)
	route := func(pattern string, mw func(http.Handler) http.Handler, fn http.HandlerFunc) {
		mux.Handle(pattern, httpx.NoStore(mw(fn)))
	}
	route("GET /api/v1/services", read, h.list)
	route("POST /api/v1/services", write, h.create)
	route("GET /api/v1/services/{id}", read, h.get)
	route("PATCH /api/v1/services/{id}", write, h.update)
	route("POST /api/v1/services/{id}/status", write, h.status)
	route("POST /api/v1/services/{id}/retire", write, h.retire)
	route("POST /api/v1/services/{id}/dependencies", write, h.addDependency)
	route("DELETE /api/v1/services/{id}/dependencies/{dependencyId}", write, h.removeDependency)
	route("GET /api/v1/impact", read, h.impact)
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "services.invalid_request", inv.Message)
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "services.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrConflict):
		httpx.WriteError(w, http.StatusConflict, "services.conflict", "A service with this name already exists.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "services.version_conflict", "The record was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrRetired):
		httpx.WriteError(w, http.StatusConflict, "services.retired", "The service is retired.")
	case errors.Is(err, application.ErrDependencyCycle):
		httpx.WriteError(w, http.StatusConflict, "services.dependency_cycle", "The dependency would create a cycle between services.")
	case errors.Is(err, application.ErrCycleCheck):
		httpx.WriteError(w, http.StatusConflict, "services.dependency_graph_too_large", "The dependency graph is too large to check for cycles.")
	case errors.Is(err, application.ErrReferenceInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "services.invalid_reference", "A referenced user, team, service, virtual machine, asset or location does not exist or cannot be used.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "services.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "services request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func principal(r *http.Request) application.Principal {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Principal{UserID: p.UserID, View: p.Has(permView), Manage: p.Has(permManage),
		InfraView:  p.Has(permInfraView) || p.Has(permInfraManage),
		AssetsView: p.Has(permAssetsView) || p.Has(permAssetsMan)}
}

func caller(w http.ResponseWriter, r *http.Request) application.Caller {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: httpx.RequestID(w)}
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst, maxBody); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "services.invalid_request", "The request body is not valid JSON for this operation.")
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

// ---- handlers ----

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "services.invalid_limit", "The limit must be a positive integer.")
		return
	}
	q := r.URL.Query()
	res, err := h.app.List(r.Context(), principal(r), application.Filter{
		Status: q.Get("status"), Criticality: q.Get("criticality"), OwnerUserID: q.Get("ownerUserId"), TeamID: q.Get("teamId"),
		Query: q.Get("q"), IncludeRetired: q.Get("includeRetired") == "true",
		Page: application.Page{Limit: limit, Cursor: q.Get("cursor")},
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := listDTO{Items: make([]serviceDTO, 0, len(res.Items)), NextCursor: res.NextCursor}
	for _, s := range res.Items {
		out.Items = append(out.Items, toService(s))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	d, err := h.app.Get(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := detailDTO{serviceDTO: toService(d.Service), Dependencies: make([]linkDTO, 0, len(d.Dependencies)), Dependents: make([]linkDTO, 0, len(d.Dependents)),
		DependenciesTruncated: d.DependenciesCutOff, DependentsTruncated: d.DependentsCutOff}
	for _, l := range d.Dependencies {
		out.Dependencies = append(out.Dependencies, toLink(l))
	}
	for _, l := range d.Dependents {
		out.Dependents = append(out.Dependents, toLink(l))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		OwnerUserID   string `json:"ownerUserId"`
		OwnerTeamID   string `json:"ownerTeamId"`
		SupportTeamID string `json:"supportTeamId"`
		Criticality   string `json:"criticality"`
		Status        string `json:"status"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.app.Create(r.Context(), caller(w, r), principal(r), application.Input{Name: b.Name, Description: b.Description,
		OwnerUserID: b.OwnerUserID, OwnerTeamID: b.OwnerTeamID, SupportTeamID: b.SupportTeamID, Criticality: b.Criticality, Status: b.Status})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toService(out))
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name            *string `json:"name"`
		Description     *string `json:"description"`
		OwnerUserID     *string `json:"ownerUserId"`
		OwnerTeamID     *string `json:"ownerTeamId"`
		SupportTeamID   *string `json:"supportTeamId"`
		Criticality     *string `json:"criticality"`
		ExpectedVersion *int    `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.app.UpdateDetails(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, application.Details{
		Name: b.Name, Description: b.Description, OwnerUserID: b.OwnerUserID, OwnerTeamID: b.OwnerTeamID, SupportTeamID: b.SupportTeamID, Criticality: b.Criticality})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toService(out))
}

func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Status          string `json:"status"`
		Reason          string `json:"reason"`
		ExpectedVersion *int   `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.app.ChangeStatus(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.Status, b.Reason)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toService(out))
}

func (h *handler) retire(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Reason          string `json:"reason"`
		ExpectedVersion *int   `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.app.Retire(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.Reason)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toService(out))
}

func (h *handler) addDependency(w http.ResponseWriter, r *http.Request) {
	var b struct {
		TargetType string `json:"targetType"`
		TargetID   string `json:"targetId"`
	}
	if !decode(w, r, &b) {
		return
	}
	l, created, err := h.app.AddDependency(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.TargetType, b.TargetID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, toLink(l))
}

// removeDependency takes the reason code as the query parameter "reason".
func (h *handler) removeDependency(w http.ResponseWriter, r *http.Request) {
	err := h.app.RemoveDependency(r.Context(), caller(w, r), principal(r), r.PathValue("id"), r.PathValue("dependencyId"), r.URL.Query().Get("reason"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) impact(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	depth := 0
	if v := q.Get("depth"); v != "" {
		n, ok := atoi(v)
		if !ok {
			httpx.WriteError(w, http.StatusBadRequest, "services.invalid_request", "depth must be a number between 1 and 6.")
			return
		}
		depth = n
	}
	res, err := h.app.Impact(r.Context(), principal(r), application.ImpactInput{Type: q.Get("type"), ID: q.Get("id"), Direction: q.Get("direction"), Depth: depth})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toImpact(res))
}

func atoi(s string) (int, bool) {
	if len(s) == 0 || len(s) > 3 {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}
