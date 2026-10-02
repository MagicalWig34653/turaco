// Package transport exposes the Products HTTP API under /api/v1.
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/products/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const (
	permView   = "products.view"
	permManage = "products.manage"
	maxBody    = 8 << 10
)

type handler struct {
	svc    *application.Service
	logger *slog.Logger
}

// Register mounts the Products routes: reads need products.view or
// products.manage, writes need products.manage.
func Register(mux *http.ServeMux, svc *application.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	read := authorization.RequireAny(auth, permView, permManage)
	write := authorization.Require(auth, permManage)
	route := func(pattern string, mw func(http.Handler) http.Handler, fn http.HandlerFunc) {
		mux.Handle(pattern, httpx.NoStore(mw(fn)))
	}
	route("GET /api/v1/manufacturers", read, h.listManufacturers)
	route("POST /api/v1/manufacturers", write, h.createManufacturer)
	route("PATCH /api/v1/manufacturers/{id}", write, h.renameManufacturer)
	route("GET /api/v1/product-categories", read, h.listCategories)
	route("POST /api/v1/product-categories", write, h.createCategory)
	route("PATCH /api/v1/product-categories/{id}", write, h.renameCategory)
	route("GET /api/v1/products", read, h.listProducts)
	route("POST /api/v1/products", write, h.createProduct)
	route("GET /api/v1/products/{id}", read, h.getProduct)
	route("PATCH /api/v1/products/{id}", write, h.updateProduct)
	route("POST /api/v1/products/{id}/activate", write, h.activate(true))
	route("POST /api/v1/products/{id}/deactivate", write, h.activate(false))
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "products.invalid_request", inv.Message)
	case errors.Is(err, application.ErrReferenceNotFound):
		httpx.WriteError(w, http.StatusBadRequest, "products.invalid_reference", "The referenced manufacturer or category does not exist.")
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "products.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrConflict):
		httpx.WriteError(w, http.StatusConflict, "products.conflict", "An entry with this name or part number already exists.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "products.version_conflict", "The record was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "products.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "products request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
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
		httpx.WriteError(w, http.StatusBadRequest, "products.invalid_request", "The request body is not valid JSON for this operation.")
		return false
	}
	return true
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

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
		httpx.WriteError(w, http.StatusBadRequest, "products.invalid_limit", "The limit must be a positive integer.")
		return application.Page{}, false
	}
	return application.Page{Limit: limit, Cursor: r.URL.Query().Get("cursor")}.Normalize(), true
}

func parseQuery(w http.ResponseWriter, r *http.Request) (string, bool) {
	q := r.URL.Query().Get("q")
	if utf8.RuneCountInString(q) > 100 || !utf8.ValidString(q) || strings.ContainsRune(q, 0) {
		httpx.WriteError(w, http.StatusBadRequest, "products.invalid_request", "The search query is invalid.")
		return "", false
	}
	return q, true
}

// ---- manufacturers and categories ----

type namedDTO struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	ParentID  *string `json:"parentId,omitempty"`
	Version   int     `json:"version"`
	CreatedAt string  `json:"createdAt"`
	UpdatedAt string  `json:"updatedAt"`
}

func toManufacturer(m application.Manufacturer) namedDTO {
	return namedDTO{ID: m.ID, Name: m.Name, Version: m.Version, CreatedAt: ts(m.CreatedAt), UpdatedAt: ts(m.UpdatedAt)}
}

func toCategory(c application.Category) namedDTO {
	return namedDTO{ID: c.ID, Name: c.Name, ParentID: c.ParentID, Version: c.Version, CreatedAt: ts(c.CreatedAt), UpdatedAt: ts(c.UpdatedAt)}
}

type nameBody struct {
	Name            string  `json:"name"`
	ParentID        *string `json:"parentId"`
	ExpectedVersion *int    `json:"expectedVersion"`
}

func (h *handler) listManufacturers(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	q, ok2 := parseQuery(w, r)
	if !ok || !ok2 {
		return
	}
	res, err := h.svc.ListManufacturers(r.Context(), principal(r), q, page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toList(res, toManufacturer))
}

func (h *handler) createManufacturer(w http.ResponseWriter, r *http.Request) {
	var b nameBody
	if !decode(w, r, &b) {
		return
	}
	m, err := h.svc.CreateManufacturer(r.Context(), caller(w, r), principal(r), b.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toManufacturer(m))
}

func (h *handler) renameManufacturer(w http.ResponseWriter, r *http.Request) {
	var b nameBody
	if !decode(w, r, &b) {
		return
	}
	if b.ExpectedVersion == nil {
		httpx.WriteError(w, http.StatusBadRequest, "products.invalid_request", "expectedVersion is required to change a record.")
		return
	}
	m, err := h.svc.RenameManufacturer(r.Context(), caller(w, r), principal(r), r.PathValue("id"), *b.ExpectedVersion, b.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toManufacturer(m))
}

func (h *handler) listCategories(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	q, ok2 := parseQuery(w, r)
	if !ok || !ok2 {
		return
	}
	res, err := h.svc.ListCategories(r.Context(), principal(r), q, page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toList(res, toCategory))
}

func (h *handler) createCategory(w http.ResponseWriter, r *http.Request) {
	var b nameBody
	if !decode(w, r, &b) {
		return
	}
	c, err := h.svc.CreateCategory(r.Context(), caller(w, r), principal(r), b.Name, b.ParentID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toCategory(c))
}

func (h *handler) renameCategory(w http.ResponseWriter, r *http.Request) {
	var b nameBody
	if !decode(w, r, &b) {
		return
	}
	if b.ExpectedVersion == nil {
		httpx.WriteError(w, http.StatusBadRequest, "products.invalid_request", "expectedVersion is required to change a record.")
		return
	}
	c, err := h.svc.RenameCategory(r.Context(), caller(w, r), principal(r), r.PathValue("id"), *b.ExpectedVersion, b.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toCategory(c))
}

// ---- products ----

type productDTO struct {
	ID                     string  `json:"id"`
	Name                   string  `json:"name"`
	ManufacturerID         *string `json:"manufacturerId"`
	CategoryID             *string `json:"categoryId"`
	ManufacturerPartNumber *string `json:"manufacturerPartNumber"`
	InternalPartNumber     *string `json:"internalPartNumber"`
	Serialized             bool    `json:"serialized"`
	StockManaged           bool    `json:"stockManaged"`
	AssetManaged           bool    `json:"assetManaged"`
	Active                 bool    `json:"active"`
	Version                int     `json:"version"`
	CreatedAt              string  `json:"createdAt"`
	UpdatedAt              string  `json:"updatedAt"`
}

func toProduct(p application.Product) productDTO {
	return productDTO{
		ID: p.ID, Name: p.Name, ManufacturerID: p.ManufacturerID, CategoryID: p.CategoryID,
		ManufacturerPartNumber: p.ManufacturerPartNumber, InternalPartNumber: p.InternalPartNumber,
		Serialized: p.Serialized, StockManaged: p.StockManaged, AssetManaged: p.AssetManaged, Active: p.Active,
		Version: p.Version, CreatedAt: ts(p.CreatedAt), UpdatedAt: ts(p.UpdatedAt),
	}
}

func (h *handler) listProducts(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	q, ok2 := parseQuery(w, r)
	if !ok || !ok2 {
		return
	}
	f := application.ProductFilter{
		TitlePrefix: q, CategoryID: r.URL.Query().Get("categoryId"), ManufacturerID: r.URL.Query().Get("manufacturerId"), Page: page,
	}
	switch r.URL.Query().Get("active") {
	case "":
	case "true":
		t := true
		f.Active = &t
	case "false":
		t := false
		f.Active = &t
	default:
		httpx.WriteError(w, http.StatusBadRequest, "products.invalid_request", "The active filter must be true or false.")
		return
	}
	res, err := h.svc.ListProducts(r.Context(), principal(r), f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toList(res, toProduct))
}

func (h *handler) getProduct(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.GetProduct(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toProduct(p))
}

type productBody struct {
	Name                   string  `json:"name"`
	ManufacturerID         *string `json:"manufacturerId"`
	CategoryID             *string `json:"categoryId"`
	ManufacturerPartNumber string  `json:"manufacturerPartNumber"`
	InternalPartNumber     string  `json:"internalPartNumber"`
	Serialized             bool    `json:"serialized"`
	StockManaged           *bool   `json:"stockManaged"`
	AssetManaged           bool    `json:"assetManaged"`
}

func (h *handler) createProduct(w http.ResponseWriter, r *http.Request) {
	var b productBody
	if !decode(w, r, &b) {
		return
	}
	p, err := h.svc.CreateProduct(r.Context(), caller(w, r), principal(r), application.ProductInput{
		Name: b.Name, ManufacturerID: b.ManufacturerID, CategoryID: b.CategoryID,
		ManufacturerPartNumber: b.ManufacturerPartNumber, InternalPartNumber: b.InternalPartNumber,
		Serialized: b.Serialized, StockManaged: b.StockManaged, AssetManaged: b.AssetManaged,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toProduct(p))
}

type updateProductBody struct {
	ExpectedVersion        *int    `json:"expectedVersion"`
	Name                   *string `json:"name"`
	ManufacturerID         *string `json:"manufacturerId"`
	ClearManufacturer      bool    `json:"clearManufacturer"`
	CategoryID             *string `json:"categoryId"`
	ClearCategory          bool    `json:"clearCategory"`
	ManufacturerPartNumber *string `json:"manufacturerPartNumber"`
	InternalPartNumber     *string `json:"internalPartNumber"`
	Serialized             *bool   `json:"serialized"`
	StockManaged           *bool   `json:"stockManaged"`
	AssetManaged           *bool   `json:"assetManaged"`
}

func (h *handler) updateProduct(w http.ResponseWriter, r *http.Request) {
	var b updateProductBody
	if !decode(w, r, &b) {
		return
	}
	if b.ExpectedVersion == nil {
		httpx.WriteError(w, http.StatusBadRequest, "products.invalid_request", "expectedVersion is required to change a product.")
		return
	}
	p, err := h.svc.UpdateProduct(r.Context(), caller(w, r), principal(r), r.PathValue("id"), *b.ExpectedVersion, application.UpdateProductInput{
		Name: b.Name, ManufacturerID: b.ManufacturerID, ClearManufacturer: b.ClearManufacturer, CategoryID: b.CategoryID,
		ClearCategory: b.ClearCategory, ManufacturerPartNumber: b.ManufacturerPartNumber, InternalPartNumber: b.InternalPartNumber,
		Serialized: b.Serialized, StockManaged: b.StockManaged, AssetManaged: b.AssetManaged,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toProduct(p))
}

func (h *handler) activate(active bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			ExpectedVersion *int `json:"expectedVersion"`
		}
		if !decode(w, r, &b) {
			return
		}
		p, err := h.svc.SetProductActive(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, active)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toProduct(p))
	}
}
