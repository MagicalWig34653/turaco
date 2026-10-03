// Package public is the Assets module's contract for other modules: Inventory
// creates received assets and moves reserved assets through their lifecycle
// inside its own transaction, without touching Assets storage.
package public

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/assets/application"
	productspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/products/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Caller identifies who acts and the operation the change belongs to.
type Caller struct {
	Actor         audit.Actor
	CorrelationID string
}

func ac(c Caller) application.Caller {
	return application.Caller{Actor: c.Actor, CorrelationID: c.CorrelationID}
}

// Assignee names who receives an asset.
type Assignee = application.Assignee

// Errors other modules may need to recognize.
var (
	ErrNotFound         = application.ErrNotFound
	ErrConflict         = application.ErrConflict
	ErrProductInvalid   = application.ErrProductInvalid
	ErrVersionConflict  = application.ErrVersionConflict
	ErrAssigneeInvalid  = application.ErrAssigneeInvalid
	ErrReferenceInvalid = application.ErrReferenceInvalid
)

// InvalidTransitionError and InvalidInputError are the module's refusal types.
type (
	InvalidTransitionError = application.InvalidTransitionError
	InvalidInputError      = application.InvalidInputError
)

// Received describes goods that become an asset.
type Received struct {
	ProductID     string
	SerialNumber  string
	AssetTag      string
	SupplierID    *string
	PurchasedAt   *time.Time
	WarrantyUntil *time.Time
	LocationID    *string
	// SourceID is the goods receipt the asset came from.
	SourceID string
	// Available registers the asset as available instead of received (still to be checked).
	Available bool
}

// Asset is the minimal view other modules get.
type Asset struct {
	ID           string
	Reference    string
	ProductID    string
	SerialNumber *string
	AssetTag     *string
	Status       string
	Version      int
}

// Assets is the module's public service.
type Assets struct{ svc *application.Service }

func New(svc *application.Service) *Assets { return &Assets{svc: svc} }

func view(a application.Asset) Asset {
	return Asset{ID: a.ID, Reference: a.Reference, ProductID: a.ProductID, SerialNumber: a.SerialNumber, AssetTag: a.AssetTag, Status: a.Status, Version: a.Version}
}

// CreateReceivedInTx registers an asset in status "received" from a goods receipt.
func (a *Assets) CreateReceivedInTx(ctx context.Context, tx pgx.Tx, c Caller, in Received) (Asset, error) {
	source, sourceType := in.SourceID, "goods_receipt"
	status := application.StatusReceived
	if in.Available {
		status = application.StatusAvailable
	}
	created, err := a.svc.CreateInTx(ctx, tx, ac(c), application.CreateInput{
		ProductID: in.ProductID, SerialNumber: in.SerialNumber, AssetTag: in.AssetTag, Status: status,
		SupplierID: in.SupplierID, PurchasedAt: in.PurchasedAt, WarrantyUntil: in.WarrantyUntil, LocationID: in.LocationID,
	}, &sourceType, &source)
	if err != nil {
		return Asset{}, err
	}
	return view(created), nil
}

// ReserveInTx moves an available asset to reserved.
func (a *Assets) ReserveInTx(ctx context.Context, tx pgx.Tx, c Caller, assetID string) (Asset, error) {
	out, err := a.svc.TransitionInTx(ctx, tx, ac(c), assetID, nil, application.OpReserve, application.Params{})
	return view(out), err
}

// ReleaseReservationInTx moves a reserved asset back to available.
func (a *Assets) ReleaseReservationInTx(ctx context.Context, tx pgx.Tx, c Caller, assetID string) (Asset, error) {
	out, err := a.svc.TransitionInTx(ctx, tx, ac(c), assetID, nil, application.OpReleaseReservation, application.Params{})
	return view(out), err
}

// AssignReservedInTx assigns a reserved asset (fulfilling its reservation).
func (a *Assets) AssignReservedInTx(ctx context.Context, tx pgx.Tx, c Caller, assetID string, to Assignee, note string) (Asset, error) {
	out, err := a.svc.TransitionInTx(ctx, tx, ac(c), assetID, nil, application.OpAssignReserved, application.Params{Assignee: to, Note: note})
	return view(out), err
}

// Assets returns id -> Asset for existing assets.
func (a *Assets) Assets(ctx context.Context, ids []string) (map[string]Asset, error) {
	out := make(map[string]Asset, len(ids))
	for _, id := range ids {
		got, err := a.svc.GetPlain(ctx, id)
		if err == application.ErrNotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[id] = view(got)
	}
	return out, nil
}

// Products adapts the Products directory to the questions assets ask.
type Products struct{ dir *productspublic.Directory }

func NewProducts(dir *productspublic.Directory) *Products { return &Products{dir: dir} }

func (p *Products) Products(ctx context.Context, ids []string) (map[string]application.ProductInfo, error) {
	found, err := p.dir.Products(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]application.ProductInfo, len(found))
	for id, pr := range found {
		out[id] = application.ProductInfo{ID: pr.ID, Name: pr.Name, Active: pr.Active, AssetManaged: pr.AssetManaged, Serialized: pr.Serialized}
	}
	return out, nil
}

// DeviceSnapshot is what another module (service desk) remembers about an asset.
type DeviceSnapshot = application.DeviceSnapshot

// Snapshot returns a snapshot of an asset; with a holder it must currently be assigned to that User.
func (a *Assets) Snapshot(ctx context.Context, assetID, holderUserID string) (DeviceSnapshot, error) {
	return a.svc.SnapshotFor(ctx, assetID, holderUserID)
}
