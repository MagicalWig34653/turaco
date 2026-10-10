// Package transport exposes GET /api/v1/search for every signed-in User. The module Searchers decide what the
// caller may read; this layer validates the parameters, limits the request rate per User and never logs the query.
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/search"
)

const (
	rateWindow = 10 * time.Second
	rateBurst  = 30
)

type handler struct {
	svc    *search.Service
	logger *slog.Logger
	mu     sync.Mutex
	recent map[string][]time.Time
	now    func() time.Time
}

// Register mounts the search route.
func Register(mux *http.ServeMux, svc *search.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger, recent: map[string][]time.Time{}, now: time.Now}
	mux.Handle("GET /api/v1/search", httpx.NoStore(authorization.RequireAuthenticated(auth)(http.HandlerFunc(h.search))))
}

// allow is a sliding-window limit per User; it bounds the database work one session can trigger.
func (h *handler) allow(user string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	kept := h.recent[user][:0]
	for _, t := range h.recent[user] {
		if now.Sub(t) < rateWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) >= rateBurst {
		h.recent[user] = kept
		return false
	}
	h.recent[user] = append(kept, now)
	if len(h.recent) > 10000 { // bound memory: drop idle users
		for u, ts := range h.recent {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= rateWindow {
				delete(h.recent, u)
			}
		}
	}
	return true
}

type hitDTO struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Reference string `json:"reference,omitempty"`
	Title     string `json:"title"`
	Subtitle  string `json:"subtitle,omitempty"`
	Exact     bool   `json:"exact,omitempty"`
}

func (h *handler) search(w http.ResponseWriter, r *http.Request) {
	p, _ := authorization.PrincipalFrom(r.Context())
	v := r.URL.Query()
	req := search.Request{Query: v.Get("q")}
	if t := strings.TrimSpace(v.Get("types")); t != "" {
		req.Types = strings.Split(t, ",")
	}
	if l := v.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 || n > search.MaxPerType {
			httpx.WriteError(w, http.StatusBadRequest, "search.invalid_request", "The limit must be between 1 and 10.")
			return
		}
		req.Limit = n
	}
	if !h.allow(p.UserID) {
		w.Header().Set("Retry-After", "5")
		httpx.WriteError(w, http.StatusTooManyRequests, "search.rate_limited", "Too many searches; wait a moment.")
		return
	}
	res, err := h.svc.Search(r.Context(), p, req)
	switch {
	case errors.Is(err, search.ErrQueryLength):
		httpx.WriteError(w, http.StatusBadRequest, "search.invalid_request", "The search text must have 2 to 100 characters.")
		return
	case errors.Is(err, search.ErrUnknownType):
		httpx.WriteError(w, http.StatusBadRequest, "search.invalid_request", "Unknown search type.")
		return
	case err != nil:
		h.logger.ErrorContext(r.Context(), "search failed", "request_id", httpx.RequestID(w), "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
		return
	}
	items := make([]hitDTO, len(res.Items))
	for i, x := range res.Items {
		items[i] = hitDTO(x)
	}
	unavailable := res.Unavailable
	if unavailable == nil {
		unavailable = []string{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "unavailable": unavailable})
}
