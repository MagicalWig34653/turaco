// Package transport exposes My Work items and counts under /api/v1/my-work (ADR-0033). The routes belong to the
// always-on Tasks module prefix "my-work"; each source is skipped while its own module is off.
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/workitems"
)

type handler struct {
	svc    *workitems.Service
	logger *slog.Logger
}

// Register mounts the routes. Every route requires a signed-in User; the sources authorize per item.
func Register(mux *http.ServeMux, svc *workitems.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	authed := authorization.RequireAuthenticated(auth)
	mux.Handle("GET /api/v1/my-work/items", httpx.NoStore(authed(http.HandlerFunc(h.items))))
	mux.Handle("GET /api/v1/my-work/counts", httpx.NoStore(authed(http.HandlerFunc(h.counts))))
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *workitems.InvalidError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "mywork.invalid_request", inv.Message)
	case errors.Is(err, workitems.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "mywork.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "my work request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func sources(r *http.Request) []string {
	raw := r.URL.Query().Get("sources")
	if raw == "" {
		return nil
	}
	return strings.Split(raw, ",")
}

type itemDTO struct {
	ID            string  `json:"id"`
	Source        string  `json:"source"`
	Kind          string  `json:"kind"`
	Title         string  `json:"title"`
	Reference     string  `json:"reference,omitempty"`
	Status        string  `json:"status"`
	WaitingReason string  `json:"waitingReason,omitempty"`
	Priority      string  `json:"priority"`
	DueAt         *string `json:"dueAt"`
	UpdatedAt     string  `json:"updatedAt"`
	Href          string  `json:"href"`
}

func (h *handler) items(w http.ResponseWriter, r *http.Request) {
	p, _ := authorization.PrincipalFrom(r.Context())
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			httpx.WriteError(w, http.StatusBadRequest, "mywork.invalid_request", "limit must be a positive number.")
			return
		}
		limit = n
	}
	page, err := h.svc.Items(r.Context(), p, sources(r), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]itemDTO, 0, len(page.Items))
	for _, it := range page.Items {
		d := itemDTO{ID: it.ID, Source: it.Source, Kind: it.Kind, Title: it.Title, Reference: it.Reference, Status: it.Status,
			WaitingReason: it.WaitingReason, Priority: it.Priority, UpdatedAt: it.UpdatedAt.UTC().Format(time.RFC3339), Href: it.Href}
		if it.DueAt != nil {
			s := it.DueAt.UTC().Format(time.RFC3339)
			d.DueAt = &s
		}
		items = append(items, d)
	}
	unavailable := page.Unavailable
	if unavailable == nil {
		unavailable = []string{}
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items       []itemDTO `json:"items"`
		NextCursor  string    `json:"nextCursor,omitempty"`
		Unavailable []string  `json:"unavailable"`
	}{items, page.NextCursor, unavailable})
}

type countDTO struct {
	Source string `json:"source"`
	Count  *int   `json:"count,omitempty"`
	Capped bool   `json:"capped,omitempty"`
	Status string `json:"status"`
}

func (h *handler) counts(w http.ResponseWriter, r *http.Request) {
	p, _ := authorization.PrincipalFrom(r.Context())
	res, err := h.svc.Counts(r.Context(), p, sources(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]countDTO, 0, len(res))
	for _, c := range res {
		d := countDTO{Source: c.Source, Status: c.Status}
		if c.Status == workitems.CountOK {
			n := c.N
			d.Count, d.Capped = &n, c.Capped
		}
		out = append(out, d)
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []countDTO `json:"items"`
	}{out})
}
