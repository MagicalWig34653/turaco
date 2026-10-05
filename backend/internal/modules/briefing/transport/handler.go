// Package transport exposes the IT Briefing HTTP API under /api/v1.
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/briefing/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const (
	permView   = "briefing.view"
	permManage = "briefing.manage"
	maxBody    = 32 << 10
)

type handler struct {
	svc    *application.Service
	feed   *application.FeedService
	logger *slog.Logger
}

// Register mounts the briefing routes. Route permissions are the outer gate;
// the service decides what each caller sees.
func Register(mux *http.ServeMux, svc *application.Service, auth authorization.Authenticator, logger *slog.Logger, feed ...*application.FeedService) {
	h := &handler{svc: svc, logger: logger}
	if len(feed) > 0 {
		h.feed = feed[0]
	}
	anyBriefing := authorization.RequireAny(auth, permView, permManage)
	manage := authorization.Require(auth, permManage)
	route := func(pattern string, mw func(http.Handler) http.Handler, fn http.HandlerFunc) {
		mux.Handle(pattern, httpx.NoStore(mw(fn)))
	}
	route("GET /api/v1/briefing-items", anyBriefing, h.list)
	if h.feed != nil {
		route("GET /api/v1/briefing/feed", authorization.RequireAny(auth, feedPermissions()...), h.getFeed)
	}
	route("POST /api/v1/briefing-items", manage, h.create)
	route("GET /api/v1/briefing-items/{id}", anyBriefing, h.get)
	route("PATCH /api/v1/briefing-items/{id}", manage, h.update)
	route("DELETE /api/v1/briefing-items/{id}", manage, h.delete)
	route("POST /api/v1/briefing-items/{id}/publish", manage, h.publish)
	route("POST /api/v1/briefing-items/{id}/withdraw", manage, h.withdraw)
}

// A single permission table controls both the route gate and source scopes.
type feedPermission struct {
	name  string
	apply func(*application.FeedPrincipal)
}

var feedPermissionTable = []feedPermission{
	{permView, func(p *application.FeedPrincipal) { p.Briefing = true }},
	{permManage, func(p *application.FeedPrincipal) { p.Briefing = true }},
	{"security.view", func(p *application.FeedPrincipal) { p.Security = true }},
	{"planning.view", func(p *application.FeedPrincipal) { p.Planning = true }},
	{"planning.manage", func(p *application.FeedPrincipal) { p.Planning = true }},
	{"changes.view", func(p *application.FeedPrincipal) { p.Changes = true }},
	{"changes.manage", func(p *application.FeedPrincipal) { p.Changes = true }},
	{"changes.execute", func(p *application.FeedPrincipal) { p.Changes = true }},
	{"tickets.view", func(p *application.FeedPrincipal) { p.Desk = true; p.Tickets = true }},
	{"tickets.manage", func(p *application.FeedPrincipal) { p.Desk = true; p.Tickets = true; p.Autotask = true }},
	{"majorincidents.manage", func(p *application.FeedPrincipal) { p.Desk = true }},
	{"endpoints.manage", func(p *application.FeedPrincipal) { p.Endpoints = true; p.Directory = true }},
	{"integrations.intune.manage", func(p *application.FeedPrincipal) { p.Endpoints = true; p.Directory = true }},
	{"organization.directory.sync", func(p *application.FeedPrincipal) { p.Directory = true }},
}

func feedPermissions() []string {
	out := make([]string, 0, len(feedPermissionTable))
	for _, permission := range feedPermissionTable {
		out = append(out, permission.name)
	}
	return out
}

func feedPrincipal(userID string, has func(string) bool) application.FeedPrincipal {
	p := application.FeedPrincipal{UserID: userID}
	for _, permission := range feedPermissionTable {
		if has(permission.name) {
			permission.apply(&p)
		}
	}
	return p
}

func (h *handler) getFeed(w http.ResponseWriter, r *http.Request) {
	p, _ := authorization.PrincipalFrom(r.Context())
	result, err := h.feed.Feed(r.Context(), feedPrincipal(p.UserID, p.Has), time.Now())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, result)
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	var tr *application.InvalidTransitionError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "briefing.invalid_request", inv.Message)
	case errors.As(err, &tr):
		httpx.WriteError(w, http.StatusConflict, "briefing.invalid_transition", "The operation is not allowed in the item's current status.")
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "briefing.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "briefing.version_conflict", "The item was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "briefing.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "briefing request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
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
		httpx.WriteError(w, http.StatusBadRequest, "briefing.invalid_request", "The request body is not valid JSON for this operation.")
		return false
	}
	return true
}

type itemDTO struct {
	ID                string  `json:"id"`
	Title             string  `json:"title"`
	Body              string  `json:"body"`
	Severity          string  `json:"severity"`
	Status            string  `json:"status"`
	ValidUntil        *string `json:"validUntil"`
	AuthorUserID      *string `json:"authorUserId"`
	PublishedAt       *string `json:"publishedAt"`
	PublishedByUserID *string `json:"publishedByUserId"`
	WithdrawnAt       *string `json:"withdrawnAt"`
	WithdrawnByUserID *string `json:"withdrawnByUserId"`
	Version           int     `json:"version"`
	CreatedAt         string  `json:"createdAt"`
	UpdatedAt         string  `json:"updatedAt"`
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func tsPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := ts(*t)
	return &s
}

func toItem(it application.Item) itemDTO {
	return itemDTO{
		ID: it.ID, Title: it.Title, Body: it.Body, Severity: it.Severity, Status: it.Status,
		ValidUntil: tsPtr(it.ValidUntil), AuthorUserID: it.AuthorUserID, PublishedAt: tsPtr(it.PublishedAt),
		PublishedByUserID: it.PublishedByUserID, WithdrawnAt: tsPtr(it.WithdrawnAt), WithdrawnByUserID: it.WithdrawnByUserID,
		Version: it.Version, CreatedAt: ts(it.CreatedAt), UpdatedAt: ts(it.UpdatedAt),
	}
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "briefing.invalid_limit", "The limit must be a positive integer.")
		return
	}
	res, err := h.svc.List(r.Context(), principal(r), r.URL.Query().Get("status"),
		application.Page{Limit: limit, Cursor: r.URL.Query().Get("cursor")})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		Items      []itemDTO `json:"items"`
		NextCursor string    `json:"nextCursor,omitempty"`
	}{Items: make([]itemDTO, 0, len(res.Items)), NextCursor: res.NextCursor}
	for _, it := range res.Items {
		out.Items = append(out.Items, toItem(it))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	it, err := h.svc.Get(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toItem(it))
}

type createBody struct {
	Title      string     `json:"title"`
	Body       string     `json:"body"`
	Severity   string     `json:"severity"`
	ValidUntil *time.Time `json:"validUntil"`
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var b createBody
	if !decode(w, r, &b) {
		return
	}
	it, err := h.svc.Create(r.Context(), caller(w, r), principal(r), application.CreateInput{
		Title: b.Title, Body: b.Body, Severity: b.Severity, ValidUntil: b.ValidUntil,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toItem(it))
}

type updateBody struct {
	ExpectedVersion *int       `json:"expectedVersion"`
	Title           *string    `json:"title"`
	Body            *string    `json:"body"`
	Severity        *string    `json:"severity"`
	ValidUntil      *time.Time `json:"validUntil"`
	ClearValidUntil bool       `json:"clearValidUntil"`
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	var b updateBody
	if !decode(w, r, &b) {
		return
	}
	if b.ExpectedVersion == nil {
		httpx.WriteError(w, http.StatusBadRequest, "briefing.invalid_request", "expectedVersion is required to change an item.")
		return
	}
	it, err := h.svc.Update(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, application.UpdateInput{
		Title: b.Title, Body: b.Body, Severity: b.Severity, ValidUntil: b.ValidUntil, ClearValidUntil: b.ClearValidUntil,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toItem(it))
}

type versionBody struct {
	ExpectedVersion *int `json:"expectedVersion"`
}

func (h *handler) publish(w http.ResponseWriter, r *http.Request) {
	var b versionBody
	if !decode(w, r, &b) {
		return
	}
	it, err := h.svc.Publish(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toItem(it))
}

func (h *handler) withdraw(w http.ResponseWriter, r *http.Request) {
	var b versionBody
	if !decode(w, r, &b) {
		return
	}
	it, err := h.svc.Withdraw(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toItem(it))
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) {
	var expected *int
	if v := r.URL.Query().Get("expectedVersion"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			httpx.WriteError(w, http.StatusBadRequest, "briefing.invalid_request", "The expected version must be a positive integer.")
			return
		}
		expected = &n
	}
	if err := h.svc.Delete(r.Context(), caller(w, r), principal(r), r.PathValue("id"), expected); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
