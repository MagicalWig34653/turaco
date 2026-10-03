// Package transport exposes the Asset HTTP API under /api/v1. Reads need
// assets.view or assets.manage (a signed-in User may also read the assets
// currently assigned to them); writes need assets.manage.
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/assets/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const (
	permView   = "assets.view"
	permManage = "assets.manage"
	maxBody    = 16 << 10
	dateLayout = "2006-01-02"
)

type handler struct {
	svc    *application.Service
	logger *slog.Logger
}

// operations maps the action endpoints to lifecycle operations.
var operations = map[string]string{
	"make-available": application.OpMakeAvailable,
	"assign":         application.OpAssign,
	"reassign":       application.OpReassign,
	"return":         application.OpReturn,
	"send-to-repair": application.OpSendToRepair,
	"finish-repair":  application.OpFinishRepair,
	"retire":         application.OpRetire,
	"dispose":        application.OpDispose,
	"mark-lost":      application.OpMarkLost,
	"recover":        application.OpRecover,
}

// Register mounts the Asset routes.
func Register(mux *http.ServeMux, svc *application.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	authed := authorization.RequireAuthenticated(auth)
	read := authorization.RequireAny(auth, permView, permManage)
	write := authorization.Require(auth, permManage)
	route := func(pattern string, mw func(http.Handler) http.Handler, fn http.HandlerFunc) {
		mux.Handle(pattern, httpx.NoStore(mw(fn)))
	}
	route("GET /api/v1/assets", read, h.list)
	route("POST /api/v1/assets", write, h.create)
	route("GET /api/v1/assets/lookup", read, h.lookup)
	route("GET /api/v1/my-assets", authed, h.mine)
	route("GET /api/v1/assets/{id}", authed, h.get)
	route("PATCH /api/v1/assets/{id}", write, h.update)
	route("POST /api/v1/assets/{id}/provisioning", write, h.provisioning)
	for path, op := range operations {
		route("POST /api/v1/assets/{id}/"+path, write, h.transition(op))
	}
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	var tr *application.InvalidTransitionError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "assets.invalid_request", inv.Message)
	case errors.As(err, &tr):
		httpx.WriteError(w, http.StatusConflict, "assets.invalid_transition", "The operation is not allowed in the asset's current status.")
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "assets.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrConflict):
		httpx.WriteError(w, http.StatusConflict, "assets.conflict", "An asset with this serial number or asset tag already exists, or the code is ambiguous.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "assets.version_conflict", "The asset was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrAssigneeInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "assets.assignee_invalid", "The assignee does not exist or is not active.")
	case errors.Is(err, application.ErrProductInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "assets.product_invalid", "The product does not exist, is inactive or is not tracked as an asset.")
	case errors.Is(err, application.ErrReferenceInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "assets.invalid_reference", "The referenced location does not exist.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "assets.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "assets request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
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
		httpx.WriteError(w, http.StatusBadRequest, "assets.invalid_request", "The request body is not valid JSON for this operation.")
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

func datePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(dateLayout)
	return &s
}

// parseDate reads an optional YYYY-MM-DD value; nil means absent.
func parseDate(w http.ResponseWriter, value *string) (*time.Time, bool) {
	if value == nil || *value == "" {
		return nil, true
	}
	t, err := time.Parse(dateLayout, *value)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "assets.invalid_request", "Dates must be in the form YYYY-MM-DD.")
		return nil, false
	}
	return &t, true
}

type assetDTO struct {
	ID                 string  `json:"id"`
	Reference          string  `json:"reference"`
	ProductID          string  `json:"productId"`
	SerialNumber       *string `json:"serialNumber"`
	AssetTag           *string `json:"assetTag"`
	Status             string  `json:"status"`
	StatusReason       *string `json:"statusReason"`
	ProvisioningStatus string  `json:"provisioningStatus"`
	OwnershipType      string  `json:"ownershipType"`
	SupplierID         *string `json:"supplierId"`
	PurchasedAt        *string `json:"purchasedAt"`
	WarrantyUntil      *string `json:"warrantyUntil"`
	LocationID         *string `json:"locationId"`
	Notes              *string `json:"notes"`
	Version            int     `json:"version"`
	CreatedAt          string  `json:"createdAt"`
	UpdatedAt          string  `json:"updatedAt"`
}

func toAsset(a application.Asset) assetDTO {
	return assetDTO{
		ID: a.ID, Reference: a.Reference, ProductID: a.ProductID, SerialNumber: a.SerialNumber, AssetTag: a.AssetTag, Status: a.Status,
		StatusReason: a.StatusReason, ProvisioningStatus: a.ProvisioningStatus, OwnershipType: a.OwnershipType, SupplierID: a.SupplierID,
		PurchasedAt: datePtr(a.PurchasedAt), WarrantyUntil: datePtr(a.WarrantyUntil), LocationID: a.LocationID, Notes: a.Notes,
		Version: a.Version, CreatedAt: ts(a.CreatedAt), UpdatedAt: ts(a.UpdatedAt),
	}
}

type assignmentDTO struct {
	ID           string  `json:"id"`
	AssigneeType string  `json:"assigneeType"`
	AssigneeID   string  `json:"assigneeId"`
	AssignedAt   string  `json:"assignedAt"`
	AssignedBy   *string `json:"assignedBy"`
	ReturnedAt   *string `json:"returnedAt"`
	Note         *string `json:"note"`
}

type detailDTO struct {
	assetDTO
	Assignments       []assignmentDTO   `json:"assignments"`
	AllowedOperations []string          `json:"allowedOperations"`
	Names             map[string]string `json:"names"`
}

type listResponse struct {
	Items      []assetDTO `json:"items"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

func toList(res application.Result) listResponse {
	out := listResponse{Items: make([]assetDTO, 0, len(res.Items)), NextCursor: res.NextCursor}
	for _, a := range res.Items {
		out.Items = append(out.Items, toAsset(a))
	}
	return out
}

func parsePage(w http.ResponseWriter, r *http.Request) (application.Page, bool) {
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "assets.invalid_limit", "The limit must be a positive integer.")
		return application.Page{}, false
	}
	return application.Page{Limit: limit, Cursor: r.URL.Query().Get("cursor")}.Normalize(), true
}

func cleanQuery(w http.ResponseWriter, q string) (string, bool) {
	if utf8.RuneCountInString(q) > 100 || !utf8.ValidString(q) || strings.ContainsRune(q, 0) {
		httpx.WriteError(w, http.StatusBadRequest, "assets.invalid_request", "The search query is invalid.")
		return "", false
	}
	return q, true
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	q, ok := cleanQuery(w, r.URL.Query().Get("q"))
	if !ok {
		return
	}
	v := r.URL.Query()
	res, err := h.svc.List(r.Context(), principal(r), application.Filter{
		Status: v.Get("status"), ProductID: v.Get("productId"), AssigneeID: v.Get("assigneeId"), LocationID: v.Get("locationId"),
		Query: q, Page: page,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toList(res))
}

func (h *handler) mine(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	res, err := h.svc.Mine(r.Context(), principal(r), page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toList(res))
}

func (h *handler) lookup(w http.ResponseWriter, r *http.Request) {
	a, err := h.svc.Lookup(r.Context(), principal(r), r.URL.Query().Get("code"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toAsset(a))
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.Get(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := detailDTO{assetDTO: toAsset(d.Asset), Assignments: make([]assignmentDTO, 0, len(d.Assignments)), AllowedOperations: d.Allowed, Names: d.Names}
	if out.AllowedOperations == nil {
		out.AllowedOperations = []string{}
	}
	for _, a := range d.Assignments {
		out.Assignments = append(out.Assignments, assignmentDTO{
			ID: a.ID, AssigneeType: a.AssigneeType, AssigneeID: a.AssigneeID, AssignedAt: ts(a.AssignedAt),
			AssignedBy: a.AssignedBy, ReturnedAt: tsPtr(a.ReturnedAt), Note: a.Note,
		})
	}
	httpx.JSON(w, http.StatusOK, out)
}

type createBody struct {
	ProductID     string  `json:"productId"`
	SerialNumber  string  `json:"serialNumber"`
	AssetTag      string  `json:"assetTag"`
	OwnershipType string  `json:"ownershipType"`
	Status        string  `json:"status"`
	SupplierID    *string `json:"supplierId"`
	PurchasedAt   *string `json:"purchasedAt"`
	WarrantyUntil *string `json:"warrantyUntil"`
	LocationID    *string `json:"locationId"`
	Notes         string  `json:"notes"`
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var b createBody
	if !decode(w, r, &b) {
		return
	}
	purchased, ok := parseDate(w, b.PurchasedAt)
	if !ok {
		return
	}
	warranty, ok := parseDate(w, b.WarrantyUntil)
	if !ok {
		return
	}
	a, err := h.svc.Create(r.Context(), caller(w, r), principal(r), application.CreateInput{
		ProductID: b.ProductID, SerialNumber: b.SerialNumber, AssetTag: b.AssetTag, OwnershipType: b.OwnershipType, Status: b.Status,
		SupplierID: b.SupplierID, PurchasedAt: purchased, WarrantyUntil: warranty, LocationID: b.LocationID, Notes: b.Notes,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toAsset(a))
}

type updateBody struct {
	ExpectedVersion    *int    `json:"expectedVersion"`
	SerialNumber       *string `json:"serialNumber"`
	AssetTag           *string `json:"assetTag"`
	OwnershipType      *string `json:"ownershipType"`
	PurchasedAt        *string `json:"purchasedAt"`
	ClearPurchasedAt   bool    `json:"clearPurchasedAt"`
	WarrantyUntil      *string `json:"warrantyUntil"`
	ClearWarrantyUntil bool    `json:"clearWarrantyUntil"`
	LocationID         *string `json:"locationId"`
	ClearLocation      bool    `json:"clearLocation"`
	Notes              *string `json:"notes"`
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	var b updateBody
	if !decode(w, r, &b) {
		return
	}
	if b.ExpectedVersion == nil {
		httpx.WriteError(w, http.StatusBadRequest, "assets.invalid_request", "expectedVersion is required.")
		return
	}
	purchased, ok := parseDate(w, b.PurchasedAt)
	if !ok {
		return
	}
	warranty, ok := parseDate(w, b.WarrantyUntil)
	if !ok {
		return
	}
	a, err := h.svc.Update(r.Context(), caller(w, r), principal(r), r.PathValue("id"), *b.ExpectedVersion, application.UpdateInput{
		SerialNumber: b.SerialNumber, AssetTag: b.AssetTag, OwnershipType: b.OwnershipType,
		PurchasedAt: purchased, ClearPurchasedAt: b.ClearPurchasedAt, WarrantyUntil: warranty, ClearWarrantyUntil: b.ClearWarrantyUntil,
		LocationID: b.LocationID, ClearLocation: b.ClearLocation, Notes: b.Notes,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toAsset(a))
}

type provisioningBody struct {
	ExpectedVersion *int   `json:"expectedVersion"`
	Status          string `json:"status"`
}

func (h *handler) provisioning(w http.ResponseWriter, r *http.Request) {
	var b provisioningBody
	if !decode(w, r, &b) {
		return
	}
	a, err := h.svc.SetProvisioning(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.Status)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toAsset(a))
}

type transitionBody struct {
	ExpectedVersion *int   `json:"expectedVersion"`
	Reason          string `json:"reason"`
	Note            string `json:"note"`
	AssigneeType    string `json:"assigneeType"`
	AssigneeID      string `json:"assigneeId"`
}

func (h *handler) transition(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b transitionBody
		if !decode(w, r, &b) {
			return
		}
		a, err := h.svc.Transition(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, op, application.Params{
			Reason: b.Reason, Note: b.Note, Assignee: application.Assignee{Type: b.AssigneeType, ID: b.AssigneeID},
		})
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toAsset(a))
	}
}
