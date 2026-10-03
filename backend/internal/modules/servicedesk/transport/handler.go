// Package transport exposes the Service Desk HTTP API under /api/v1. Any
// signed-in User raises tickets and reads those they reported or are affected
// by; tickets.view reads all tickets and internal comments; tickets.manage
// works them.
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const (
	permView   = "tickets.view"
	permManage = "tickets.manage"
	maxBody    = 24 << 10
)

type handler struct {
	svc    *application.Service
	logger *slog.Logger
}

// Register mounts the ticket routes.
func Register(mux *http.ServeMux, svc *application.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	authed := authorization.RequireAuthenticated(auth)
	route := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, httpx.NoStore(authed(fn))) }
	route("GET /api/v1/tickets", h.list)
	route("POST /api/v1/tickets", h.create)
	route("GET /api/v1/tickets/{id}", h.get)
	route("POST /api/v1/tickets/{id}/comments", h.comment)
	route("POST /api/v1/tickets/{id}/assign", h.assign)
	route("POST /api/v1/tickets/{id}/priority", h.priority)
	for _, op := range []string{application.OpStart, application.OpWait, application.OpResume, application.OpResolve, application.OpClose, application.OpReopen, application.OpCancel} {
		route("POST /api/v1/tickets/{id}/"+op, h.transition(op))
	}
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	var tr *application.InvalidTransitionError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "tickets.invalid_request", inv.Message)
	case errors.As(err, &tr):
		httpx.WriteError(w, http.StatusConflict, "tickets.invalid_transition", "The operation is not allowed in the ticket's current status.")
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "tickets.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "tickets.version_conflict", "The ticket was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrUserInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "tickets.user_invalid", "The user does not exist or is not active.")
	case errors.Is(err, application.ErrTeamInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "tickets.team_invalid", "The team does not exist or is not active.")
	case errors.Is(err, application.ErrDeviceInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "tickets.device_invalid", "The device does not exist or is not assigned to the affected user.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "tickets.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "ticket request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func principal(r *http.Request) application.Principal {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Principal{UserID: p.UserID, View: p.Has(permView), Manage: p.Has(permManage)}
}

func caller(w http.ResponseWriter, r *http.Request) application.Caller {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: httpx.RequestID(w)}
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst, maxBody); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "tickets.invalid_request", "The request body is not valid JSON for this operation.")
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

type ticketDTO struct {
	ID             string         `json:"id"`
	Reference      string         `json:"reference"`
	Title          string         `json:"title"`
	Description    *string        `json:"description"`
	Status         string         `json:"status"`
	WaitingReason  *string        `json:"waitingReason"`
	StatusReason   *string        `json:"statusReason"`
	Resolution     *string        `json:"resolution"`
	Priority       string         `json:"priority"`
	ReporterID     string         `json:"reporterId"`
	AffectedUserID string         `json:"affectedUserId"`
	QueueTeamID    *string        `json:"queueTeamId"`
	AssigneeID     *string        `json:"assigneeId"`
	AssetID        *string        `json:"assetId"`
	DeviceSnapshot map[string]any `json:"deviceSnapshot"`
	ResolvedAt     *string        `json:"resolvedAt"`
	ClosedAt       *string        `json:"closedAt"`
	Version        int            `json:"version"`
	CreatedAt      string         `json:"createdAt"`
	UpdatedAt      string         `json:"updatedAt"`
}

func toTicket(t application.Ticket) ticketDTO {
	return ticketDTO{ID: t.ID, Reference: t.Reference, Title: t.Title, Description: t.Description, Status: t.Status, WaitingReason: t.WaitingReason,
		StatusReason: t.StatusReason, Resolution: t.Resolution, Priority: t.Priority, ReporterID: t.ReporterID, AffectedUserID: t.AffectedUserID,
		QueueTeamID: t.QueueTeamID, AssigneeID: t.AssigneeID, AssetID: t.AssetID, DeviceSnapshot: t.DeviceSnapshot, ResolvedAt: tsPtr(t.ResolvedAt),
		ClosedAt: tsPtr(t.ClosedAt), Version: t.Version, CreatedAt: ts(t.CreatedAt), UpdatedAt: ts(t.UpdatedAt)}
}

type commentDTO struct {
	ID        string `json:"id"`
	AuthorID  string `json:"authorId"`
	Body      string `json:"body"`
	Internal  bool   `json:"internal"`
	CreatedAt string `json:"createdAt"`
}

type detailDTO struct {
	ticketDTO
	Comments          []commentDTO      `json:"comments"`
	AllowedOperations []string          `json:"allowedOperations"`
	Names             map[string]string `json:"names"`
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "tickets.invalid_limit", "The limit must be a positive integer.")
		return
	}
	v := r.URL.Query()
	res, err := h.svc.List(r.Context(), principal(r), v.Get("scope") == "all", application.Filter{
		Status: v.Get("status"), AssigneeID: v.Get("assigneeId"), QueueID: v.Get("queueId"), OpenOnly: v.Get("open") == "true",
		Page: application.Page{Limit: limit, Cursor: v.Get("cursor")},
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		Items      []ticketDTO `json:"items"`
		NextCursor string      `json:"nextCursor,omitempty"`
	}{Items: make([]ticketDTO, 0, len(res.Items)), NextCursor: res.NextCursor}
	for _, t := range res.Items {
		out.Items = append(out.Items, toTicket(t))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.Get(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := detailDTO{ticketDTO: toTicket(d.Ticket), Comments: make([]commentDTO, 0, len(d.Comments)), AllowedOperations: d.Allowed, Names: d.Names}
	for _, c := range d.Comments {
		out.Comments = append(out.Comments, commentDTO{ID: c.ID, AuthorID: c.AuthorID, Body: c.Body, Internal: c.Internal, CreatedAt: ts(c.CreatedAt)})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Title          string  `json:"title"`
		Description    string  `json:"description"`
		AffectedUserID *string `json:"affectedUserId"`
		AssetID        *string `json:"assetId"`
		Priority       string  `json:"priority"`
		QueueTeamID    *string `json:"queueTeamId"`
	}
	if !decode(w, r, &b) {
		return
	}
	t, err := h.svc.Create(r.Context(), caller(w, r), principal(r), application.CreateInput{
		Title: b.Title, Description: b.Description, AffectedUserID: b.AffectedUserID, AssetID: b.AssetID, Priority: b.Priority, QueueTeamID: b.QueueTeamID,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toTicket(t))
}

func (h *handler) comment(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Body     string `json:"body"`
		Internal bool   `json:"internal"`
	}
	if !decode(w, r, &b) {
		return
	}
	c, err := h.svc.AddComment(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.Body, b.Internal)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, commentDTO{ID: c.ID, AuthorID: c.AuthorID, Body: c.Body, Internal: c.Internal, CreatedAt: ts(c.CreatedAt)})
}

func (h *handler) assign(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int    `json:"expectedVersion"`
		AssigneeID      *string `json:"assigneeId"`
		QueueTeamID     *string `json:"queueTeamId"`
	}
	if !decode(w, r, &b) {
		return
	}
	t, err := h.svc.Assign(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.AssigneeID, b.QueueTeamID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toTicket(t))
}

func (h *handler) priority(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int   `json:"expectedVersion"`
		Priority        string `json:"priority"`
	}
	if !decode(w, r, &b) {
		return
	}
	t, err := h.svc.SetPriority(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.Priority)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toTicket(t))
}

func (h *handler) transition(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			ExpectedVersion *int   `json:"expectedVersion"`
			Reason          string `json:"reason"`
		}
		if !decode(w, r, &b) {
			return
		}
		t, err := h.svc.Transition(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, op, application.Params{Reason: b.Reason})
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toTicket(t))
	}
}
