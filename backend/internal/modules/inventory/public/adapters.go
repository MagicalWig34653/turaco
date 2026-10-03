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
