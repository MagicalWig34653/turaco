package transport

import (
	"net/http"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/inventory/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type receiptLineDTO struct {
	ID                string   `json:"id"`
	OrderLineID       string   `json:"orderLineId"`
	ProductID         string   `json:"productId"`
	Quantity          int      `json:"quantity"`
	StorageLocationID *string  `json:"storageLocationId"`
	AssetIDs          []string `json:"assetIds"`
}

type receiptDTO struct {
	ID           string           `json:"id"`
	Reference    string           `json:"reference"`
	OrderID      string           `json:"orderId"`
	SupplierID   string           `json:"supplierId"`
	DeliveryNote *string          `json:"deliveryNote"`
	ReceivedBy   *string          `json:"receivedBy"`
	CreatedAt    string           `json:"createdAt"`
	Lines        []receiptLineDTO `json:"lines"`
}

func toReceipt(g application.GoodsReceipt) receiptDTO {
	out := receiptDTO{ID: g.ID, Reference: g.Reference, OrderID: g.OrderID, SupplierID: g.SupplierID, DeliveryNote: g.DeliveryNote,
		ReceivedBy: g.ReceivedBy, CreatedAt: ts(g.CreatedAt), Lines: make([]receiptLineDTO, 0, len(g.Lines))}
	for _, l := range g.Lines {
		assets := l.AssetIDs
		if assets == nil {
			assets = []string{}
		}
		out.Lines = append(out.Lines, receiptLineDTO{ID: l.ID, OrderLineID: l.OrderLineID, ProductID: l.ProductID, Quantity: l.Quantity,
			StorageLocationID: l.StorageLocationID, AssetIDs: assets})
	}
	return out
}

func (h *handler) listReceipts(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	res, err := h.svc.ListGoodsReceipts(r.Context(), principal(r), r.URL.Query().Get("orderId"), page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toList(res, toReceipt))
}

func (h *handler) getReceipt(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.GetGoodsReceipt(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toReceipt(out))
}

type receiptBody struct {
	OrderID         string `json:"orderId"`
	DeliveryNote    string `json:"deliveryNote"`
	AssetsAvailable bool   `json:"assetsAvailable"`
	Lines           []struct {
		OrderLineID       string `json:"orderLineId"`
		Quantity          int    `json:"quantity"`
		StorageLocationID string `json:"storageLocationId"`
		Units             []struct {
			SerialNumber string `json:"serialNumber"`
			AssetTag     string `json:"assetTag"`
		} `json:"units"`
		AssetLocationID string  `json:"assetLocationId"`
		WarrantyUntil   *string `json:"warrantyUntil"`
	} `json:"lines"`
}

func (h *handler) postReceipt(w http.ResponseWriter, r *http.Request) {
	var b receiptBody
	if !decode(w, r, &b) {
		return
	}
	in := application.ReceiptInput{OrderID: b.OrderID, DeliveryNote: b.DeliveryNote, AssetsAvailable: b.AssetsAvailable}
	for _, l := range b.Lines {
		line := application.ReceiptLineInput{OrderLineID: l.OrderLineID, Quantity: l.Quantity, StorageLocationID: l.StorageLocationID, AssetLocationID: l.AssetLocationID}
		if l.WarrantyUntil != nil && *l.WarrantyUntil != "" {
			t, err := time.Parse("2006-01-02", *l.WarrantyUntil)
			if err != nil {
				httpx.WriteError(w, http.StatusBadRequest, "inventory.invalid_request", "Dates must be in the form YYYY-MM-DD.")
				return
			}
			line.WarrantyUntil = &t
		}
		for _, u := range l.Units {
			line.Units = append(line.Units, application.ReceivedUnit{SerialNumber: u.SerialNumber, AssetTag: u.AssetTag})
		}
		in.Lines = append(in.Lines, line)
	}
	out, err := h.svc.PostGoodsReceipt(r.Context(), caller(w, r), principal(r), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toReceipt(out))
}
