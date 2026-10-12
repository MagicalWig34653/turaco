package setup

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authentication"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

// Limiter is the attempt counter (authentication.Throttle).
type Limiter interface {
	Reserve(ctx context.Context, key string, limit int) (bool, time.Duration, error)
}

// Handler serves the unauthenticated setup endpoints.
type Handler struct {
	Store          *Store
	Granter        Granter
	Limiter        Limiter
	TrustedProxies []netip.Prefix
	Logger         *slog.Logger
}

const (
	clientAttemptLimit = 10
	globalAttemptLimit = 30
	maxBody            = 4 << 10
	// codeClosed is the one answer for every failure before the token is accepted.
	codeClosed = "setup.unavailable"
)

var guidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Register adds GET /api/v1/setup/status and POST /api/v1/setup/first-administrator.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/setup/status", httpx.NoStore(http.HandlerFunc(h.status)))
	mux.Handle("POST /api/v1/setup/first-administrator", httpx.NoStore(authentication.RequireSameOrigin(http.HandlerFunc(h.firstAdministrator))))
}

// status tells only whether the page is open: no administrator exists and an unexpired token was issued.
func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	available, err := h.available(r.Context())
	if err != nil {
		h.Logger.Error("setup status", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "Internal error.")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]bool{"available": available})
}

func (h *Handler) available(ctx context.Context) (bool, error) {
	exists, err := h.Granter.AdministratorExists(ctx)
	if err != nil || exists {
		return false, err
	}
	return h.Store.OpenTokenExists(ctx)
}

type firstAdminRequest struct {
	Token  string `json:"token"`
	User   string `json:"user"`
	Tenant string `json:"tenant"`
	Object string `json:"object"`
}

func (h *Handler) firstAdministrator(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req firstAdminRequest
	if err := httpx.DecodeJSON(w, r, &req, maxBody); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "setup.invalid_request", "The request is invalid.")
		return
	}
	// Reserve attempts before any check: per client and for the installation as a whole.
	for _, k := range []struct {
		key   string
		limit int
	}{{"setup:" + authentication.ClientKey(authentication.ClientIP(r, h.TrustedProxies)), clientAttemptLimit}, {"setup:all", globalAttemptLimit}} {
		allowed, retry, err := h.Limiter.Reserve(ctx, k.key, k.limit)
		if err != nil {
			h.Logger.Error("setup throttle", "error", err)
			httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "Internal error.")
			return
		}
		if !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retry.Seconds())+1)))
			httpx.WriteError(w, http.StatusTooManyRequests, "setup.rate_limited", "Too many attempts. Try again later.")
			return
		}
	}
	open, err := h.available(ctx)
	if err != nil {
		h.Logger.Error("setup availability", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "Internal error.")
		return
	}
	valid := false
	if open {
		if valid, err = h.Store.Valid(ctx, req.Token); err != nil {
			h.Logger.Error("setup token check", "error", err)
			httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "Internal error.")
			return
		}
	}
	if !valid {
		h.Logger.Warn("setup attempt rejected")
		httpx.WriteError(w, http.StatusForbidden, codeClosed, "Setup is not available or the token is not valid.")
		return
	}
	// The operator is trusted from here on: specific answers help them correct the input.
	if req.User == "" || len(req.User) > 320 || (req.Tenant == "") != (req.Object == "") ||
		(req.Tenant != "" && (!guidPattern.MatchString(req.Tenant) || !guidPattern.MatchString(req.Object))) {
		httpx.WriteError(w, http.StatusBadRequest, "setup.invalid_request", "The request is invalid.")
		return
	}
	_, err = h.Granter.Grant(ctx, Grant{
		User: req.User, Tenant: req.Tenant, Object: req.Object,
		Actor: audit.SystemActor("setup-wizard"), CorrelationID: httpx.RequestID(w), Via: "web",
	})
	switch {
	case errors.Is(err, ErrAdministratorExists), errors.Is(err, ErrAlreadyAdministrator):
		httpx.WriteError(w, http.StatusForbidden, codeClosed, "Setup is not available or the token is not valid.")
		return
	case errors.Is(err, ErrUserNotFound):
		httpx.WriteError(w, http.StatusUnprocessableEntity, "setup.user_not_found", "The user was not found. Wait for the first directory sync or check the name.")
		return
	case errors.Is(err, ErrLocalAccount):
		httpx.WriteError(w, http.StatusUnprocessableEntity, "setup.local_account", "A local account cannot be the first administrator.")
		return
	case errors.Is(err, ErrEntraLink):
		httpx.WriteError(w, http.StatusUnprocessableEntity, "setup.entra_link_failed", "The Entra identity could not be linked to this user.")
		return
	case err != nil:
		h.Logger.Error("setup first administrator", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "Internal error.")
		return
	}
	if _, err := h.Store.Consume(ctx, req.Token); err != nil {
		h.Logger.Error("setup consume token", "error", err)
	}
	httpx.JSON(w, http.StatusOK, map[string]bool{"created": true})
}
