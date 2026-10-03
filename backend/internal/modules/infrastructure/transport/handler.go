// Package transport exposes the Infrastructure HTTP API under /api/v1. Reads
// need infrastructure.view or infrastructure.manage; writes need
// infrastructure.manage. The Asset location lookup additionally needs
// assets.view or assets.manage.
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/infrastructure/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const (
	permView         = "infrastructure.view"
	permManage       = "infrastructure.manage"
	permAssetsView   = "assets.view"
	permAssetsManage = "assets.manage"
	maxBody          = 64 << 10
)

type handler struct {
	svc    *application.Service
	logger *slog.Logger
}

// Register mounts the Infrastructure routes.
func Register(mux *http.ServeMux, svc *application.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	read := authorization.RequireAny(auth, permView, permManage)
	write := authorization.Require(auth, permManage)
	route := func(pattern string, mw func(http.Handler) http.Handler, fn http.HandlerFunc) {
		mux.Handle(pattern, httpx.NoStore(mw(fn)))
	}
	route("GET /api/v1/infrastructure/tree", read, h.tree)
	route("GET /api/v1/infrastructure/placement-warnings", read, h.placementWarnings)

	route("GET /api/v1/buildings", read, h.listBuildings)
	route("POST /api/v1/buildings", write, h.createBuilding)
	route("GET /api/v1/buildings/{id}", read, h.getBuilding)
	route("PATCH /api/v1/buildings/{id}", write, h.updateBuilding)
	route("POST /api/v1/buildings/{id}/archive", write, h.buildingArchived(true))
	route("POST /api/v1/buildings/{id}/unarchive", write, h.buildingArchived(false))
	route("GET /api/v1/buildings/{id}/rooms", read, h.listRooms)
	route("POST /api/v1/buildings/{id}/rooms", write, h.createRoom)

	route("GET /api/v1/rooms/{id}", read, h.getRoom)
	route("PATCH /api/v1/rooms/{id}", write, h.updateRoom)
	route("POST /api/v1/rooms/{id}/archive", write, h.roomArchived(true))
	route("POST /api/v1/rooms/{id}/unarchive", write, h.roomArchived(false))
	route("GET /api/v1/rooms/{id}/racks", read, h.listRacks)
	route("POST /api/v1/rooms/{id}/racks", write, h.createRack)

	route("GET /api/v1/racks/{id}", read, h.getRack)
	route("PATCH /api/v1/racks/{id}", write, h.renameRack)
	route("POST /api/v1/racks/{id}/archive", write, h.rackArchived(true))
	route("POST /api/v1/racks/{id}/unarchive", write, h.rackArchived(false))
	route("GET /api/v1/racks/{id}/placements", read, h.listPlacements)

	route("POST /api/v1/rack-placements", write, h.place)
	route("GET /api/v1/rack-placements/{id}", read, h.getPlacement)
	route("POST /api/v1/rack-placements/{id}/move", write, h.move)
	route("POST /api/v1/rack-placements/{id}/remove", write, h.remove)
	route("GET /api/v1/assets/{id}/location", read, h.assetLocation)

	route("GET /api/v1/virtual-machines", read, h.listVMs)
	route("POST /api/v1/virtual-machines", write, h.createVM)
	route("GET /api/v1/virtual-machines/{id}", read, h.getVM)
	route("PATCH /api/v1/virtual-machines/{id}", write, h.updateVM)
	route("POST /api/v1/virtual-machines/{id}/state", write, h.vmState)
	route("POST /api/v1/virtual-machines/{id}/hypervisor", write, h.vmHypervisor)
	route("POST /api/v1/virtual-machines/{id}/decommission", write, h.vmDecommission)
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "infrastructure.invalid_request", inv.Message)
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "infrastructure.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrConflict):
		httpx.WriteError(w, http.StatusConflict, "infrastructure.conflict", "An entry with this name already exists.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "infrastructure.version_conflict", "The record was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrArchived):
		httpx.WriteError(w, http.StatusConflict, "infrastructure.archived", "The record or its parent is archived.")
	case errors.Is(err, application.ErrNotEmpty):
		httpx.WriteError(w, http.StatusConflict, "infrastructure.not_empty", "Archive or remove the active rooms, racks and placements below first.")
	case errors.Is(err, application.ErrOccupied):
		httpx.WriteError(w, http.StatusConflict, "infrastructure.occupied", "The requested units are already occupied.")
	case errors.Is(err, application.ErrAssetPlaced):
		httpx.WriteError(w, http.StatusConflict, "infrastructure.asset_placed", "The asset already has an active placement.")
	case errors.Is(err, application.ErrPlacementClosed):
		httpx.WriteError(w, http.StatusConflict, "infrastructure.placement_closed", "The placement is no longer active.")
	case errors.Is(err, application.ErrDecommissioned):
		httpx.WriteError(w, http.StatusConflict, "infrastructure.decommissioned", "The virtual machine is decommissioned.")
	case errors.Is(err, application.ErrAssetUnusable):
		httpx.WriteError(w, http.StatusBadRequest, "infrastructure.asset_invalid", "The asset does not exist or is disposed, lost or retired.")
	case errors.Is(err, application.ErrReferenceInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "infrastructure.invalid_reference", "The referenced location or asset does not exist or cannot be used.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "infrastructure.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "infrastructure request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func principal(r *http.Request) application.Principal {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Principal{UserID: p.UserID, View: p.Has(permView), Manage: p.Has(permManage),
		AssetsView: p.Has(permAssetsView) || p.Has(permAssetsManage)}
}

func caller(w http.ResponseWriter, r *http.Request) application.Caller {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: httpx.RequestID(w)}
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst, maxBody); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "infrastructure.invalid_request", "The request body is not valid JSON for this operation.")
		return false
	}
	return true
}

// needVersion writes a 400 when a body without expectedVersion reaches an operation that requires it.
func needVersion(w http.ResponseWriter, v *int) bool {
	if v == nil {
		httpx.WriteError(w, http.StatusBadRequest, "infrastructure.invalid_request", "expectedVersion is required.")
		return false
	}
	return true
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func tsPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := ts(*t)
	return &s
}

func parsePage(w http.ResponseWriter, r *http.Request) (application.Page, bool) {
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "infrastructure.invalid_limit", "The limit must be a positive integer.")
		return application.Page{}, false
	}
	return application.Page{Limit: limit, Cursor: r.URL.Query().Get("cursor")}.Normalize(), true
}

func flag(r *http.Request, name string) bool { return r.URL.Query().Get(name) == "true" }

type listResponse[T any] struct {
	Items      []T               `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
	Names      map[string]string `json:"assetReferences,omitempty"`
}

func toList[A, T any](res application.Result[A], conv func(A) T) listResponse[T] {
	out := listResponse[T]{Items: make([]T, 0, len(res.Items)), NextCursor: res.NextCursor}
	for _, it := range res.Items {
		out.Items = append(out.Items, conv(it))
	}
	return out
}
