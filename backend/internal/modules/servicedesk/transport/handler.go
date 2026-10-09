// Package transport exposes the Service Desk HTTP API under /api/v1. Any
// signed-in User raises tickets and reads those they reported or are affected
// by; tickets.view reads all tickets and internal comments; tickets.manage
// works them.
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
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
	route("GET /api/v1/tickets/fields", h.fields)
	route("POST /api/v1/tickets/query", h.query)
	route("POST /api/v1/tickets", h.create)
	route("GET /api/v1/tickets/by-reference", h.byReference)
	route("GET /api/v1/tickets/{id}", h.get)
	route("POST /api/v1/tickets/{id}/move-queue", h.moveQueue)
	route("POST /api/v1/tickets/{id}/comments", h.comment)
	route("POST /api/v1/tickets/{id}/assign", h.assign)
	route("POST /api/v1/tickets/{id}/priority", h.priority)
	registerQueues(route, h)
	for _, op := range []string{application.OpStart, application.OpWait, application.OpResume, application.OpResolve, application.OpClose, application.OpReopen, application.OpCancel} {
		route("POST /api/v1/tickets/{id}/"+op, h.transition(op))
	}
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	if query.WriteError(w, err) {
		return
	}
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
	case errors.Is(err, application.ErrCommentLimit):
		httpx.WriteError(w, http.StatusConflict, "tickets.comment_limit", "This ticket has reached the comment limit.")
	case errors.Is(err, application.ErrUserInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "tickets.user_invalid", "The user does not exist or is not active.")
	case errors.Is(err, application.ErrTeamInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "tickets.team_invalid", "The team does not exist or is not active.")
	case errors.Is(err, application.ErrDeviceInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "tickets.device_invalid", "The device does not exist or is not assigned to the affected user.")
	case errors.Is(err, application.ErrQueueNotFound):
		httpx.WriteError(w, http.StatusNotFound, "servicedesk.queue_not_found", "The queue was not found.")
	case errors.Is(err, application.ErrQueueNotPermitted):
		httpx.WriteError(w, http.StatusForbidden, "servicedesk.queue_not_permitted", "You cannot use this queue.")
	case errors.Is(err, application.ErrQueueArchived):
		httpx.WriteError(w, http.StatusConflict, "servicedesk.queue_archived", "The queue is archived.")
	case errors.Is(err, application.ErrQueueKeyTaken):
		httpx.WriteError(w, http.StatusConflict, "servicedesk.queue_key_taken", "A queue with this key already exists.")
	case errors.Is(err, application.ErrQueuePrefixTaken):
		httpx.WriteError(w, http.StatusConflict, "servicedesk.queue_prefix_taken", "This prefix is already used by a current or former queue.")
	case errors.Is(err, application.ErrQueueHasOpenTickets):
		httpx.WriteError(w, http.StatusConflict, "servicedesk.queue_has_open_tickets", "Move the open tickets to another queue first.")
	case errors.Is(err, application.ErrQueueIsDefault):
		httpx.WriteError(w, http.StatusConflict, "servicedesk.queue_is_default", "The intake queue cannot be archived; choose another intake queue first.")
	case errors.Is(err, application.ErrQueueSame):
		httpx.WriteError(w, http.StatusConflict, "servicedesk.queue_same", "The ticket is already in this queue.")
	case errors.Is(err, application.ErrAssigneeNoAccess):
		httpx.WriteError(w, http.StatusBadRequest, "servicedesk.assignee_no_queue_access", "The assignee cannot view tickets of this queue.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "tickets.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "ticket request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func principal(r *http.Request) application.Principal {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Principal{UserID: p.UserID, View: p.Has(permView), Manage: p.Has(permManage), QueuesManage: p.Has(application.PermQueuesManage)}
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
	ID              string         `json:"id"`
	Reference       string         `json:"reference"`
	Title           string         `json:"title"`
	Description     *string        `json:"description"`
	Status          string         `json:"status"`
	WaitingReason   *string        `json:"waitingReason"`
	StatusReason    *string        `json:"statusReason"`
	Resolution      *string        `json:"resolution"`
	Priority        string         `json:"priority"`
	ReporterID      string         `json:"reporterId"`
	AffectedUserID  string         `json:"affectedUserId"`
	QueueTeamID     *string        `json:"queueTeamId"`
	QueueID         string         `json:"queueId,omitempty"`
	Queue           *queueRefDTO   `json:"queue"`
	QueueLabel      string         `json:"queueLabel,omitempty"`
	Aliases         []string       `json:"aliases,omitempty"`
	AssigneeID      *string        `json:"assigneeId"`
	AssetID         *string        `json:"assetId"`
	MajorIncidentID *string        `json:"majorIncidentId"`
	DeviceSnapshot  map[string]any `json:"deviceSnapshot"`
	ResolvedAt      *string        `json:"resolvedAt"`
	ClosedAt        *string        `json:"closedAt"`
	Version         int            `json:"version"`
	CreatedAt       string         `json:"createdAt"`
	UpdatedAt       string         `json:"updatedAt"`
}

type queueRefDTO struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Prefix string `json:"prefix"`
	Name   string `json:"name"`
}

func toTicket(t application.Ticket) ticketDTO {
	var q *queueRefDTO
	if t.Queue != nil {
		q = &queueRefDTO{ID: t.Queue.ID, Key: t.Queue.Key, Prefix: t.Queue.Prefix, Name: t.Queue.Name}
	}
	return ticketDTO{QueueID: queueIDOf(t), Queue: q, QueueLabel: t.QueueLabel, Aliases: t.Aliases, ID: t.ID, Reference: t.Reference, Title: t.Title, Description: t.Description, Status: t.Status, WaitingReason: t.WaitingReason,
		StatusReason: t.StatusReason, Resolution: t.Resolution, Priority: t.Priority, ReporterID: t.ReporterID, AffectedUserID: t.AffectedUserID,
		QueueTeamID: t.QueueTeamID, AssigneeID: t.AssigneeID, AssetID: t.AssetID, MajorIncidentID: t.MajorIncidentID, DeviceSnapshot: t.DeviceSnapshot, ResolvedAt: tsPtr(t.ResolvedAt),
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
	if query.HasParams(v) || v.Get("queue") != "" {
		h.queryList(w, r, v, limit)
		return
	}
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

// queueIDOf is the Queue id only when the caller may know the Queue.
func queueIDOf(t application.Ticket) string {
	if t.Queue == nil {
		return ""
	}
	return t.Queue.ID
}

// queryList serves the additive filter/sort/search/count parameters of the list endpoint; the plain
// parameters (status, assigneeId, queueId, open, scope) keep their meaning and are ANDed to the filter.
func (h *handler) queryList(w http.ResponseWriter, r *http.Request, v url.Values, limit int) {
	req, err := query.ParseParams(v, v.Get("cursor"), limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	p := principal(r)
	compat, err := h.svc.CompatNodes(r.Context(), p, application.Filter{Status: v.Get("status"), AssigneeID: v.Get("assigneeId"),
		QueueID: v.Get("queueId"), OpenOnly: v.Get("open") == "true"})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	scope := application.ScopeMine
	if v.Get("scope") == "all" {
		scope = application.ScopeAll
	}
	page, err := h.svc.QueryScoped(r.Context(), p, req, scope, compat, v.Get("queue"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, query.NewEnvelope(page, toTicket))
}

// fields returns the ticket Field Catalog as the caller may use it.
func (h *handler) fields(w http.ResponseWriter, r *http.Request) {
	info, err := h.svc.QueryFields(r.Context(), principal(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, info)
}

// query runs a Filter AST over the tickets the caller may see (staff: all, others: their own).
func (h *handler) query(w http.ResponseWriter, r *http.Request) {
	req, err := query.DecodeBody(w, r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	page, err := h.svc.QueryScoped(r.Context(), principal(r), req, application.ScopeAuto, nil, "")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, query.NewEnvelope(page, toTicket))
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
		QueueID        *string `json:"queueId"`
		QueueKey       string  `json:"queueKey"`
	}
	if !decode(w, r, &b) {
		return
	}
	t, err := h.svc.Create(r.Context(), caller(w, r), principal(r), application.CreateInput{
		Title: b.Title, Description: b.Description, AffectedUserID: b.AffectedUserID, AssetID: b.AssetID, Priority: b.Priority, QueueTeamID: b.QueueTeamID,
		QueueID: b.QueueID, QueueKey: b.QueueKey,
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

func (h *handler) moveQueue(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int   `json:"expectedVersion"`
		TargetQueueID   string `json:"targetQueueId"`
		ReasonCode      string `json:"reasonCode"`
	}
	if !decode(w, r, &b) {
		return
	}
	t, err := h.svc.MoveToQueue(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.TargetQueueID, b.ReasonCode)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toTicket(t))
}

// byReference resolves a current or earlier reference of a Ticket. Unknown, malformed and invisible references all
// answer 404 tickets.not_found.
func (h *handler) byReference(w http.ResponseWriter, r *http.Request) {
	m, err := h.svc.FindByReference(r.Context(), principal(r), r.URL.Query().Get("reference"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		TicketID  string `json:"ticketId"`
		Reference string `json:"reference"`
		Alias     bool   `json:"alias"`
	}{m.TicketID, m.Reference, m.Alias})
}
