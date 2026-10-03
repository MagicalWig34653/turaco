package transport

import (
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/inventory/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

// ---- warehouses and storage locations ----

func includeInactive(r *http.Request) bool { return r.URL.Query().Get("includeInactive") == "true" }

func (h *handler) listWarehouses(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	res, err := h.svc.ListWarehouses(r.Context(), principal(r), includeInactive(r), page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toList(res, toWarehouse))
}

func (h *handler) getWarehouse(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.GetWarehouse(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toWarehouse(out))
}

type warehouseBody struct {
	Name            string  `json:"name"`
	LocationID      *string `json:"locationId"`
	ClearLocation   bool    `json:"clearLocation"`
	ExpectedVersion *int    `json:"expectedVersion"`
}

func (h *handler) createWarehouse(w http.ResponseWriter, r *http.Request) {
	var b warehouseBody
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.CreateWarehouse(r.Context(), caller(w, r), principal(r), b.Name, b.LocationID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toWarehouse(out))
}

func (h *handler) updateWarehouse(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name            *string `json:"name"`
		LocationID      *string `json:"locationId"`
		ClearLocation   bool    `json:"clearLocation"`
		ExpectedVersion *int    `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.ExpectedVersion == nil {
		httpx.WriteError(w, http.StatusBadRequest, "inventory.invalid_request", "expectedVersion is required.")
		return
	}
	out, err := h.svc.UpdateWarehouse(r.Context(), caller(w, r), principal(r), r.PathValue("id"), *b.ExpectedVersion,
		application.WarehouseUpdate{Name: b.Name, LocationID: b.LocationID, ClearLocation: b.ClearLocation})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toWarehouse(out))
}

type versionBody struct {
	ExpectedVersion *int `json:"expectedVersion"`
}

func (h *handler) warehouseActive(active bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b versionBody
		if !decode(w, r, &b) {
			return
		}
		out, err := h.svc.SetWarehouseActive(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, active)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toWarehouse(out))
	}
}

func (h *handler) listLocations(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	res, err := h.svc.ListStorageLocations(r.Context(), principal(r), r.PathValue("id"), includeInactive(r), page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toList(res, toLocation))
}

func (h *handler) createLocation(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.CreateStorageLocation(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toLocation(out))
}

func (h *handler) renameLocation(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name            string `json:"name"`
		ExpectedVersion *int   `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.ExpectedVersion == nil {
		httpx.WriteError(w, http.StatusBadRequest, "inventory.invalid_request", "expectedVersion is required.")
		return
	}
	out, err := h.svc.RenameStorageLocation(r.Context(), caller(w, r), principal(r), r.PathValue("id"), *b.ExpectedVersion, b.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toLocation(out))
}

func (h *handler) locationActive(active bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b versionBody
		if !decode(w, r, &b) {
			return
		}
		out, err := h.svc.SetStorageLocationActive(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, active)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toLocation(out))
	}
}

// ---- stock and ledger ----

func (h *handler) listStock(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	v := r.URL.Query()
	res, err := h.svc.ListStock(r.Context(), principal(r), application.StockFilter{
		ProductID: v.Get("productId"), WarehouseID: v.Get("warehouseId"), StorageLocationID: v.Get("storageLocationId"),
		WithStockOnly: v.Get("withStockOnly") == "true", Page: page,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := toList(res, toBalance)
	var products, locations []string
	for _, b := range res.Items {
		products, locations = append(products, b.ProductID), append(locations, b.StorageLocationID)
	}
	out, err = withNames(h, r, out, products, locations)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) listTransactions(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	v := r.URL.Query()
	res, err := h.svc.ListTransactions(r.Context(), principal(r), application.TransactionFilter{
		ProductID: v.Get("productId"), StorageLocationID: v.Get("storageLocationId"), Type: v.Get("type"),
		ContextType: v.Get("contextType"), ContextID: v.Get("contextId"), Page: page,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := toList(res, toTransaction)
	var products, locations []string
	for _, t := range res.Items {
		products, locations = append(products, t.ProductID), append(locations, t.StorageLocationID)
	}
	out, err = withNames(h, r, out, products, locations)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type moveBody struct {
	ProductID         string `json:"productId"`
	StorageLocationID string `json:"storageLocationId"`
	FromID            string `json:"fromStorageLocationId"`
	ToID              string `json:"toStorageLocationId"`
	Quantity          int    `json:"quantity"`
	Delta             int    `json:"delta"`
	Reason            string `json:"reason"`
	OriginType        string `json:"originType"`
	OriginID          string `json:"originId"`
}

func (b moveBody) origin() application.Origin {
	return application.Origin{Type: b.OriginType, ID: b.OriginID}
}

func (h *handler) respondRows(w http.ResponseWriter, r *http.Request, rows []application.Transaction, err error) {
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		Items []transactionDTO `json:"items"`
	}{Items: make([]transactionDTO, 0, len(rows))}
	for _, t := range rows {
		out.Items = append(out.Items, toTransaction(t))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) stockMove(op func(h *handler, r *http.Request, w http.ResponseWriter, m application.StockMove) ([]application.Transaction, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b moveBody
		if !decode(w, r, &b) {
			return
		}
		rows, err := op(h, r, w, application.StockMove{
			ProductID: b.ProductID, StorageLocationID: b.StorageLocationID, Quantity: b.Quantity, Reason: b.Reason, Origin: b.origin(),
		})
		h.respondRows(w, r, rows, err)
	}
}

func (h *handler) issue(w http.ResponseWriter, r *http.Request) {
	h.stockMove(func(h *handler, r *http.Request, w http.ResponseWriter, m application.StockMove) ([]application.Transaction, error) {
		return h.svc.Issue(r.Context(), caller(w, r), principal(r), m)
	})(w, r)
}

func (h *handler) returnStock(w http.ResponseWriter, r *http.Request) {
	h.stockMove(func(h *handler, r *http.Request, w http.ResponseWriter, m application.StockMove) ([]application.Transaction, error) {
		return h.svc.Return(r.Context(), caller(w, r), principal(r), m)
	})(w, r)
}

func (h *handler) dispose(w http.ResponseWriter, r *http.Request) {
	h.stockMove(func(h *handler, r *http.Request, w http.ResponseWriter, m application.StockMove) ([]application.Transaction, error) {
		return h.svc.Dispose(r.Context(), caller(w, r), principal(r), m)
	})(w, r)
}

func (h *handler) transfer(w http.ResponseWriter, r *http.Request) {
	var b moveBody
	if !decode(w, r, &b) {
		return
	}
	rows, err := h.svc.Transfer(r.Context(), caller(w, r), principal(r), application.TransferMove{
		ProductID: b.ProductID, FromID: b.FromID, ToID: b.ToID, Quantity: b.Quantity, Reason: b.Reason, Origin: b.origin(),
	})
	h.respondRows(w, r, rows, err)
}

func (h *handler) correct(w http.ResponseWriter, r *http.Request) {
	var b moveBody
	if !decode(w, r, &b) {
		return
	}
	rows, err := h.svc.Correct(r.Context(), caller(w, r), principal(r), application.Correction{
		ProductID: b.ProductID, StorageLocationID: b.StorageLocationID, Delta: b.Delta, Reason: b.Reason, Origin: b.origin(),
	})
	h.respondRows(w, r, rows, err)
}

// ---- reservations ----

func (h *handler) listReservations(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	v := r.URL.Query()
	if s := v.Get("status"); s != "" {
		known := false
		for _, k := range application.ReservationStatuses {
			known = known || k == s
		}
		if !known {
			httpx.WriteError(w, http.StatusBadRequest, "inventory.invalid_request", "Unknown status.")
			return
		}
	}
	res, err := h.svc.ListReservations(r.Context(), principal(r), application.ReservationFilter{
		Status: v.Get("status"), ProductID: v.Get("productId"), ContextType: v.Get("contextType"), ContextID: v.Get("contextId"), Page: page,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := toList(res, toReservation)
	var products, locations []string
	for _, x := range res.Items {
		products = append(products, x.ProductID)
		if x.StorageLocationID != nil {
			locations = append(locations, *x.StorageLocationID)
		}
	}
	out, err = withNames(h, r, out, products, locations)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) getReservation(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.GetReservation(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toReservation(out))
}

func (h *handler) reserve(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Kind              string `json:"kind"`
		ProductID         string `json:"productId"`
		StorageLocationID string `json:"storageLocationId"`
		Quantity          int    `json:"quantity"`
		AssetID           string `json:"assetId"`
		OriginType        string `json:"originType"`
		OriginID          string `json:"originId"`
	}
	if !decode(w, r, &b) {
		return
	}
	origin := application.Origin{Type: b.OriginType, ID: b.OriginID}
	var out application.Reservation
	var err error
	switch b.Kind {
	case application.KindQuantity:
		out, err = h.svc.ReserveQuantity(r.Context(), caller(w, r), principal(r), application.QuantityReservation{
			ProductID: b.ProductID, StorageLocationID: b.StorageLocationID, Quantity: b.Quantity, Origin: origin,
		})
	case application.KindAsset:
		out, err = h.svc.ReserveAsset(r.Context(), caller(w, r), principal(r), application.AssetReservation{AssetID: b.AssetID, Origin: origin})
	default:
		httpx.WriteError(w, http.StatusBadRequest, "inventory.invalid_request", "kind must be quantity or asset.")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toReservation(out))
}

func (h *handler) release(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int   `json:"expectedVersion"`
		Reason          string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.Release(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.Reason)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toReservation(out))
}

func (h *handler) fulfill(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int   `json:"expectedVersion"`
		Note            string `json:"note"`
		AssigneeType    string `json:"assigneeType"`
		AssigneeID      string `json:"assigneeId"`
	}
	if !decode(w, r, &b) {
		return
	}
	var assignee *application.AssetAssignee
	if b.AssigneeType != "" || b.AssigneeID != "" {
		assignee = &application.AssetAssignee{Type: b.AssigneeType, ID: b.AssigneeID}
	}
	out, err := h.svc.Fulfill(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, assignee, b.Note)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toReservation(out))
}
