package application

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	maxReceiptLines = 100
	maxReceiptUnits = 500
	maxDeliveryNote = 100
)

// ReceivedUnit is one delivered unit of a serialized product.
type ReceivedUnit struct {
	SerialNumber string
	AssetTag     string
}

// ReceiptLineInput is the delivery of one purchase order line.
type ReceiptLineInput struct {
	OrderLineID string
	Quantity    int
	// StorageLocationID is where quantity-tracked stock goes (required for such products).
	StorageLocationID string
	// Units lists the delivered units of a serialized product (one per piece, serial number required).
	Units []ReceivedUnit
	// AssetLocationID is the Organization Location new assets are placed at (optional).
	AssetLocationID string
	WarrantyUntil   *time.Time
}

// ReceiptInput is a goods receipt against a purchase order.
type ReceiptInput struct {
	OrderID      string
	DeliveryNote string
	// AssetsAvailable registers new assets as available instead of received (to be checked).
	AssetsAvailable bool
	Lines           []ReceiptLineInput
}

type receiptKind int

const (
	kindUntracked receiptKind = iota
	kindStock
	kindAsset
)

func classify(p ProductInfo) receiptKind {
	switch {
	case p.Serialized && p.AssetManaged:
		return kindAsset
	case p.StockManaged && !p.Serialized:
		return kindStock
	}
	return kindUntracked
}

// PostGoodsReceipt books a delivery against a purchase order in one
// transaction: the order's received quantities, the stock of quantity-tracked
// products (ledger rows at the chosen storage location) and one Asset per
// delivered unit of serialized products. Over-delivery and an order that
// cannot receive goods are refused and nothing is booked. The posted receipt
// is immutable. Requires inventory.manage.
func (s *Service) PostGoodsReceipt(ctx context.Context, c Caller, p Principal, in ReceiptInput) (GoodsReceipt, error) {
	if err := c.validate(); err != nil {
		return GoodsReceipt{}, err
	}
	if !p.Manage {
		return GoodsReceipt{}, ErrForbidden
	}
	note := strings.TrimSpace(in.DeliveryNote)
	if len(note) > maxDeliveryNote {
		return GoodsReceipt{}, invalid("the delivery note must be at most %d characters", maxDeliveryNote)
	}
	if note != "" {
		if _, err := cleanName(note); err != nil {
			return GoodsReceipt{}, invalid("the delivery note must not contain control or invisible formatting characters")
		}
	}
	if len(in.Lines) == 0 || len(in.Lines) > maxReceiptLines {
		return GoodsReceipt{}, invalid("a receipt needs between 1 and %d lines", maxReceiptLines)
	}
	order, err := s.orders.Order(ctx, in.OrderID)
	if err != nil {
		return GoodsReceipt{}, err
	}
	byLine := map[string]OrderLine{}
	for _, l := range order.Lines {
		byLine[l.ID] = l
	}
	ids := make([]string, 0, len(byLine))
	for _, l := range order.Lines {
		ids = append(ids, l.ProductID)
	}
	products, err := s.products.Products(ctx, ids)
	if err != nil {
		return GoodsReceipt{}, fmt.Errorf("load products: %w", err)
	}
	units := 0
	seen := map[string]bool{}
	kinds := make([]receiptKind, len(in.Lines))
	for i, l := range in.Lines {
		ol, ok := byLine[l.OrderLineID]
		if !ok {
			return GoodsReceipt{}, invalid("line %d does not belong to the purchase order", i+1)
		}
		if seen[l.OrderLineID] {
			return GoodsReceipt{}, invalid("an order line may appear only once per receipt")
		}
		seen[l.OrderLineID] = true
		if err := checkQuantity(l.Quantity); err != nil {
			return GoodsReceipt{}, err
		}
		kinds[i] = classify(products[ol.ProductID])
		switch kinds[i] {
		case kindStock:
			if l.StorageLocationID == "" {
				return GoodsReceipt{}, invalid("line %d needs a storage location", i+1)
			}
		case kindAsset:
			if len(l.Units) != l.Quantity {
				return GoodsReceipt{}, invalid("line %d needs one serial number per piece (%d expected, %d given)", i+1, l.Quantity, len(l.Units))
			}
			units += len(l.Units)
		}
	}
	if units > maxReceiptUnits {
		return GoodsReceipt{}, invalid("a receipt may create at most %d assets", maxReceiptUnits)
	}

	var out GoodsReceipt
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		// The order is locked first (inside RecordReceiptInTx); stock rows and assets follow.
		receipt := make([]OrderReceipt, 0, len(in.Lines))
		for _, l := range in.Lines {
			receipt = append(receipt, OrderReceipt{LineID: l.OrderLineID, Quantity: l.Quantity})
		}
		if err := s.orders.RecordReceiptInTx(ctx, tx, c.Actor, c.CorrelationID, in.OrderID, receipt); err != nil {
			return err
		}
		gr, err := s.store.InsertGoodsReceiptTx(ctx, tx, in.OrderID, order.SupplierID, strPtr(note), userPtr(c))
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		origin := Origin{Type: "goods_receipt", ID: gr.ID}
		for i, l := range in.Lines {
			ol := byLine[l.OrderLineID]
			line := GoodsReceiptLine{OrderLineID: l.OrderLineID, ProductID: ol.ProductID, Quantity: l.Quantity}
			switch kinds[i] {
			case kindStock:
				if _, err := s.ReceiveInTx(ctx, tx, c, StockMove{ProductID: ol.ProductID, StorageLocationID: l.StorageLocationID, Quantity: l.Quantity, Origin: origin}); err != nil {
					return err
				}
				loc := l.StorageLocationID
				line.StorageLocationID = &loc
			}
			saved, err := s.store.InsertGoodsReceiptLineTx(ctx, tx, gr.ID, line)
			if err != nil {
				return err
			}
			if kinds[i] == kindAsset {
				for _, u := range l.Units {
					id, err := s.assets.CreateReceivedInTx(ctx, tx, c.Actor, c.CorrelationID, ReceivedAsset{
						ProductID: ol.ProductID, SerialNumber: u.SerialNumber, AssetTag: u.AssetTag, SupplierID: order.SupplierID,
						PurchasedAt: &now, WarrantyUntil: l.WarrantyUntil, LocationID: strPtr2(l.AssetLocationID), Available: in.AssetsAvailable, SourceID: gr.ID,
					})
					if err != nil {
						return err
					}
					if err := s.store.AddReceiptAssetTx(ctx, tx, saved.ID, id); err != nil {
						return err
					}
					saved.AssetIDs = append(saved.AssetIDs, id)
				}
			}
			gr.Lines = append(gr.Lines, saved)
		}
		out = gr
		meta := map[string]any{"orderId": in.OrderID, "lines": len(gr.Lines), "units": units}
		if err := recordAudit(ctx, tx, c, "inventory.goods_receipt.posted", "goods_receipt", gr.ID, nil, nil, meta); err != nil {
			return err
		}
		return publish(ctx, tx, c, "GoodsReceived", map[string]any{"receiptId": gr.ID, "orderId": in.OrderID, "supplierId": order.SupplierID})
	})
	if err != nil {
		return GoodsReceipt{}, err
	}
	return out, nil
}

func strPtr2(s string) *string { return strPtr(s) }

// GetGoodsReceipt returns a receipt with its lines. Requires inventory.view.
func (s *Service) GetGoodsReceipt(ctx context.Context, p Principal, id string) (GoodsReceipt, error) {
	if !p.canView() {
		return GoodsReceipt{}, ErrForbidden
	}
	return s.store.GetGoodsReceipt(ctx, id)
}

// ListGoodsReceipts lists receipts, newest first. Requires inventory.view.
func (s *Service) ListGoodsReceipts(ctx context.Context, p Principal, orderID string, page Page) (Result[GoodsReceipt], error) {
	if !p.canView() {
		return Result[GoodsReceipt]{}, ErrForbidden
	}
	return s.store.ListGoodsReceipts(ctx, orderID, page.Normalize())
}
