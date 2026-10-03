// Package public holds the adapters that connect Inventory to the contracts
// of the modules it depends on (Products, Assets), so the Inventory
// application depends only on small interfaces of its own.
package public

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	assetspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/inventory/application"
	procurementpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/procurement/public"
	productspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/products/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Products adapts the Products directory to the questions inventory asks.
type Products struct{ dir *productspublic.Directory }

func NewProducts(dir *productspublic.Directory) *Products { return &Products{dir: dir} }

func (p *Products) Products(ctx context.Context, ids []string) (map[string]application.ProductInfo, error) {
	found, err := p.dir.Products(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]application.ProductInfo, len(found))
	for id, pr := range found {
		out[id] = application.ProductInfo{ID: pr.ID, Name: pr.Name, Active: pr.Active, Serialized: pr.Serialized,
			StockManaged: pr.StockManaged, AssetManaged: pr.AssetManaged}
	}
	return out, nil
}

// Assets adapts the Assets contract to the operations reservations need.
type Assets struct{ a *assetspublic.Assets }

func NewAssets(a *assetspublic.Assets) *Assets { return &Assets{a: a} }

func caller(actor audit.Actor, correlationID string) assetspublic.Caller {
	return assetspublic.Caller{Actor: actor, CorrelationID: correlationID}
}

func (x *Assets) Assets(ctx context.Context, ids []string) (map[string]application.AssetView, error) {
	found, err := x.a.Assets(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]application.AssetView, len(found))
	for id, a := range found {
		out[id] = application.AssetView{ID: a.ID, ProductID: a.ProductID, Status: a.Status}
	}
	return out, nil
}

// ReserveInTx reserves an available asset; any other status is ErrAssetUnavailable.
func (x *Assets) ReserveInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, assetID string) error {
	_, err := x.a.ReserveInTx(ctx, tx, caller(actor, correlationID), assetID)
	var tr *assetspublic.InvalidTransitionError
	if errors.As(err, &tr) {
		return application.ErrAssetUnavailable
	}
	return err
}

func (x *Assets) ReleaseReservationInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, assetID string) error {
	_, err := x.a.ReleaseReservationInTx(ctx, tx, caller(actor, correlationID), assetID)
	return err
}

func (x *Assets) AssignReservedInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, assetID string, to application.AssetAssignee, note string) error {
	_, err := x.a.AssignReservedInTx(ctx, tx, caller(actor, correlationID), assetID, assetspublic.Assignee{Type: to.Type, ID: to.ID}, note)
	if errors.Is(err, assetspublic.ErrAssigneeInvalid) {
		return application.ErrAssigneeInvalid
	}
	return err
}

// CreateReceivedInTx registers one asset from delivered goods.
func (x *Assets) CreateReceivedInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID string, in application.ReceivedAsset) (string, error) {
	var supplier *string
	if in.SupplierID != "" {
		supplier = &in.SupplierID
	}
	a, err := x.a.CreateReceivedInTx(ctx, tx, caller(actor, correlationID), assetspublic.Received{
		ProductID: in.ProductID, SerialNumber: in.SerialNumber, AssetTag: in.AssetTag, SupplierID: supplier, PurchasedAt: in.PurchasedAt,
		WarrantyUntil: in.WarrantyUntil, LocationID: in.LocationID, SourceID: in.SourceID, Available: in.Available,
	})
	if errors.Is(err, assetspublic.ErrConflict) {
		return "", application.ErrDuplicateAsset
	}
	if err != nil {
		return "", err
	}
	return a.ID, nil
}

// Orders adapts the Procurement contract to what goods receipt needs.
type Orders struct{ o *procurementpublic.Orders }

func NewOrders(o *procurementpublic.Orders) *Orders { return &Orders{o: o} }

func (x *Orders) Order(ctx context.Context, id string) (application.OrderView, error) {
	o, err := x.o.Order(ctx, id)
	if errors.Is(err, procurementpublic.ErrNotFound) {
		return application.OrderView{}, application.ErrNotFound
	}
	if err != nil {
		return application.OrderView{}, err
	}
	out := application.OrderView{ID: o.ID, Reference: o.Reference, SupplierID: o.SupplierID, Status: o.Status, Lines: make([]application.OrderLine, 0, len(o.Lines))}
	for _, l := range o.Lines {
		out.Lines = append(out.Lines, application.OrderLine{ID: l.ID, ProductID: l.ProductID, Quantity: l.Quantity, ReceivedQuantity: l.ReceivedQuantity})
	}
	return out, nil
}

func (x *Orders) RecordReceiptInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, orderID string, lines []application.OrderReceipt) error {
	receipt := make([]procurementpublic.ReceiptLine, 0, len(lines))
	for _, l := range lines {
		receipt = append(receipt, procurementpublic.ReceiptLine{LineID: l.LineID, Quantity: l.Quantity})
	}
	_, err := x.o.RecordReceiptInTx(ctx, tx, procurementpublic.Caller{Actor: actor, CorrelationID: correlationID}, orderID, receipt)
	var tr *procurementpublic.InvalidTransitionError
	switch {
	case errors.Is(err, procurementpublic.ErrOverReceipt):
		return application.ErrOverReceipt
	case errors.As(err, &tr):
		return application.ErrOrderNotReceivable
	case errors.Is(err, procurementpublic.ErrNotFound):
		return application.ErrNotFound
	}
	return err
}
