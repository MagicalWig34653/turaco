// Package transport exposes the module registry: GET /api/v1/modules/status for every signed-in User and the
// administration under /api/v1/admin/modules (permission modules.manage). Responses are never cacheable.
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/modules"
)

const maxBody = 4 << 10

type handler struct {
	svc    *modules.Service
	logger *slog.Logger
}

// Register mounts the module routes.
func Register(mux *http.ServeMux, svc *modules.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	signedIn := authorization.RequireAuthenticated(auth)
	manage := authorization.Require(auth, modules.PermManage)
	mux.Handle("GET /api/v1/modules/status", httpx.NoStore(signedIn(http.HandlerFunc(h.status))))
	mux.Handle("GET /api/v1/admin/modules", httpx.NoStore(manage(http.HandlerFunc(h.list))))
	mux.Handle("POST /api/v1/admin/modules/{key}/enable", httpx.NoStore(manage(http.HandlerFunc(h.change(true)))))
	mux.Handle("POST /api/v1/admin/modules/{key}/disable", httpx.NoStore(manage(http.HandlerFunc(h.change(false)))))
}

type statusItem struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
}

func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	infos, err := h.svc.Status(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]statusItem, len(infos))
	for i, in := range infos {
		out[i] = statusItem{Key: in.Module.Key, Enabled: in.Enabled}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

type moduleView struct {
	Key             string   `json:"key"`
	NameKey         string   `json:"nameKey"`
	DescriptionKey  string   `json:"descriptionKey"`
	Category        string   `json:"category"`
	Core            bool     `json:"core"`
	Enabled         bool     `json:"enabled"`
	SwitchOn        bool     `json:"switchOn"`
	State           string   `json:"state"`
	BlockedReason   *string  `json:"blockedReason"`
	Requires        []string `json:"requires"`
	RequiredBy      []string `json:"requiredBy"`
	StartupGates    []string `json:"startupGates"`
	Version         int      `json:"version"`
	ChangedAt       *string  `json:"changedAt"`
	ChangedByUserID *string  `json:"changedByUserId"`
	ReasonCode      *string  `json:"reasonCode"`
}

func (h *handler) view(in modules.Info) moduleView {
	m := in.Module
	v := moduleView{Key: m.Key, NameKey: m.NameKey(), DescriptionKey: m.DescriptionKey(), Category: m.Category, Core: m.Core,
		Enabled: in.Enabled, SwitchOn: in.SwitchOn, State: in.State, Requires: nonNil(m.Requires),
		RequiredBy: nonNil(h.svc.Index().RequiredBy(m.Key)), StartupGates: nonNil(m.StartupGates), Version: in.Version, ChangedByUserID: in.ChangedBy}
	if in.BlockedReason != "" {
		v.BlockedReason = &in.BlockedReason
	}
	if in.ChangedAt != nil {
		t := in.ChangedAt.UTC().Format(time.RFC3339)
		v.ChangedAt = &t
	}
	if in.ReasonCode != "" {
		v.ReasonCode = &in.ReasonCode
	}
	return v
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (h *handler) caller(w http.ResponseWriter, r *http.Request) modules.Caller {
	p, _ := authorization.PrincipalFrom(r.Context())
	return modules.Caller{UserID: p.UserID, CorrelationID: httpx.RequestID(w), CanManage: p.Has(modules.PermManage)}
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	infos, err := h.svc.List(r.Context(), h.caller(w, r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]moduleView, len(infos))
	for i, in := range infos {
		out[i] = h.view(in)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

type changeBody struct {
	ExpectedVersion *int   `json:"expectedVersion"`
	ReasonCode      string `json:"reasonCode"`
}

func (h *handler) change(enable bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body changeBody
		if err := httpx.DecodeJSON(w, r, &body, maxBody); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "platform.modules.invalid_request", "The request body is not valid JSON for this operation.")
			return
		}
		op := h.svc.Disable
		if enable {
			op = h.svc.Enable
		}
		in, err := op(r.Context(), h.caller(w, r), r.PathValue("key"), body.ReasonCode, body.ExpectedVersion)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, h.view(in))
	}
}

type blockerEnvelope struct {
	Error struct {
		Code      string   `json:"code"`
		Message   string   `json:"message"`
		RequestID string   `json:"requestId,omitempty"`
		Blockers  []string `json:"blockers"`
	} `json:"error"`
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *modules.InvalidError
	var dep *modules.DependencyError
	var blocked *modules.BlockedError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "platform.modules.invalid_request", inv.Message)
	case errors.Is(err, modules.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, modules.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "platform.modules.not_found", "The requested module was not found.")
	case errors.Is(err, modules.ErrNotSwitchable):
		httpx.WriteError(w, http.StatusConflict, "platform.modules.not_switchable", "Core modules are always on and cannot be switched.")
	case errors.Is(err, modules.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "platform.modules.version_conflict", "The module was changed by someone else; reload and try again.")
	case errors.Is(err, modules.ErrNoChange):
		httpx.WriteError(w, http.StatusConflict, "platform.modules.no_change", "The module is already in the requested state.")
	case errors.As(err, &blocked):
		httpx.WriteError(w, http.StatusConflict, "platform.modules.blocked", "The module cannot be enabled yet: "+blocked.Reason+".")
	case errors.As(err, &dep):
		var env blockerEnvelope
		env.Error.RequestID = httpx.RequestID(w)
		env.Error.Blockers = dep.Blockers
		if dep.Enabling {
			env.Error.Code = "platform.modules.requires_disabled"
			env.Error.Message = "Enable the required modules first: " + strings.Join(dep.Blockers, ", ") + "."
		} else {
			env.Error.Code = "platform.modules.required_by_enabled"
			env.Error.Message = "Disable the modules that depend on it first: " + strings.Join(dep.Blockers, ", ") + "."
		}
		httpx.JSON(w, http.StatusConflict, env)
	default:
		h.logger.ErrorContext(r.Context(), "module request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}
