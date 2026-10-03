// Package transport exposes the Inventory HTTP API under /api/v1. Reads need
// inventory.view or inventory.manage; writes need inventory.manage.
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/inventory/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const (
	permView   = "inventory.view"
	permManage = "inventory.manage"
	maxBody    = 128 << 10
)

type handler struct {
	svc    *application.Service
	logger *slog.Logger
}

// Register mounts the Inventory routes.
func Register(mux *http.ServeMux, svc *application.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	read := authorization.RequireAny(auth, permView, permManage)
	write := authorization.Require(auth, permManage)
	route := func(pattern string, mw func(http.Handler) http.Handler, fn http.HandlerFunc) {
		mux.Handle(pattern, httpx.NoStore(mw(fn)))
	}
	route("GET /api/v1/warehouses", read, h.listWarehouses)
	route("POST /api/v1/warehouses", write, h.createWarehouse)
	route("GET /api/v1/warehouses/{id}", read, h.getWarehouse)
	route("PATCH /api/v1/warehouses/{id}", write, h.updateWarehouse)
	route("POST /api/v1/warehouses/{id}/activate", write, h.warehouseActive(true))
	route("POST /api/v1/warehouses/{id}/deactivate", write, h.warehouseActive(false))
	route("GET /api/v1/warehouses/{id}/storage-locations", read, h.listLocations)
	route("POST /api/v1/warehouses/{id}/storage-locations", write, h.createLocation)
	route("PATCH /api/v1/storage-locations/{id}", write, h.renameLocation)
	route("POST /api/v1/storage-locations/{id}/activate", write, h.locationActive(true))
	route("POST /api/v1/storage-locations/{id}/deactivate", write, h.locationActive(false))

	route("GET /api/v1/stock", read, h.listStock)
	route("GET /api/v1/inventory-transactions", read, h.listTransactions)
	route("POST /api/v1/stock/issue", write, h.issue)
	route("POST /api/v1/stock/return", write, h.returnStock)
	route("POST /api/v1/stock/dispose", write, h.dispose)
	route("POST /api/v1/stock/transfer", write, h.transfer)
	route("POST /api/v1/stock/correct", write, h.correct)

	route("GET /api/v1/goods-receipts", read, h.listReceipts)
	route("POST /api/v1/goods-receipts", write, h.postReceipt)
	route("GET /api/v1/goods-receipts/{id}", read, h.getReceipt)

	route("GET /api/v1/reservations", read, h.listReservations)
	route("POST /api/v1/reservations", write, h.reserve)
	route("GET /api/v1/reservations/{id}", read, h.getReservation)
	route("POST /api/v1/reservations/{id}/release", write, h.release)
	route("POST /api/v1/reservations/{id}/fulfill", write, h.fulfill)
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	var tr *application.InvalidTransitionError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "inventory.invalid_request", inv.Message)
	case errors.As(err, &tr):
		httpx.WriteError(w, http.StatusConflict, "inventory.invalid_transition", "The operation is not allowed in the reservation's current status.")
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "inventory.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrConflict):
		httpx.WriteError(w, http.StatusConflict, "inventory.conflict", "An entry with this name already exists, or the asset already has an active reservation.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "inventory.version_conflict", "The record was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrInsufficientStock):
		httpx.WriteError(w, http.StatusConflict, "inventory.insufficient_stock", "There is not enough available stock for this operation.")
	case errors.Is(err, application.ErrAssetUnavailable):
		httpx.WriteError(w, http.StatusConflict, "inventory.asset_unavailable", "The asset is not available for reservation.")
	case errors.Is(err, application.ErrProductInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "inventory.product_invalid", "The product does not exist, is inactive or is not tracked by quantity.")
	case errors.Is(err, application.ErrLocationInactive):
		httpx.WriteError(w, http.StatusBadRequest, "inventory.location_invalid", "The storage location or warehouse does not exist or is not active.")
	case errors.Is(err, application.ErrReferenceInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "inventory.invalid_reference", "The referenced location does not exist.")
	case errors.Is(err, application.ErrDuplicateAsset):
		httpx.WriteError(w, http.StatusConflict, "inventory.duplicate_asset", "An asset with this serial number or asset tag already exists.")
	case errors.Is(err, application.ErrOrderNotReceivable):
		httpx.WriteError(w, http.StatusConflict, "inventory.order_not_receivable", "The purchase order cannot receive goods in its current status.")
	case errors.Is(err, application.ErrOverReceipt):
		httpx.WriteError(w, http.StatusConflict, "inventory.over_receipt", "A received quantity exceeds the ordered quantity.")
	case errors.Is(err, application.ErrAssigneeInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "inventory.assignee_invalid", "The assignee does not exist or is not active.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "inventory.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "inventory request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
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
		httpx.WriteError(w, http.StatusBadRequest, "inventory.invalid_request", "The request body is not valid JSON for this operation.")
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

type listResponse[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

func toList[A, T any](res application.Result[A], conv func(A) T) listResponse[T] {
	out := listResponse[T]{Items: make([]T, 0, len(res.Items)), NextCursor: res.NextCursor}
	for _, it := range res.Items {
		out.Items = append(out.Items, conv(it))
	}
	return out
}

func parsePage(w http.ResponseWriter, r *http.Request) (application.Page, bool) {
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "inventory.invalid_limit", "The limit must be a positive integer.")
		return application.Page{}, false
	}
	return application.Page{Limit: limit, Cursor: r.URL.Query().Get("cursor")}.Normalize(), true
}
