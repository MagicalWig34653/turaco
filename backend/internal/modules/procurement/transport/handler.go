// Package transport exposes the Procurement HTTP API under /api/v1. Reads need
// procurement.view or procurement.manage; writes need procurement.manage.
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/procurement/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const (
	permView   = "procurement.view"
	permManage = "procurement.manage"
	maxBody    = 16 << 10
)

type handler struct {
	svc    *application.Service
	logger *slog.Logger
}

// Register mounts the Procurement routes.
func Register(mux *http.ServeMux, svc *application.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	read := authorization.RequireAny(auth, permView, permManage)
	write := authorization.Require(auth, permManage)
	route := func(pattern string, mw func(http.Handler) http.Handler, fn http.HandlerFunc) {
		mux.Handle(pattern, httpx.NoStore(mw(fn)))
	}
	route("GET /api/v1/suppliers", read, h.listSuppliers)
	route("POST /api/v1/suppliers", write, h.createSupplier)
	route("GET /api/v1/suppliers/{id}", read, h.getSupplier)
	route("PATCH /api/v1/suppliers/{id}", write, h.updateSupplier)
	route("POST /api/v1/suppliers/{id}/activate", write, h.supplierActive(true))
	route("POST /api/v1/suppliers/{id}/deactivate", write, h.supplierActive(false))

	route("GET /api/v1/procurement-requests", read, h.listNeeds)
	route("POST /api/v1/procurement-requests", write, h.createNeed)
	route("GET /api/v1/procurement-requests/{id}", read, h.getNeed)
	route("POST /api/v1/procurement-requests/{id}/cancel", write, h.cancelNeed)

	route("GET /api/v1/purchase-orders", read, h.listOrders)
	route("POST /api/v1/purchase-orders", write, h.createOrder)
	route("GET /api/v1/purchase-orders/{id}", read, h.getOrder)
	route("PATCH /api/v1/purchase-orders/{id}", write, h.updateOrder)
	route("POST /api/v1/purchase-orders/{id}/lines", write, h.addLine)
	route("PATCH /api/v1/purchase-orders/{id}/lines/{lineId}", write, h.updateLine)
	route("DELETE /api/v1/purchase-orders/{id}/lines/{lineId}", write, h.removeLine)
	route("POST /api/v1/purchase-orders/{id}/submit", write, h.submit)
	route("POST /api/v1/purchase-orders/{id}/send", write, h.send)
	route("POST /api/v1/purchase-orders/{id}/acknowledge", write, h.acknowledge)
	route("POST /api/v1/purchase-orders/{id}/cancel", write, h.cancel)
	route("POST /api/v1/purchase-orders/{id}/close", write, h.closeOrder)
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	var tr *application.InvalidTransitionError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "procurement.invalid_request", inv.Message)
	case errors.As(err, &tr):
		httpx.WriteError(w, http.StatusConflict, "procurement.invalid_transition", "The operation is not allowed in the current status.")
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "procurement.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrConflict):
		httpx.WriteError(w, http.StatusConflict, "procurement.conflict", "An entry with this name already exists.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "procurement.version_conflict", "The record was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrNeedUnavailable):
		httpx.WriteError(w, http.StatusConflict, "procurement.request_unavailable", "The procurement request is not open or is for another product.")
	case errors.Is(err, application.ErrNoEligibleApprover):
		httpx.WriteError(w, http.StatusConflict, "procurement.no_eligible_approver", "The chosen approver cannot approve this order.")
	case errors.Is(err, application.ErrProductInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "procurement.product_invalid", "The product does not exist or is inactive.")
	case errors.Is(err, application.ErrSupplierInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "procurement.supplier_invalid", "The supplier does not exist or is inactive.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "procurement.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "procurement request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
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
		httpx.WriteError(w, http.StatusBadRequest, "procurement.invalid_request", "The request body is not valid JSON for this operation.")
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
		httpx.WriteError(w, http.StatusBadRequest, "procurement.invalid_limit", "The limit must be a positive integer.")
		return application.Page{}, false
	}
	return application.Page{Limit: limit, Cursor: r.URL.Query().Get("cursor")}.Normalize(), true
}

func cleanQuery(w http.ResponseWriter, q string) (string, bool) {
	if utf8.RuneCountInString(q) > 100 || !utf8.ValidString(q) || strings.ContainsRune(q, 0) {
		httpx.WriteError(w, http.StatusBadRequest, "procurement.invalid_request", "The search query is invalid.")
		return "", false
	}
	return q, true
}

type versionBody struct {
	ExpectedVersion *int   `json:"expectedVersion"`
	Reason          string `json:"reason"`
}
