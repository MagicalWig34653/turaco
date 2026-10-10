// Package transport exposes the administration settings: GET /api/v1/admin/settings and
// PUT /api/v1/admin/settings/{key}.
package transport

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/settings"
)

const (
	permHealthView = "platform.health.view"
	permAdmin      = "platform.admin"
)

type handler struct {
	svc    *settings.Service
	logger *slog.Logger
}

// Register mounts the routes. Reading needs platform.health.view; changing needs platform.admin (checked in the
// route and again in the service).
func Register(mux *http.ServeMux, svc *settings.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	mux.Handle("GET /api/v1/admin/settings", httpx.NoStore(authorization.Require(auth, permHealthView)(http.HandlerFunc(h.list))))
	mux.Handle("PUT /api/v1/admin/settings/{key}", httpx.NoStore(authorization.Require(auth, permAdmin)(http.HandlerFunc(h.put))))
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{"items": h.svc.List(r.Context())})
}

func (h *handler) put(w http.ResponseWriter, r *http.Request) {
	p, _ := authorization.PrincipalFrom(r.Context())
	var body struct {
		Value           json.RawMessage `json:"value"`
		ExpectedVersion *int            `json:"expectedVersion"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || len(body.Value) == 0 {
		httpx.WriteError(w, http.StatusBadRequest, "settings.invalid_request", "The request body is invalid.")
		return
	}
	item, err := h.svc.Set(r.Context(), p.UserID, p.Has(permAdmin), r.PathValue("key"), body.Value, body.ExpectedVersion, httpx.RequestID(w))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, item)
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, settings.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, settings.ErrUnknownKey):
		httpx.WriteError(w, http.StatusNotFound, "settings.not_found", "The setting does not exist.")
	case errors.Is(err, settings.ErrVersionRequired):
		httpx.WriteError(w, http.StatusBadRequest, "settings.version_required", "expectedVersion is required.")
	case errors.Is(err, settings.ErrInvalidValue):
		httpx.WriteError(w, http.StatusBadRequest, "settings.invalid_value", "The value does not match the setting's type or bounds.")
	case errors.Is(err, settings.ErrConflict):
		httpx.WriteError(w, http.StatusConflict, "settings.version_conflict", "The setting changed meanwhile; reload and try again.")
	default:
		h.logger.ErrorContext(r.Context(), "settings request failed", "request_id", httpx.RequestID(w), "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}
