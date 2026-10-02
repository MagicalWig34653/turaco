// Package transport exposes the caller's own in-app notifications under
// /api/v1/notifications. Every authenticated user may use it; a user only
// ever sees and changes their own notifications.
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

type handler struct {
	svc    *notifications.Service
	logger *slog.Logger
}

// Register mounts the notification routes.
func Register(mux *http.ServeMux, svc *notifications.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	authed := authorization.RequireAuthenticated(auth)
	route := func(pattern string, fn http.HandlerFunc) {
		mux.Handle(pattern, httpx.NoStore(authed(fn)))
	}
	route("GET /api/v1/notifications", h.list)
	route("GET /api/v1/notifications/unread-count", h.unreadCount)
	route("POST /api/v1/notifications/read-all", h.readAll)
	route("POST /api/v1/notifications/{id}/read", h.read)
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, notifications.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "notifications.not_found", "The requested resource was not found.")
	case errors.Is(err, notifications.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "notifications.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "notification request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func userID(r *http.Request) string {
	p, _ := authorization.PrincipalFrom(r.Context())
	return p.UserID
}

type notificationDTO struct {
	ID        string         `json:"id"`
	Category  string         `json:"category"`
	Params    map[string]any `json:"params"`
	LinkType  *string        `json:"linkType"`
	LinkID    *string        `json:"linkId"`
	CreatedAt string         `json:"createdAt"`
	ReadAt    *string        `json:"readAt"`
}

type listResponse struct {
	Items      []notificationDTO `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "notifications.invalid_limit", "The limit must be a positive integer.")
		return
	}
	unread := false
	switch r.URL.Query().Get("unread") {
	case "":
	case "true":
		unread = true
	default:
		httpx.WriteError(w, http.StatusBadRequest, "notifications.invalid_request", "The unread filter must be true.")
		return
	}
	res, err := h.svc.List(r.Context(), userID(r), unread, notifications.Page{Limit: limit, Cursor: r.URL.Query().Get("cursor")})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := listResponse{Items: make([]notificationDTO, 0, len(res.Items)), NextCursor: res.NextCursor}
	for _, n := range res.Items {
		dto := notificationDTO{
			ID: n.ID, Category: n.Category, Params: n.Params, LinkType: n.LinkType, LinkID: n.LinkID,
			CreatedAt: n.CreatedAt.UTC().Format(time.RFC3339),
		}
		if dto.Params == nil {
			dto.Params = map[string]any{}
		}
		if n.ReadAt != nil {
			s := n.ReadAt.UTC().Format(time.RFC3339)
			dto.ReadAt = &s
		}
		out.Items = append(out.Items, dto)
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) unreadCount(w http.ResponseWriter, r *http.Request) {
	n, err := h.svc.UnreadCount(r.Context(), userID(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"count": n, "max": notifications.MaxUnreadCount})
}

func (h *handler) read(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.MarkRead(r.Context(), userID(r), r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) readAll(w http.ResponseWriter, r *http.Request) {
	n, err := h.svc.MarkAllRead(r.Context(), userID(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"marked": n})
}
