package transport

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// PermTeamsManage manages the Teams channel routes.
const PermTeamsManage = "integrations.teams.manage"

type routesHandler struct {
	svc    *notifications.Service
	logger *slog.Logger
}

// RegisterChannelRoutes mounts the administration of Teams channel routes under /api/v1/integrations/teams. Every route needs
// integrations.teams.manage (checked here and again in the service). The routes stay reachable while the module is
// off so an administrator can prepare them.
func RegisterChannelRoutes(mux *http.ServeMux, svc *notifications.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &routesHandler{svc: svc, logger: logger}
	route := func(pattern string, fn http.HandlerFunc) {
		mux.Handle(pattern, httpx.NoStore(authorization.Require(auth, PermTeamsManage)(fn)))
	}
	route("GET /api/v1/integrations/teams/channel-routes", h.list)
	route("POST /api/v1/integrations/teams/channel-routes", h.create)
	route("DELETE /api/v1/integrations/teams/channel-routes/{id}", h.delete)
}

type routeDTO struct {
	ID             string `json:"id"`
	Category       string `json:"category"`
	DestinationKey string `json:"destinationKey"`
	CreatedBy      string `json:"createdBy"`
	CreatedAt      string `json:"createdAt"`
}

func toRouteDTO(r notifications.Route) routeDTO {
	return routeDTO{ID: r.ID, Category: r.Category, DestinationKey: r.DestinationKey, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339)}
}

func (h *routesHandler) list(w http.ResponseWriter, r *http.Request) {
	routes, err := h.svc.ListRoutes(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	info := h.svc.ChannelInfo()
	items := make([]routeDTO, 0, len(routes))
	for _, x := range routes {
		items = append(items, toRouteDTO(x))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"items": items, "mode": info.Mode, "destinations": info.Destinations, "categories": info.Categories,
	})
}

func (h *routesHandler) create(w http.ResponseWriter, r *http.Request) {
	p, _ := authorization.PrincipalFrom(r.Context())
	var body struct {
		Category       string `json:"category"`
		DestinationKey string `json:"destinationKey"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Category == "" || body.DestinationKey == "" {
		httpx.WriteError(w, http.StatusBadRequest, "notifications.invalid_request", "The request body is invalid.")
		return
	}
	route, err := h.svc.CreateRoute(r.Context(), p.UserID, p.Has(PermTeamsManage), body.Category, body.DestinationKey, httpx.RequestID(w))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toRouteDTO(route))
}

func (h *routesHandler) delete(w http.ResponseWriter, r *http.Request) {
	p, _ := authorization.PrincipalFrom(r.Context())
	if err := h.svc.DeleteRoute(r.Context(), p.UserID, p.Has(PermTeamsManage), r.PathValue("id"), httpx.RequestID(w)); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *routesHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, notifications.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, notifications.ErrNotBroadcastable):
		httpx.WriteError(w, http.StatusBadRequest, "notifications.category_not_broadcastable", "The category cannot be posted to a channel.")
	case errors.Is(err, notifications.ErrUnknownDestination):
		httpx.WriteError(w, http.StatusBadRequest, "notifications.unknown_destination", "The channel destination is not configured.")
	case errors.Is(err, notifications.ErrRouteExists):
		httpx.WriteError(w, http.StatusConflict, "notifications.route_exists", "This route already exists.")
	case errors.Is(err, notifications.ErrRouteNotFound):
		httpx.WriteError(w, http.StatusNotFound, "notifications.route_not_found", "The requested route was not found.")
	default:
		h.logger.ErrorContext(r.Context(), "channel route request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}
