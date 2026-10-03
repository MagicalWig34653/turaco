// Package transport exposes the Knowledge HTTP API under /api/v1. Every
// signed-in user reads published employee articles; knowledge.view also reads
// published internal ones; knowledge.manage writes and sees everything.
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const (
	permView   = "knowledge.view"
	permManage = "knowledge.manage"
	maxBody    = 40 << 10
)

type handler struct {
	svc    *application.Service
	logger *slog.Logger
}

// Register mounts the knowledge routes.
func Register(mux *http.ServeMux, svc *application.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	authed := authorization.RequireAuthenticated(auth)
	route := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, httpx.NoStore(authed(fn))) }
	route("GET /api/v1/knowledge-articles", h.list)
	route("POST /api/v1/knowledge-articles", h.create)
	route("GET /api/v1/knowledge-articles/{id}", h.get)
	route("PATCH /api/v1/knowledge-articles/{id}", h.update)
	route("POST /api/v1/knowledge-articles/{id}/publish", h.move(true))
	route("POST /api/v1/knowledge-articles/{id}/retire", h.move(false))
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	var tr *application.InvalidTransitionError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "knowledge.invalid_request", inv.Message)
	case errors.As(err, &tr):
		httpx.WriteError(w, http.StatusConflict, "knowledge.invalid_transition", "The operation is not allowed in the article's current status.")
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "knowledge.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "knowledge.version_conflict", "The article was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "knowledge.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "knowledge request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
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
		httpx.WriteError(w, http.StatusBadRequest, "knowledge.invalid_request", "The request body is not valid JSON for this operation.")
		return false
	}
	return true
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

type articleDTO struct {
	ID          string  `json:"id"`
	Reference   string  `json:"reference"`
	Title       string  `json:"title"`
	Summary     string  `json:"summary"`
	Body        *string `json:"body,omitempty"`
	Audience    string  `json:"audience"`
	Status      string  `json:"status"`
	PublishedAt *string `json:"publishedAt"`
	Version     int     `json:"version"`
	UpdatedAt   string  `json:"updatedAt"`
}

func toArticle(a application.Article, withBody bool) articleDTO {
	d := articleDTO{ID: a.ID, Reference: a.Reference, Title: a.Title, Summary: a.Summary, Audience: a.Audience, Status: a.Status, Version: a.Version, UpdatedAt: ts(a.UpdatedAt)}
	if a.PublishedAt != nil {
		s := ts(*a.PublishedAt)
		d.PublishedAt = &s
	}
	if withBody {
		d.Body = &a.Body
	}
	return d
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "knowledge.invalid_limit", "The limit must be a positive integer.")
		return
	}
	v := r.URL.Query()
	res, err := h.svc.List(r.Context(), principal(r), v.Get("q"), v.Get("status"), application.Page{Limit: limit, Cursor: v.Get("cursor")})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		Items      []articleDTO `json:"items"`
		NextCursor string       `json:"nextCursor,omitempty"`
	}{Items: make([]articleDTO, 0, len(res.Items)), NextCursor: res.NextCursor}
	for _, a := range res.Items {
		out.Items = append(out.Items, toArticle(a, false))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	a, err := h.svc.Get(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toArticle(a, true))
}

type inputBody struct {
	ExpectedVersion *int   `json:"expectedVersion"`
	Title           string `json:"title"`
	Summary         string `json:"summary"`
	Body            string `json:"body"`
	Audience        string `json:"audience"`
}

func (b inputBody) input() application.Input {
	return application.Input{Title: b.Title, Summary: b.Summary, Body: b.Body, Audience: b.Audience}
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var b inputBody
	if !decode(w, r, &b) {
		return
	}
	a, err := h.svc.Create(r.Context(), caller(w, r), principal(r), b.input())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toArticle(a, true))
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	var b inputBody
	if !decode(w, r, &b) {
		return
	}
	if b.ExpectedVersion == nil {
		httpx.WriteError(w, http.StatusBadRequest, "knowledge.invalid_request", "expectedVersion is required.")
		return
	}
	a, err := h.svc.Update(r.Context(), caller(w, r), principal(r), r.PathValue("id"), *b.ExpectedVersion, b.input())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toArticle(a, true))
}

func (h *handler) move(publishing bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			ExpectedVersion *int `json:"expectedVersion"`
		}
		if !decode(w, r, &b) {
			return
		}
		var a application.Article
		var err error
		if publishing {
			a, err = h.svc.Publish(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
		} else {
			a, err = h.svc.Retire(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
		}
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toArticle(a, true))
	}
}
