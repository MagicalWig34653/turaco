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

const permMajor = "majorincidents.manage"

type majorHandler struct {
	svc    *application.MajorService
	logger *slog.Logger
}

// RegisterMajor mounts the Major Incident routes: every signed-in User reads and
// follows incidents; majorincidents.manage declares and updates them.
func RegisterMajor(mux *http.ServeMux, svc *application.MajorService, auth authorization.Authenticator, logger *slog.Logger) {
	h := &majorHandler{svc: svc, logger: logger}
	authed := authorization.RequireAuthenticated(auth)
	route := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, httpx.NoStore(authed(fn))) }
	route("GET /api/v1/major-incidents", h.list)
	route("POST /api/v1/major-incidents", h.declare)
	route("GET /api/v1/major-incidents/{id}", h.get)
	route("POST /api/v1/major-incidents/{id}/updates", h.update)
	route("POST /api/v1/major-incidents/{id}/tickets", h.link)
	route("DELETE /api/v1/major-incidents/{id}/tickets/{ticketId}", h.unlink)
	route("POST /api/v1/major-incidents/{id}/subscribe", h.subscribe(true))
	route("POST /api/v1/major-incidents/{id}/unsubscribe", h.subscribe(false))
	route("POST /api/v1/major-incidents/{id}/owner", h.setOwner)
	route("POST /api/v1/major-incidents/{id}/next-update", h.setNextUpdate)
	route("POST /api/v1/major-incidents/{id}/locations", h.setLocations)
	for _, op := range []string{application.MOInvestigate, application.MOMitigate, application.MOMonitor, application.MOResolve, application.MOClose} {
		route("POST /api/v1/major-incidents/{id}/"+op, h.transition(op))
	}
}

func (h *majorHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	var tr *application.InvalidTransitionError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "tickets.invalid_request", inv.Message)
	case errors.As(err, &tr):
		httpx.WriteError(w, http.StatusConflict, "tickets.invalid_transition", "The operation is not allowed in the incident's current status.")
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "tickets.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "tickets.version_conflict", "The incident was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "tickets.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "major incident request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func me(r *http.Request) (user string, manage bool) {
	p, _ := authorization.PrincipalFrom(r.Context())
	return p.UserID, p.Has(permMajor)
}

func mcaller(w http.ResponseWriter, r *http.Request) application.Caller {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: httpx.RequestID(w)}
}

func mdecode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst, maxBody); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "tickets.invalid_request", "The request body is not valid JSON for this operation.")
		return false
	}
	return true
}

type majorDTO struct {
	ID         string  `json:"id"`
	Reference  string  `json:"reference"`
	Title      string  `json:"title"`
	Summary    string  `json:"summary"`
	Status     string  `json:"status"`
	ResolvedAt *string `json:"resolvedAt"`
	ClosedAt   *string `json:"closedAt"`
	Subscribed bool    `json:"subscribed"`
	Tickets    int     `json:"linkedTickets"`
	Version    int     `json:"version"`
	CreatedAt  string  `json:"createdAt"`
	UpdatedAt  string  `json:"updatedAt"`
	IsExercise bool    `json:"isExercise"`
	// NextUpdateDue is public; the owner is staff information and only sent to majorincidents.manage holders.
	NextUpdateDue *string `json:"nextUpdateDue"`
	OwnerUserID   *string `json:"ownerUserId,omitempty"`
}

func toMajor(m application.MajorIncident, manage bool) majorDTO {
	d := toMajorPublic(m)
	if manage {
		d.OwnerUserID = m.OwnerUserID
	}
	return d
}

func toMajorPublic(m application.MajorIncident) majorDTO {
	return majorDTO{IsExercise: m.IsExercise, NextUpdateDue: tsPtr(m.NextUpdateDue), ID: m.ID, Reference: m.Reference, Title: m.Title, Summary: m.Summary, Status: m.Status, ResolvedAt: tsPtr(m.ResolvedAt), ClosedAt: tsPtr(m.ClosedAt),
		Subscribed: m.Subscribed, Tickets: m.Tickets, Version: m.Version, CreatedAt: ts(m.CreatedAt), UpdatedAt: ts(m.UpdatedAt)}
}

func (h *majorHandler) list(w http.ResponseWriter, r *http.Request) {
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "tickets.invalid_limit", "The limit must be a positive integer.")
		return
	}
	user, _ := me(r)
	q := r.URL.Query()
	res, err := h.svc.List(r.Context(), user, q.Get("active") == "true", q.Get("exercises") != "false", application.Page{Limit: limit, Cursor: r.URL.Query().Get("cursor")})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		Items      []majorDTO `json:"items"`
		NextCursor string     `json:"nextCursor,omitempty"`
	}{Items: make([]majorDTO, 0, len(res.Items)), NextCursor: res.NextCursor}
	for _, m := range res.Items {
		out.Items = append(out.Items, toMajorPublic(m))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *majorHandler) get(w http.ResponseWriter, r *http.Request) {
	user, manage := me(r)
	d, err := h.svc.Get(r.Context(), user, manage, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type upd struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		Body      string `json:"body"`
		CreatedAt string `json:"createdAt"`
	}
	type ref struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	type tk struct {
		ID        string `json:"id"`
		Reference string `json:"reference"`
		Title     string `json:"title"`
		Status    string `json:"status"`
		Priority  string `json:"priority"`
	}
	out := struct {
		majorDTO
		Updates           []upd    `json:"updates"`
		Tickets_          []tk     `json:"tickets"`
		AllowedOperations []string `json:"allowedOperations"`
		Locations         []ref    `json:"locations"`
		// HiddenTickets and OwnerName are staff information (majorincidents.manage only).
		HiddenTickets int    `json:"hiddenLinkedTickets,omitempty"`
		OwnerName     string `json:"ownerName,omitempty"`
	}{majorDTO: toMajor(d.Incident, manage), Updates: make([]upd, 0, len(d.Updates)), Tickets_: make([]tk, 0, len(d.Tickets)), AllowedOperations: d.Operations,
		Locations: make([]ref, 0, len(d.Locations)), HiddenTickets: d.HiddenTickets}
	if d.Owner != nil {
		out.OwnerName = d.Owner.Name
	}
	for _, l := range d.Locations {
		out.Locations = append(out.Locations, ref{ID: l.ID, Name: l.Name})
	}
	for _, t := range d.Tickets {
		out.Tickets_ = append(out.Tickets_, tk{ID: t.ID, Reference: t.Reference, Title: t.Title, Status: t.Status, Priority: t.Priority})
	}
	for _, u := range d.Updates {
		out.Updates = append(out.Updates, upd{ID: u.ID, Status: u.Status, Body: u.Body, CreatedAt: ts(u.CreatedAt)})
	}
	httpx.JSON(w, http.StatusOK, out)
}

const maxDeclareTickets = 50

// declare opens the incident and then links the named tickets one by one (each link is its own audited operation
// with its own Queue access check). Tickets that could not be linked are returned in notLinkedTicketIds; the
// incident exists either way.
func (h *majorHandler) declare(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Title      string   `json:"title"`
		Message    string   `json:"message"`
		TicketIDs  []string `json:"ticketIds"`
		IsExercise bool     `json:"isExercise"`
	}
	if !mdecode(w, r, &b) {
		return
	}
	if len(b.TicketIDs) > maxDeclareTickets {
		httpx.WriteError(w, http.StatusBadRequest, "tickets.invalid_request", "At most 50 tickets can be linked when declaring an incident.")
		return
	}
	_, manage := me(r)
	c := mcaller(w, r)
	m, err := h.svc.Declare(r.Context(), c, manage, b.Title, b.Message, b.IsExercise)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	notLinked := []string{}
	for _, id := range b.TicketIDs {
		if err := h.svc.LinkTicket(r.Context(), c, manage, m.ID, id); err != nil {
			notLinked = append(notLinked, id)
		}
	}
	if len(b.TicketIDs) > 0 {
		if cur, err := h.svc.Get(r.Context(), c.Actor.UserID, manage, m.ID); err == nil {
			m = cur.Incident
		}
	}
	httpx.JSON(w, http.StatusCreated, struct {
		majorDTO
		NotLinked []string `json:"notLinkedTicketIds"`
	}{toMajor(m, manage), notLinked})
}

func (h *majorHandler) update(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Message string `json:"message"`
	}
	if !mdecode(w, r, &b) {
		return
	}
	_, manage := me(r)
	m, err := h.svc.PostUpdate(r.Context(), mcaller(w, r), manage, r.PathValue("id"), b.Message)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toMajor(m, manage))
}

func (h *majorHandler) link(w http.ResponseWriter, r *http.Request) {
	var b struct {
		TicketID string `json:"ticketId"`
	}
	if !mdecode(w, r, &b) {
		return
	}
	_, manage := me(r)
	if err := h.svc.LinkTicket(r.Context(), mcaller(w, r), manage, r.PathValue("id"), b.TicketID); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *majorHandler) unlink(w http.ResponseWriter, r *http.Request) {
	_, manage := me(r)
	if err := h.svc.UnlinkTicket(r.Context(), mcaller(w, r), manage, r.PathValue("id"), r.PathValue("ticketId")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *majorHandler) subscribe(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, _ := me(r)
		if err := h.svc.Subscribe(r.Context(), mcaller(w, r), user, r.PathValue("id"), on); err != nil {
			h.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *majorHandler) transition(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			ExpectedVersion *int   `json:"expectedVersion"`
			Message         string `json:"message"`
		}
		if !mdecode(w, r, &b) {
			return
		}
		_, manage := me(r)
		m, err := h.svc.Transition(r.Context(), mcaller(w, r), manage, r.PathValue("id"), b.ExpectedVersion, op, b.Message)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toMajor(m, manage))
	}
}

func (h *majorHandler) setOwner(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int    `json:"expectedVersion"`
		OwnerUserID     *string `json:"ownerUserId"`
	}
	if !mdecode(w, r, &b) {
		return
	}
	_, manage := me(r)
	m, err := h.svc.SetOwner(r.Context(), mcaller(w, r), manage, r.PathValue("id"), b.ExpectedVersion, b.OwnerUserID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toMajor(m, manage))
}

func (h *majorHandler) setNextUpdate(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int       `json:"expectedVersion"`
		DueAt           *time.Time `json:"dueAt"`
	}
	if !mdecode(w, r, &b) {
		return
	}
	_, manage := me(r)
	m, err := h.svc.SetNextUpdate(r.Context(), mcaller(w, r), manage, r.PathValue("id"), b.ExpectedVersion, b.DueAt)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toMajor(m, manage))
}

func (h *majorHandler) setLocations(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int     `json:"expectedVersion"`
		LocationIDs     []string `json:"locationIds"`
	}
	if !mdecode(w, r, &b) {
		return
	}
	_, manage := me(r)
	m, err := h.svc.SetLocations(r.Context(), mcaller(w, r), manage, r.PathValue("id"), b.ExpectedVersion, b.LocationIDs)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toMajor(m, manage))
}
