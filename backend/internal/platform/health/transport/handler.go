// Package transport exposes the administration health pages: GET /api/v1/admin/health, /integrations, /system and
// the setup checklist at /api/v1/admin/setup.
package transport

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/health"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const (
	permHealthView = "platform.health.view"
	permAdmin      = "platform.admin"
)

// SystemInfo is the static part of GET /admin/system.
type SystemInfo struct {
	Version     string
	Commit      string
	Environment string
	StartedAt   time.Time
}

type handler struct {
	reg    *health.Registry
	setup  *health.Setup
	info   SystemInfo
	logger *slog.Logger
}

// Register mounts the routes; all need platform.health.view, and changing a setup item needs platform.admin.
func Register(mux *http.ServeMux, reg *health.Registry, setup *health.Setup, info SystemInfo, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{reg: reg, setup: setup, info: info, logger: logger}
	view := authorization.Require(auth, permHealthView)
	mux.Handle("GET /api/v1/admin/health", httpx.NoStore(view(http.HandlerFunc(h.health))))
	mux.Handle("GET /api/v1/admin/integrations", httpx.NoStore(view(http.HandlerFunc(h.integrations))))
	mux.Handle("GET /api/v1/admin/system", httpx.NoStore(view(http.HandlerFunc(h.system))))
	mux.Handle("GET /api/v1/admin/setup", httpx.NoStore(view(http.HandlerFunc(h.listSetup))))
	mux.Handle("PUT /api/v1/admin/setup/items/{key}", httpx.NoStore(view(http.HandlerFunc(h.putSetup))))
}

func summary(entries []health.Entry) map[string]int {
	out := map[string]int{"attention": 0, "total": len(entries)}
	for _, e := range entries {
		if e.Status.Attention() {
			out["attention"]++
		}
	}
	return out
}

func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	entries := h.reg.All(r.Context())
	httpx.JSON(w, http.StatusOK, map[string]any{"items": entries, "summary": summary(entries)})
}

func (h *handler) integrations(w http.ResponseWriter, r *http.Request) {
	out := []health.Entry{}
	for _, e := range h.reg.All(r.Context()) {
		if e.Category == health.CategoryIntegration {
			out = append(out, e)
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out, "summary": summary(out)})
}

func (h *handler) system(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"version": h.info.Version, "environment": h.info.Environment, "startedAt": h.info.StartedAt.UTC()}
	if h.info.Commit != "" {
		out["commit"] = h.info.Commit
	}
	for _, e := range h.reg.All(r.Context()) {
		switch e.Key {
		case "database":
			out["database"] = map[string]any{"status": e.Status, "serverVersion": e.Detail["serverVersion"]}
		case "migrations":
			out["migrations"] = map[string]any{"status": e.Status, "latestVersion": e.Detail["latestVersion"], "latestName": e.Detail["latestName"], "applied": e.Counts["applied"]}
		case "modules":
			out["modules"] = e.Counts
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) listSetup(w http.ResponseWriter, r *http.Request) {
	list, err := h.setup.List(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, list)
}

func (h *handler) putSetup(w http.ResponseWriter, r *http.Request) {
	p, _ := authorization.PrincipalFrom(r.Context())
	var body struct {
		State           string `json:"state"`
		Reason          string `json:"reason"`
		ExpectedVersion *int   `json:"expectedVersion"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "health.invalid_request", "The request body is invalid.")
		return
	}
	item, err := h.setup.Put(r.Context(), p.UserID, p.Has(permAdmin), r.PathValue("key"), body.State, body.Reason, body.ExpectedVersion, httpx.RequestID(w))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, item)
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, health.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, health.ErrUnknownItem):
		httpx.WriteError(w, http.StatusNotFound, "health.not_found", "The setup item does not exist.")
	case errors.Is(err, health.ErrInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "health.invalid_request", "The state or reason is invalid (skipping needs a reason).")
	case errors.Is(err, health.ErrNotConfirmable):
		httpx.WriteError(w, http.StatusConflict, "health.not_confirmable", "This item is derived from data and cannot be confirmed.")
	case errors.Is(err, health.ErrConflict):
		httpx.WriteError(w, http.StatusConflict, "health.version_conflict", "The item changed meanwhile; reload and try again.")
	default:
		h.logger.ErrorContext(r.Context(), "health request failed", "request_id", httpx.RequestID(w), "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}
