package transport

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const permExecute = "runbooks.execute"

type runbookHandler struct {
	svc    *application.RunbookService
	logger *slog.Logger
}

// RegisterRunbooks mounts the runbook routes: knowledge.view/manage or runbooks.execute read,
// knowledge.manage defines, runbooks.execute starts and cancels executions.
func RegisterRunbooks(mux *http.ServeMux, svc *application.RunbookService, auth authorization.Authenticator, logger *slog.Logger) {
	h := &runbookHandler{svc: svc, logger: logger}
	authed := authorization.RequireAuthenticated(auth)
	route := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, httpx.NoStore(authed(fn))) }
	route("GET /api/v1/runbooks", h.list)
	route("POST /api/v1/runbooks", h.create)
	route("GET /api/v1/runbooks/{id}", h.get)
	route("PATCH /api/v1/runbooks/{id}", h.update)
	route("POST /api/v1/runbooks/{id}/activate", h.active(true))
	route("POST /api/v1/runbooks/{id}/deactivate", h.active(false))
	route("POST /api/v1/runbooks/{id}/executions", h.start)
	route("GET /api/v1/runbook-executions", h.listExecutions)
	route("GET /api/v1/runbook-executions/{id}", h.getExecution)
	route("POST /api/v1/runbook-executions/{id}/cancel", h.cancel)
}

func (h *runbookHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	var tr *application.InvalidTransitionError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "knowledge.invalid_request", inv.Message)
	case errors.As(err, &tr):
		httpx.WriteError(w, http.StatusConflict, "knowledge.invalid_transition", "The operation is not allowed in the current state.")
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "knowledge.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "knowledge.version_conflict", "The runbook was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "knowledge.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "runbook request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func rp(r *http.Request) application.RunbookPrincipal {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.RunbookPrincipal{UserID: p.UserID, View: p.Has(permView), Manage: p.Has(permManage), Execute: p.Has(permExecute)}
}

func rcaller(w http.ResponseWriter, r *http.Request) application.Caller {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: httpx.RequestID(w)}
}

func rdecode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst, maxBody); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "knowledge.invalid_request", "The request body is not valid JSON for this operation.")
		return false
	}
	return true
}

type stepDTO struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	TeamID      string `json:"teamId,omitempty"`
}

type runbookDTO struct {
	ID          string    `json:"id"`
	Reference   string    `json:"reference"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Steps       []stepDTO `json:"steps"`
	Active      bool      `json:"active"`
	Version     int       `json:"version"`
	UpdatedAt   string    `json:"updatedAt"`
}

func toSteps(in []application.Step) []stepDTO {
	out := make([]stepDTO, 0, len(in))
	for _, s := range in {
		out = append(out, stepDTO{Title: s.Title, Description: s.Description, TeamID: s.TeamID})
	}
	return out
}

func toRunbook(r application.Runbook) runbookDTO {
	return runbookDTO{ID: r.ID, Reference: r.Reference, Title: r.Title, Description: r.Description, Steps: toSteps(r.Steps), Active: r.Active, Version: r.Version, UpdatedAt: ts(r.UpdatedAt)}
}

type executionDTO struct {
	ID           string    `json:"id"`
	RunbookID    string    `json:"runbookId"`
	RunbookTitle string    `json:"runbookTitle"`
	Steps        []stepDTO `json:"steps"`
	ContextType  *string   `json:"contextType"`
	ContextID    *string   `json:"contextId"`
	Status       string    `json:"status"`
	TaskIDs      []string  `json:"taskIds"`
	FinishedAt   *string   `json:"finishedAt"`
	Version      int       `json:"version"`
	CreatedAt    string    `json:"createdAt"`
}

func toExecution(e application.Execution) executionDTO {
	d := executionDTO{ID: e.ID, RunbookID: e.RunbookID, RunbookTitle: e.RunbookTitle, Steps: toSteps(e.Steps), ContextType: e.ContextType, ContextID: e.ContextID,
		Status: e.Status, TaskIDs: e.TaskIDs, Version: e.Version, CreatedAt: ts(e.CreatedAt)}
	if d.TaskIDs == nil {
		d.TaskIDs = []string{}
	}
	if e.FinishedAt != nil {
		s := ts(*e.FinishedAt)
		d.FinishedAt = &s
	}
	return d
}

func (h *runbookHandler) list(w http.ResponseWriter, r *http.Request) {
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "knowledge.invalid_limit", "The limit must be a positive integer.")
		return
	}
	res, err := h.svc.List(r.Context(), rp(r), r.URL.Query().Get("active") == "true", application.Page{Limit: limit, Cursor: r.URL.Query().Get("cursor")})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		Items      []runbookDTO `json:"items"`
		NextCursor string       `json:"nextCursor,omitempty"`
	}{Items: make([]runbookDTO, 0, len(res.Items)), NextCursor: res.NextCursor}
	for _, x := range res.Items {
		out.Items = append(out.Items, toRunbook(x))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *runbookHandler) get(w http.ResponseWriter, r *http.Request) {
	x, err := h.svc.Get(r.Context(), rp(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toRunbook(x))
}

type runbookBody struct {
	ExpectedVersion *int      `json:"expectedVersion"`
	Title           string    `json:"title"`
	Description     string    `json:"description"`
	Steps           []stepDTO `json:"steps"`
}

func (b runbookBody) input() application.RunbookInput {
	in := application.RunbookInput{Title: b.Title, Description: b.Description}
	for _, s := range b.Steps {
		in.Steps = append(in.Steps, application.Step{Title: s.Title, Description: s.Description, TeamID: s.TeamID})
	}
	return in
}

func (h *runbookHandler) create(w http.ResponseWriter, r *http.Request) {
	var b runbookBody
	if !rdecode(w, r, &b) {
		return
	}
	x, err := h.svc.Create(r.Context(), rcaller(w, r), rp(r), b.input())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toRunbook(x))
}

func (h *runbookHandler) update(w http.ResponseWriter, r *http.Request) {
	var b runbookBody
	if !rdecode(w, r, &b) {
		return
	}
	if b.ExpectedVersion == nil {
		httpx.WriteError(w, http.StatusBadRequest, "knowledge.invalid_request", "expectedVersion is required.")
		return
	}
	x, err := h.svc.Update(r.Context(), rcaller(w, r), rp(r), r.PathValue("id"), *b.ExpectedVersion, b.input())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toRunbook(x))
}

func (h *runbookHandler) active(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			ExpectedVersion *int `json:"expectedVersion"`
		}
		if !rdecode(w, r, &b) {
			return
		}
		x, err := h.svc.SetActive(r.Context(), rcaller(w, r), rp(r), r.PathValue("id"), b.ExpectedVersion, on)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toRunbook(x))
	}
}

func (h *runbookHandler) start(w http.ResponseWriter, r *http.Request) {
	var b struct {
		TicketID string `json:"ticketId"`
	}
	if !rdecode(w, r, &b) {
		return
	}
	e, err := h.svc.Start(r.Context(), rcaller(w, r), rp(r), r.PathValue("id"), b.TicketID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toExecution(e))
}

func (h *runbookHandler) cancel(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Reason string `json:"reason"`
	}
	if !rdecode(w, r, &b) {
		return
	}
	e, err := h.svc.Cancel(r.Context(), rcaller(w, r), rp(r), r.PathValue("id"), b.Reason)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toExecution(e))
}

func (h *runbookHandler) getExecution(w http.ResponseWriter, r *http.Request) {
	e, err := h.svc.GetExecution(r.Context(), rp(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toExecution(e))
}

func (h *runbookHandler) listExecutions(w http.ResponseWriter, r *http.Request) {
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "knowledge.invalid_limit", "The limit must be a positive integer.")
		return
	}
	v := r.URL.Query()
	res, err := h.svc.ListExecutions(r.Context(), rp(r), v.Get("runbookId"), v.Get("ticketId"), application.Page{Limit: limit, Cursor: v.Get("cursor")})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		Items      []executionDTO `json:"items"`
		NextCursor string         `json:"nextCursor,omitempty"`
	}{Items: make([]executionDTO, 0, len(res.Items)), NextCursor: res.NextCursor}
	for _, e := range res.Items {
		out.Items = append(out.Items, toExecution(e))
	}
	httpx.JSON(w, http.StatusOK, out)
}
