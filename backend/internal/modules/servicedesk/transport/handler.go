// Package transport exposes the Service Desk HTTP API under /api/v1. Any
// signed-in User raises tickets and reads those they reported or are affected
// by; tickets.view reads all tickets and internal comments; tickets.manage
// works them.
package transport

import (
	"cmp"
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
	route("GET /api/v1/tickets/overview", h.overview)
	route("POST /api/v1/tickets/query", h.query)
	route("POST /api/v1/tickets", h.create)
	route("GET /api/v1/tickets/by-reference", h.byReference)
	route("GET /api/v1/tickets/{id}", h.get)
	route("GET /api/v1/tickets/{id}/follow", h.followState)
	route("POST /api/v1/tickets/{id}/follow", h.follow)
	route("DELETE /api/v1/tickets/{id}/follow", h.unfollow)
	route("POST /api/v1/tickets/{id}/move-queue", h.moveQueue)
	route("POST /api/v1/tickets/{id}/comments", h.comment)
	route("POST /api/v1/tickets/{id}/assign", h.assign)
	route("GET /api/v1/tickets/{id}/history", h.history)
	route("POST /api/v1/sla/tickets/{id}/exempt", h.exemptSLA)
	route("GET /api/v1/sla/report", h.slaReport)
	route("POST /api/v1/tickets/{id}/duplicate", h.markDuplicate)
	route("POST /api/v1/tickets/{id}/priority", h.priority)
	route("POST /api/v1/tickets/{id}/location", h.location)
	route("POST /api/v1/tickets/{id}/device", h.device)
	route("GET /api/v1/tickets/{id}/duplicates", h.duplicates)
	route("GET /api/v1/tickets/{id}/related-tickets", h.relatedTickets)
	route("POST /api/v1/tickets/{id}/related-tickets", h.linkTicket)
	route("DELETE /api/v1/tickets/{id}/related-tickets/{otherId}", h.unlinkTicket)
	route("POST /api/v1/tickets/{id}/callback", h.callback)
	route("GET /api/v1/tickets/{id}/callback-tasks", h.callbackTasks)
	route("GET /api/v1/tickets/{id}/changes", h.linkedChanges)
	route("POST /api/v1/tickets/{id}/changes", h.linkChange)
	route("DELETE /api/v1/tickets/{id}/changes/{changeId}", h.unlinkChange)
	route("GET /api/v1/changes/{id}/tickets", h.ticketsOfChange)
	route("GET /api/v1/tickets/{id}/participants", h.participants)
	route("POST /api/v1/tickets/{id}/participants", h.addParticipant)
	route("DELETE /api/v1/tickets/{id}/participants/{userId}", h.removeParticipant)
	route("GET /api/v1/tickets/{id}/participant-candidates", h.participantCandidates)
	registerQueues(route, h)
	registerRouting(route, h)
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
	case errors.Is(err, application.ErrAssigneeInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "tickets.user_invalid", "The user does not exist or is not active.")
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
	case errors.Is(err, application.ErrDuplicateTarget):
		httpx.WriteError(w, http.StatusConflict, "servicedesk.invalid_duplicate_target", "This ticket cannot be the target: it must be another ticket that is not cancelled, not itself a duplicate, and the ticket being marked must not have duplicates of its own.")
	case errors.Is(err, application.ErrLocationOutOfScope):
		httpx.WriteError(w, http.StatusForbidden, "access.location_out_of_scope", "The location is outside the area you may work in.")
	case errors.Is(err, application.ErrAssigneeNoAccess):
		httpx.WriteError(w, http.StatusBadRequest, "servicedesk.assignee_no_queue_access", "The assignee cannot view tickets of this queue.")
	case errors.Is(err, application.ErrParticipantInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "tickets.participant_invalid", "Only active external accounts can be added as participants.")
	case errors.Is(err, application.ErrParticipantLimit):
		httpx.WriteError(w, http.StatusConflict, "tickets.participant_limit", "This ticket has reached the participant limit.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "tickets.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "ticket request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func principal(r *http.Request) application.Principal {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Principal{UserID: p.UserID, View: p.Has(permView), Manage: p.Has(permManage), ViewScope: p.ScopeOf(permView), ManageScope: p.ScopeOf(permManage), QueuesManage: p.Has(application.PermQueuesManage),
		ChangesView: p.Has("changes.view") || p.Has("changes.manage") || p.Has("changes.execute"), AssetsView: p.Has("assets.view") || p.Has("assets.manage"), External: p.External, SLAManage: p.Has("sla.manage")}
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
	WaitingUntil    *string        `json:"waitingUntil"`
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
	DuplicateOfID   *string        `json:"duplicateOfId"`
	PatientImpact   bool           `json:"patientImpact"`
	Impact          *string        `json:"impact"`
	LocationID      *string        `json:"affectedLocationId"`
	DeviceSnapshot  map[string]any `json:"deviceSnapshot"`
	ResolvedAt      *string        `json:"resolvedAt"`
	ClosedAt        *string        `json:"closedAt"`
	Version         int            `json:"version"`
	CreatedAt       string         `json:"createdAt"`
	UpdatedAt       string         `json:"updatedAt"`
	// SLASummary is the staff-only SLA state and due times of list and detail reads; absent for requesters and while
	// the sla switch is off.
	SLASummary *slaSummaryDTO `json:"slaSummary,omitempty"`
}

type slaSummaryDTO struct {
	State           string  `json:"state"`
	ResponseDueAt   *string `json:"responseDueAt"`
	ResolutionDueAt *string `json:"resolutionDueAt"`
}

type queueRefDTO struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Prefix string `json:"prefix"`
	Name   string `json:"name"`
}

func toTicket(t application.Ticket) ticketDTO {
	out := toTicketBase(t)
	if t.SLA != nil {
		out.SLASummary = &slaSummaryDTO{State: t.SLA.State, ResponseDueAt: tsPtr(t.SLA.ResponseDueAt), ResolutionDueAt: tsPtr(t.SLA.ResolutionDueAt)}
	}
	return out
}

func toTicketBase(t application.Ticket) ticketDTO {
	var q *queueRefDTO
	if t.Queue != nil {
		q = &queueRefDTO{ID: t.Queue.ID, Key: t.Queue.Key, Prefix: t.Queue.Prefix, Name: t.Queue.Name}
	}
	return ticketDTO{QueueID: queueIDOf(t), Queue: q, QueueLabel: t.QueueLabel, Aliases: t.Aliases, ID: t.ID, Reference: t.Reference, Title: t.Title, Description: t.Description, Status: t.Status, WaitingReason: t.WaitingReason, WaitingUntil: tsPtr(t.WaitingUntil),
		StatusReason: t.StatusReason, Resolution: t.Resolution, Priority: t.Priority, ReporterID: t.ReporterID, AffectedUserID: t.AffectedUserID,
		QueueTeamID: t.QueueTeamID, AssigneeID: t.AssigneeID, AssetID: t.AssetID, MajorIncidentID: t.MajorIncidentID, DuplicateOfID: t.DuplicateOfID, PatientImpact: t.PatientImpact, Impact: nilStr(t.ReportedImpact), LocationID: t.AffectedLocationID, DeviceSnapshot: t.DeviceSnapshot, ResolvedAt: tsPtr(t.ResolvedAt),
		ClosedAt: tsPtr(t.ClosedAt), Version: t.Version, CreatedAt: ts(t.CreatedAt), UpdatedAt: ts(t.UpdatedAt)}
}

type commentDTO struct {
	ID               string   `json:"id"`
	AuthorID         string   `json:"authorId"`
	Body             string   `json:"body"`
	Internal         bool     `json:"internal"`
	MentionedUserIDs []string `json:"mentionedUserIds"`
	CreatedAt        string   `json:"createdAt"`
}

func toComment(c application.Comment) commentDTO {
	m := c.MentionedUserIDs
	if m == nil {
		m = []string{}
	}
	return commentDTO{ID: c.ID, AuthorID: c.AuthorID, Body: c.Body, Internal: c.Internal, MentionedUserIDs: m, CreatedAt: ts(c.CreatedAt)}
}

type detailDTO struct {
	ticketDTO
	SLA               *slaDTO           `json:"sla,omitempty"`
	Comments          []commentDTO      `json:"comments"`
	AllowedOperations []string          `json:"allowedOperations"`
	Abilities         abilitiesDTO      `json:"abilities"`
	Names             map[string]string `json:"names"`
}

type slaDTO struct {
	Clocks []application.SLAClock `json:"clocks"`
}

type abilitiesDTO struct {
	Comment          bool `json:"comment"`
	InternalComment  bool `json:"internalComment"`
	Assign           bool `json:"assign"`
	SetPriority      bool `json:"setPriority"`
	Transition       bool `json:"transition"`
	Move             bool `json:"move"`
	MarkDuplicate    bool `json:"markDuplicate"`
	SetLocation      bool `json:"setLocation"`
	SetDevice        bool `json:"setDevice"`
	ScheduleCallback bool `json:"scheduleCallback"`
	ExemptSLA        bool `json:"exemptSla"`
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
	out := detailDTO{ticketDTO: toTicket(d.Ticket), Comments: make([]commentDTO, 0, len(d.Comments)), AllowedOperations: d.Allowed, Names: d.Names, Abilities: abilitiesDTO{Comment: d.Abilities.Comment, InternalComment: d.Abilities.InternalComment,
		Assign: d.Abilities.Assign, SetPriority: d.Abilities.SetPriority, Transition: d.Abilities.Transition, Move: d.Abilities.MoveQueue, MarkDuplicate: d.Abilities.MarkDuplicate, SetLocation: d.Abilities.SetLocation, SetDevice: d.Abilities.SetDevice, ScheduleCallback: d.Abilities.ScheduleCallback}}
	if d.SLA != nil {
		out.SLA = &slaDTO{Clocks: d.SLA}
	}
	if principal(r).SLAManage {
		for _, clock := range d.SLA {
			if clock.Status == "running" || clock.Status == "paused" {
				out.Abilities.ExemptSLA = true
				break
			}
		}
	}
	for _, c := range d.Comments {
		out.Comments = append(out.Comments, toComment(c))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) exemptSLA(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpectedVersion *int   `json:"expectedVersion"`
		Reason          string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := h.svc.ExemptSLA(r.Context(), caller(w, r), principal(r), r.PathValue("id"), in.ExpectedVersion, in.Reason); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"exempted": true})
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
		PatientImpact  bool    `json:"patientImpact"`
		Impact         string  `json:"impact"`
	}
	if !decode(w, r, &b) {
		return
	}
	t, err := h.svc.Create(r.Context(), caller(w, r), principal(r), application.CreateInput{
		Title: b.Title, Description: b.Description, AffectedUserID: b.AffectedUserID, AssetID: b.AssetID, Priority: b.Priority, QueueTeamID: b.QueueTeamID,
		QueueID: b.QueueID, QueueKey: b.QueueKey, PatientImpact: b.PatientImpact, Impact: b.Impact,
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
		// MentionedUserIDs name people an internal note mentions (they are notified when they may view the Queue).
		MentionedUserIDs []string `json:"mentionedUserIds"`
	}
	if !decode(w, r, &b) {
		return
	}
	c, err := h.svc.AddCommentWithMentions(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.Body, b.Internal, b.MentionedUserIDs)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toComment(c))
}

func (h *handler) assign(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int    `json:"expectedVersion"`
		AssigneeID      *string `json:"assigneeId"`
		QueueTeamID     *string `json:"queueTeamId"`
		Reason          string  `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	t, err := h.svc.AssignWithReason(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.AssigneeID, b.QueueTeamID, b.Reason)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toTicket(t))
}

// markDuplicate cancels the ticket as a duplicate of another one (explicit lifecycle operation, expectedVersion).
func (h *handler) markDuplicate(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion     *int   `json:"expectedVersion"`
		DuplicateOfTicketID string `json:"duplicateOfTicketId"`
		DuplicateOfID       string `json:"duplicateOfId"` // alias of duplicateOfTicketId
		DuplicateOfRef      string `json:"duplicateOfReference"`
		ReasonCode          string `json:"reasonCode"`
		Reason              string `json:"reason"`
		Note                string `json:"note"` // alias of reason
	}
	if !decode(w, r, &b) {
		return
	}
	target := cmp.Or(b.DuplicateOfTicketID, b.DuplicateOfID)
	if target == "" && b.DuplicateOfRef != "" {
		m, err := h.svc.FindByReference(r.Context(), principal(r), b.DuplicateOfRef)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		target = m.TicketID
	}
	t, err := h.svc.MarkDuplicate(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, target, cmp.Or(b.Reason, b.Note, b.ReasonCode))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toTicket(t))
}

type historyDTO struct {
	ID             string `json:"id"`
	At             string `json:"at"`
	Kind           string `json:"kind"`
	ActorID        string `json:"actorId,omitempty"`
	Via            string `json:"via"`
	Reason         string `json:"reason,omitempty"`
	ExpectedAt     string `json:"expectedAt,omitempty"`
	FromUserID     string `json:"fromUserId,omitempty"`
	ToUserID       string `json:"toUserId,omitempty"`
	FromTeamID     string `json:"fromTeamId,omitempty"`
	ToTeamID       string `json:"toTeamId,omitempty"`
	FromStatus     string `json:"fromStatus,omitempty"`
	ToStatus       string `json:"toStatus,omitempty"`
	FromPrio       string `json:"fromPriority,omitempty"`
	ToPrio         string `json:"toPriority,omitempty"`
	DuplicateOfID  string `json:"duplicateOfId,omitempty"`
	FromLocationID string `json:"fromLocationId,omitempty"`
	ToLocationID   string `json:"toLocationId,omitempty"`
	FromDevice     string `json:"fromDevice,omitempty"`
	ToDevice       string `json:"toDevice,omitempty"`
	TaskID         string `json:"taskId,omitempty"`
	DueAt          string `json:"dueAt,omitempty"`
	AssigneeID     string `json:"assigneeId,omitempty"`
	SLAKind        string `json:"slaKind,omitempty"`
	SLAOperation   string `json:"slaOperation,omitempty"`
}

// history lists the change history of a Ticket (assignments, routing, status, priority, Queue moves) for people
// who work it.
func (h *handler) history(w http.ResponseWriter, r *http.Request) {
	entries, names, err := h.svc.History(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]historyDTO, 0, len(entries))
	for _, e := range entries {
		out = append(out, historyDTO{ID: e.ID, At: ts(e.At), Kind: e.Kind, ActorID: e.ActorID, Via: e.Via, Reason: e.Reason, ExpectedAt: e.ExpectedAt, FromUserID: e.FromUserID, ToUserID: e.ToUserID,
			FromTeamID: e.FromTeamID, ToTeamID: e.ToTeamID, FromStatus: e.FromStatus, ToStatus: e.ToStatus, FromPrio: e.FromPrio, ToPrio: e.ToPrio, DuplicateOfID: e.DuplicateOfID, FromLocationID: e.FromLocationID, ToLocationID: e.ToLocationID, FromDevice: e.FromDevice, ToDevice: e.ToDevice, TaskID: e.TaskID, DueAt: e.DueAt, AssigneeID: e.AssigneeID, SLAKind: e.SLAKind, SLAOperation: e.SLAOperation})
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []historyDTO      `json:"items"`
		Names map[string]string `json:"names"`
	}{out, names})
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

// location sets (or, with a null locationId, clears) the affected Location of the ticket.
func (h *handler) location(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int    `json:"expectedVersion"`
		LocationID      *string `json:"locationId"`
	}
	if !decode(w, r, &b) {
		return
	}
	t, err := h.svc.SetLocation(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.LocationID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toTicket(t))
}

// device sets (or, with a null assetId, clears) the device of the ticket.
func (h *handler) device(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int    `json:"expectedVersion"`
		AssetID         *string `json:"assetId"`
	}
	if !decode(w, r, &b) {
		return
	}
	t, err := h.svc.SetDevice(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.AssetID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toTicket(t))
}

// callback schedules a callback: a Task of the Tasks module for the ticket, due at dueAt, assigned to assigneeId
// (default: the caller).
func (h *handler) callback(w http.ResponseWriter, r *http.Request) {
	var b struct {
		DueAt      time.Time `json:"dueAt"`
		AssigneeID string    `json:"assigneeId"`
		Title      string    `json:"title"`
	}
	if !decode(w, r, &b) {
		return
	}
	cb, err := h.svc.ScheduleCallback(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.DueAt, b.AssigneeID, b.Title)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, struct {
		TaskID     string `json:"taskId"`
		DueAt      string `json:"dueAt"`
		AssigneeID string `json:"assigneeId"`
	}{cb.TaskID, ts(cb.DueAt), cb.AssignedUserID})
}

func (h *handler) transition(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			ExpectedVersion *int       `json:"expectedVersion"`
			Reason          string     `json:"reason"`
			ExpectedAt      *time.Time `json:"expectedAt"`
		}
		if !decode(w, r, &b) {
			return
		}
		t, err := h.svc.Transition(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, op, application.Params{Reason: b.Reason, ExpectedAt: b.ExpectedAt})
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

func nilStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

type changeLinkDTO struct {
	ID        string `json:"id"`
	ChangeID  string `json:"changeId,omitempty"`
	Reference string `json:"reference,omitempty"`
	Title     string `json:"title,omitempty"`
	Status    string `json:"status,omitempty"`
	Hidden    bool   `json:"hidden"`
}

func toChangeLink(l application.ChangeLink) changeLinkDTO {
	return changeLinkDTO{ID: l.ID, ChangeID: l.ChangeID, Reference: l.Reference, Title: l.Title, Status: l.Status, Hidden: l.Hidden}
}

// linkedChanges lists the Changes related to the ticket.
func (h *handler) linkedChanges(w http.ResponseWriter, r *http.Request) {
	links, err := h.svc.LinkedChanges(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]changeLinkDTO, 0, len(links))
	for _, l := range links {
		out = append(out, toChangeLink(l))
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []changeLinkDTO `json:"items"`
	}{out})
}

// linkChange relates the ticket to a Change (idempotent).
func (h *handler) linkChange(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ChangeID string `json:"changeId"`
	}
	if !decode(w, r, &b) {
		return
	}
	l, err := h.svc.LinkChange(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ChangeID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toChangeLink(l))
}

func (h *handler) unlinkChange(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.UnlinkChange(r.Context(), caller(w, r), principal(r), r.PathValue("id"), r.PathValue("changeId")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ticketsOfChange lists the tickets related to a Change that the caller may see.
func (h *handler) ticketsOfChange(w http.ResponseWriter, r *http.Request) {
	links, err := h.svc.TicketsOfChange(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type item struct {
		TicketID  string `json:"ticketId"`
		Reference string `json:"reference"`
		Title     string `json:"title"`
		Status    string `json:"status"`
	}
	out := make([]item, 0, len(links))
	for _, l := range links {
		out = append(out, item{l.TicketID, l.Reference, l.Title, l.Status})
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []item `json:"items"`
	}{out})
}

type relatedTicketDTO struct {
	ID        string `json:"id"`
	TicketID  string `json:"ticketId,omitempty"`
	Reference string `json:"reference,omitempty"`
	Title     string `json:"title,omitempty"`
	Status    string `json:"status,omitempty"`
	Hidden    bool   `json:"hidden"`
}

func writeRelated(w http.ResponseWriter, links []application.RelatedTicket) {
	out := make([]relatedTicketDTO, 0, len(links))
	for _, l := range links {
		out = append(out, relatedTicketDTO{ID: l.ID, TicketID: l.TicketID, Reference: l.Reference, Title: l.Title, Status: l.Status, Hidden: l.Hidden})
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []relatedTicketDTO `json:"items"`
	}{out})
}

// duplicates lists the tickets marked as duplicates of the ticket.
func (h *handler) duplicates(w http.ResponseWriter, r *http.Request) {
	links, err := h.svc.Duplicates(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeRelated(w, links)
}

// relatedTickets lists the tickets linked to the ticket.
func (h *handler) relatedTickets(w http.ResponseWriter, r *http.Request) {
	links, err := h.svc.RelatedTickets(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeRelated(w, links)
}

// linkTicket relates the ticket to another ticket (idempotent).
func (h *handler) linkTicket(w http.ResponseWriter, r *http.Request) {
	var b struct {
		TicketID string `json:"ticketId"`
	}
	if !decode(w, r, &b) {
		return
	}
	l, err := h.svc.LinkTicket(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.TicketID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, relatedTicketDTO{ID: l.ID, TicketID: l.TicketID, Reference: l.Reference, Title: l.Title, Status: l.Status})
}

func (h *handler) unlinkTicket(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.UnlinkTicket(r.Context(), caller(w, r), principal(r), r.PathValue("id"), r.PathValue("otherId")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) callbackTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := h.svc.CallbackTasksForTicket(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": tasks})
}
