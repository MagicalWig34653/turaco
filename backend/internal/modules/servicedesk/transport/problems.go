package transport

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const permProblems = "problems.manage"

type problemHandler struct {
	svc    *application.ProblemService
	logger *slog.Logger
}

// RegisterProblems mounts the Problem routes: people with tickets.view or tickets.manage read
// problems and known errors; problems.manage writes. Employees get 404/403.
func RegisterProblems(mux *http.ServeMux, svc *application.ProblemService, auth authorization.Authenticator, logger *slog.Logger) {
	h := &problemHandler{svc: svc, logger: logger}
	authed := authorization.RequireAuthenticated(auth)
	route := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, httpx.NoStore(authed(fn))) }
	route("GET /api/v1/problems", h.list)
	route("POST /api/v1/problems", h.create)
	route("GET /api/v1/problems/{id}", h.get)
	route("POST /api/v1/problems/{id}/owner", h.owner)
	route("POST /api/v1/problems/{id}/tickets", h.link(true))
	route("DELETE /api/v1/problems/{id}/tickets/{ticketId}", h.unlink)
	route("GET /api/v1/tickets/{id}/known-errors", h.knownErrors)
	for _, op := range []string{application.POInvestigate, application.POIdentifyCause, application.POMarkKnownError, application.POPlanResolution, application.POResolve, application.POClose} {
		route("POST /api/v1/problems/{id}/"+opPath(op), h.transition(op))
	}
}

func opPath(op string) string {
	out := []byte(op)
	for i, b := range out {
		if b == '_' {
			out[i] = '-'
		}
	}
	return string(out)
}

func (h *problemHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	var tr *application.InvalidTransitionError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "tickets.invalid_request", inv.Message)
	case errors.As(err, &tr):
		httpx.WriteError(w, http.StatusConflict, "tickets.invalid_transition", "The operation is not allowed in the problem's current status.")
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "tickets.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "tickets.version_conflict", "The problem was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrUserInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "tickets.user_invalid", "The user does not exist or is not active.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "tickets.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "problem request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func pp(r *http.Request) application.ProblemPrincipal {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.ProblemPrincipal{UserID: p.UserID, Staff: p.Has(permView) || p.Has(permManage), Manage: p.Has(permProblems)}
}

func pcaller(w http.ResponseWriter, r *http.Request) application.Caller {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: httpx.RequestID(w)}
}

func pdecode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst, maxBody); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "tickets.invalid_request", "The request body is not valid JSON for this operation.")
		return false
	}
	return true
}

type problemDTO struct {
	ID          string  `json:"id"`
	Reference   string  `json:"reference"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
	Status      string  `json:"status"`
	Cause       *string `json:"cause"`
	Workaround  *string `json:"workaround"`
	Resolution  *string `json:"resolution"`
	OwnerID     *string `json:"ownerId"`
	Tickets     int     `json:"linkedTickets"`
	ResolvedAt  *string `json:"resolvedAt"`
	ClosedAt    *string `json:"closedAt"`
	Version     int     `json:"version"`
	CreatedAt   string  `json:"createdAt"`
	UpdatedAt   string  `json:"updatedAt"`
}

func toProblem(p application.Problem) problemDTO {
	return problemDTO{ID: p.ID, Reference: p.Reference, Title: p.Title, Description: p.Description, Status: p.Status, Cause: p.Cause, Workaround: p.Workaround,
		Resolution: p.Resolution, OwnerID: p.OwnerID, Tickets: p.Tickets, ResolvedAt: tsPtr(p.ResolvedAt), ClosedAt: tsPtr(p.ClosedAt),
		Version: p.Version, CreatedAt: ts(p.CreatedAt), UpdatedAt: ts(p.UpdatedAt)}
}

func (h *problemHandler) list(w http.ResponseWriter, r *http.Request) {
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "tickets.invalid_limit", "The limit must be a positive integer.")
		return
	}
	v := r.URL.Query()
	res, err := h.svc.List(r.Context(), pp(r), v.Get("status"), application.Page{Limit: limit, Cursor: v.Get("cursor")})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		Items      []problemDTO `json:"items"`
		NextCursor string       `json:"nextCursor,omitempty"`
	}{Items: make([]problemDTO, 0, len(res.Items)), NextCursor: res.NextCursor}
	for _, p := range res.Items {
		out.Items = append(out.Items, toProblem(p))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *problemHandler) get(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.Get(r.Context(), pp(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type tk struct {
		ID        string `json:"id"`
		Reference string `json:"reference"`
		Title     string `json:"title"`
		Status    string `json:"status"`
	}
	out := struct {
		problemDTO
		Tickets_          []tk     `json:"tickets"`
		AllowedOperations []string `json:"allowedOperations"`
	}{problemDTO: toProblem(d.Problem), Tickets_: make([]tk, 0, len(d.Tickets)), AllowedOperations: d.Operations}
	for _, t := range d.Tickets {
		out.Tickets_ = append(out.Tickets_, tk{ID: t.ID, Reference: t.Reference, Title: t.Title, Status: t.Status})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *problemHandler) create(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if !pdecode(w, r, &b) {
		return
	}
	p, err := h.svc.CreateProblem(r.Context(), pcaller(w, r), pp(r), b.Title, b.Description)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toProblem(p))
}

func (h *problemHandler) owner(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int   `json:"expectedVersion"`
		OwnerID         string `json:"ownerId"`
	}
	if !pdecode(w, r, &b) {
		return
	}
	p, err := h.svc.SetOwner(r.Context(), pcaller(w, r), pp(r), r.PathValue("id"), b.ExpectedVersion, b.OwnerID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toProblem(p))
}

func (h *problemHandler) link(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			TicketID string `json:"ticketId"`
		}
		if !pdecode(w, r, &b) {
			return
		}
		if err := h.svc.LinkTicket(r.Context(), pcaller(w, r), pp(r), r.PathValue("id"), b.TicketID, on); err != nil {
			h.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *problemHandler) unlink(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.LinkTicket(r.Context(), pcaller(w, r), pp(r), r.PathValue("id"), r.PathValue("ticketId"), false); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *problemHandler) knownErrors(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.KnownErrorsOfTicket(r.Context(), pp(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		Items []problemDTO `json:"items"`
	}{Items: make([]problemDTO, 0, len(list))}
	for _, p := range list {
		out.Items = append(out.Items, toProblem(p))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *problemHandler) transition(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			ExpectedVersion *int   `json:"expectedVersion"`
			Text            string `json:"text"`
		}
		if !pdecode(w, r, &b) {
			return
		}
		p, err := h.svc.Transition(r.Context(), pcaller(w, r), pp(r), r.PathValue("id"), b.ExpectedVersion, op, application.ProblemParams{Text: b.Text})
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toProblem(p))
	}
}
