package transport

import (
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/procurement/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

// ---- suppliers ----

type supplierDTO struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	AccountReference *string `json:"accountReference"`
	Active           bool    `json:"active"`
	Version          int     `json:"version"`
	CreatedAt        string  `json:"createdAt"`
	UpdatedAt        string  `json:"updatedAt"`
}

func toSupplier(s application.Supplier) supplierDTO {
	return supplierDTO{ID: s.ID, Name: s.Name, AccountReference: s.AccountReference, Active: s.Active, Version: s.Version, CreatedAt: ts(s.CreatedAt), UpdatedAt: ts(s.UpdatedAt)}
}

func (h *handler) listSuppliers(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	q, ok := cleanQuery(w, r.URL.Query().Get("q"))
	if !ok {
		return
	}
	res, err := h.svc.ListSuppliers(r.Context(), principal(r), q, r.URL.Query().Get("includeInactive") == "true", page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toList(res, toSupplier))
}

func (h *handler) getSupplier(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.GetSupplier(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toSupplier(out))
}

func (h *handler) createSupplier(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name             string `json:"name"`
		AccountReference string `json:"accountReference"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.CreateSupplier(r.Context(), caller(w, r), principal(r), b.Name, b.AccountReference)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toSupplier(out))
}

func (h *handler) updateSupplier(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion  *int    `json:"expectedVersion"`
		Name             *string `json:"name"`
		AccountReference *string `json:"accountReference"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.ExpectedVersion == nil {
		httpx.WriteError(w, http.StatusBadRequest, "procurement.invalid_request", "expectedVersion is required.")
		return
	}
	out, err := h.svc.UpdateSupplier(r.Context(), caller(w, r), principal(r), r.PathValue("id"), *b.ExpectedVersion,
		application.SupplierUpdate{Name: b.Name, AccountReference: b.AccountReference})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toSupplier(out))
}

func (h *handler) supplierActive(active bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b versionBody
		if !decode(w, r, &b) {
			return
		}
		out, err := h.svc.SetSupplierActive(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, active)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toSupplier(out))
	}
}

// ---- procurement requests ----

type needDTO struct {
	ID           string  `json:"id"`
	Reference    string  `json:"reference"`
	ProductID    string  `json:"productId"`
	Quantity     int     `json:"quantity"`
	Status       string  `json:"status"`
	StatusReason *string `json:"statusReason"`
	ContextType  *string `json:"contextType"`
	ContextID    *string `json:"contextId"`
	Notes        *string `json:"notes"`
	RequestedBy  *string `json:"requestedBy"`
	Version      int     `json:"version"`
	CreatedAt    string  `json:"createdAt"`
	UpdatedAt    string  `json:"updatedAt"`
}

func toNeed(n application.Need) needDTO {
	return needDTO{ID: n.ID, Reference: n.Reference, ProductID: n.ProductID, Quantity: n.Quantity, Status: n.Status, StatusReason: n.StatusReason,
		ContextType: n.ContextType, ContextID: n.ContextID, Notes: n.Notes, RequestedBy: n.RequestedBy, Version: n.Version,
		CreatedAt: ts(n.CreatedAt), UpdatedAt: ts(n.UpdatedAt)}
}

func (h *handler) listNeeds(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	v := r.URL.Query()
	if s := v.Get("status"); s != "" {
		known := false
		for _, k := range application.NeedStatuses {
			known = known || k == s
		}
		if !known {
			httpx.WriteError(w, http.StatusBadRequest, "procurement.invalid_request", "Unknown status.")
			return
		}
	}
	res, err := h.svc.ListNeeds(r.Context(), principal(r), application.NeedFilter{
		Status: v.Get("status"), ProductID: v.Get("productId"), ContextType: v.Get("contextType"), ContextID: v.Get("contextId"), Page: page,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := toList(res, toNeed)
	var products []string
	for _, n := range res.Items {
		products = append(products, n.ProductID)
	}
	out, err = withNames(h, r, out, products, nil)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) getNeed(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.GetNeed(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toNeed(out))
}

func (h *handler) createNeed(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ProductID  string `json:"productId"`
		Quantity   int    `json:"quantity"`
		OriginType string `json:"originType"`
		OriginID   string `json:"originId"`
		Notes      string `json:"notes"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.CreateNeed(r.Context(), caller(w, r), principal(r), application.NewNeed{
		ProductID: b.ProductID, Quantity: b.Quantity, ContextType: b.OriginType, ContextID: b.OriginID, Notes: b.Notes,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toNeed(out))
}

func (h *handler) cancelNeed(w http.ResponseWriter, r *http.Request) {
	var b versionBody
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.CancelNeed(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.Reason)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toNeed(out))
}
