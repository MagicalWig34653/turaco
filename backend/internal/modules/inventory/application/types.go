// Package application holds the Inventory use cases: warehouses and storage
// locations, the immutable ledger of stock movements with its balances, and
// reservations of stock or serialized assets
// (docs/product/f4-inventory-design.md).
package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Inventory transaction types (docs/domain/core-data-model.md).
const (
	TxGoodsReceipt = "goods_receipt"
	TxReservation  = "reservation"
	TxRelease      = "release"
	TxIssue        = "issue"
	TxReturn       = "return"
	TxTransfer     = "transfer"
	TxCorrection   = "correction"
	TxDisposal     = "disposal"
)

// Reservation kinds and statuses.
const (
	KindQuantity = "quantity"
	KindAsset    = "asset"

	ResActive    = "active"
	ResFulfilled = "fulfilled"
	ResReleased  = "released"
	ResCancelled = "cancelled"
)

// ReservationStatuses lists the statuses the API filters by. `expired` is part
// of the state machine but nothing expires reservations yet.
var ReservationStatuses = []string{ResActive, ResFulfilled, ResReleased, ResCancelled}

const (
	DefaultLimit = 50
	MaxLimit     = 200
	// MaxQuantity bounds a single movement; balances are 32-bit integers.
	MaxQuantity = 1_000_000
	maxName     = 100
	maxReason   = 500
)

// Warehouse is a logical inventory boundary.
type Warehouse struct {
	ID         string
	Name       string
	LocationID *string
	Active     bool
	Version    int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// StorageLocation is a specific place within a Warehouse.
type StorageLocation struct {
	ID          string
	WarehouseID string
	Name        string
	Active      bool
	Version     int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Balance is the current quantity of a Product at a Storage Location.
type Balance struct {
	ProductID         string
	StorageLocationID string
	WarehouseID       string
	OnHand            int
	Reserved          int
	UpdatedAt         time.Time
}

// Available is the stock that can still be reserved or issued.
func (b Balance) Available() int { return b.OnHand - b.Reserved }

// Transaction is one immutable ledger row.
type Transaction struct {
	ID                string
	Type              string
	ProductID         string
	StorageLocationID string
	OnHandDelta       int
	ReservedDelta     int
	GroupID           string
	ReservationID     *string
	ContextType       *string
	ContextID         *string
	Reason            *string
	ActorUserID       *string
	CorrelationID     string
	CreatedAt         time.Time
}

// Reservation allocates stock quantity or one serialized asset for a record.
type Reservation struct {
	ID                string
	Kind              string
	ProductID         string
	StorageLocationID *string
	Quantity          *int
	AssetID           *string
	Status            string
	ContextType       *string
	ContextID         *string
	Reason            *string
	ClosedAt          *time.Time
	CreatedBy         *string
	Version           int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Principal is the caller's inventory authority: View (inventory.view) reads,
// Manage (inventory.manage) also changes.
type Principal struct {
	UserID string
	View   bool
	Manage bool
}

func (p Principal) canView() bool { return p.View || p.Manage }

// Caller identifies who performs a mutation and the request it belongs to.
type Caller struct {
	Actor         audit.Actor
	CorrelationID string
}

func (c Caller) validate() error {
	if err := c.Actor.Validate(); err != nil {
		return err
	}
	if c.CorrelationID == "" {
		return errors.New("inventory: correlation id is required")
	}
	return nil
}

var (
	ErrNotFound          = errors.New("inventory: not found")
	ErrForbidden         = errors.New("inventory: forbidden")
	ErrInvalidCursor     = errors.New("inventory: invalid cursor")
	ErrVersionConflict   = errors.New("inventory: version conflict")
	ErrConflict          = errors.New("inventory: conflict")
	ErrInsufficientStock = errors.New("inventory: not enough available stock")
	ErrProductInvalid    = errors.New("inventory: product does not exist, is inactive or is not tracked this way")
	ErrLocationInactive  = errors.New("inventory: storage location does not exist or is not active")
	ErrReferenceInvalid  = errors.New("inventory: referenced location does not exist")
	ErrAssetUnavailable  = errors.New("inventory: the asset cannot be reserved")
	ErrAssigneeInvalid   = errors.New("inventory: assignee does not exist or is not active")
	// ErrDuplicateAsset means a delivered unit's serial number or asset tag already exists.
	ErrDuplicateAsset = errors.New("inventory: an asset with this serial number or asset tag already exists")
	// ErrOrderNotReceivable means the purchase order cannot receive goods in its current status.
	ErrOrderNotReceivable = errors.New("inventory: the purchase order cannot receive goods in its current status")
	// ErrOverReceipt means a line would receive more than was ordered.
	ErrOverReceipt = errors.New("inventory: received quantity exceeds the ordered quantity")
)

// InvalidTransitionError reports an operation the reservation's status does not allow.
type InvalidTransitionError struct {
	Operation string
	From      string
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("inventory: operation %s is not allowed in status %s", e.Operation, e.From)
}

// InvalidInputError carries a user-safe validation message.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return "inventory: invalid input: " + e.Message }

func invalid(format string, args ...any) error {
	return &InvalidInputError{Message: fmt.Sprintf(format, args...)}
}

// Page is keyset pagination over the UUIDv7 primary key (ascending).
type Page struct {
	Limit  int
	Cursor string
}

func (p Page) Normalize() Page {
	if p.Limit <= 0 {
		p.Limit = DefaultLimit
	}
	if p.Limit > MaxLimit {
		p.Limit = MaxLimit
	}
	return p
}

// Result is one page of any item type; NextCursor is empty on the last page.
type Result[T any] struct {
	Items      []T
	NextCursor string
}

// Origin is the record a movement or reservation belongs to.
type Origin struct {
	Type string
	ID   string
}

// StockFilter selects balances.
type StockFilter struct {
	ProductID         string
	WarehouseID       string
	StorageLocationID string
	// WithStockOnly hides balances that are zero.
	WithStockOnly bool
	Page          Page
}

// TransactionFilter selects ledger rows (newest first).
type TransactionFilter struct {
	ProductID         string
	StorageLocationID string
	Type              string
	ContextType       string
	ContextID         string
	Page              Page
}

// ReservationFilter selects reservations.
type ReservationFilter struct {
	Status      string
	ProductID   string
	ContextType string
	ContextID   string
	Page        Page
}

// NewReservation is the input of Store.InsertReservationTx.
type NewReservation struct {
	Kind              string
	ProductID         string
	StorageLocationID *string
	Quantity          *int
	AssetID           *string
	ContextType       *string
	ContextID         *string
	CreatedBy         *string
}

// Store is the persistence port. Mutating methods run in the caller's
// transaction so ledger rows, balances, reservations, audit and events commit
// together.
type Store interface {
	InTx(ctx context.Context, fn func(tx pgx.Tx) error) error

	InsertWarehouseTx(ctx context.Context, tx pgx.Tx, name string, locationID *string) (Warehouse, error)
	LockWarehouseTx(ctx context.Context, tx pgx.Tx, id string) (Warehouse, error)
	UpdateWarehouseTx(ctx context.Context, tx pgx.Tx, w Warehouse) (Warehouse, error)
	GetWarehouse(ctx context.Context, id string) (Warehouse, error)
	ListWarehouses(ctx context.Context, includeInactive bool, page Page) (Result[Warehouse], error)

	InsertLocationTx(ctx context.Context, tx pgx.Tx, warehouseID, name string) (StorageLocation, error)
	LockLocationTx(ctx context.Context, tx pgx.Tx, id string) (StorageLocation, error)
	UpdateLocationTx(ctx context.Context, tx pgx.Tx, l StorageLocation) (StorageLocation, error)
	GetLocation(ctx context.Context, id string) (StorageLocation, error)
	// LocationLabels returns id -> "Warehouse / Storage location" for existing storage locations.
	LocationLabels(ctx context.Context, ids []string) (map[string]string, error)
	// ActiveLocationsTx returns id -> true for active storage locations of active warehouses.
	ActiveLocationsTx(ctx context.Context, tx pgx.Tx, ids []string) (map[string]bool, error)
	ListLocations(ctx context.Context, warehouseID string, includeInactive bool, page Page) (Result[StorageLocation], error)

	// LockBalancesTx locks existing balance rows of a product in a fixed order.
	LockBalancesTx(ctx context.Context, tx pgx.Tx, productID string, locationIDs []string) error
	// The stock primitives return false when the guarded condition fails
	// (not enough available stock, reserved below zero, ...).
	AddStockTx(ctx context.Context, tx pgx.Tx, productID, locationID string, qty int) error
	RemoveStockTx(ctx context.Context, tx pgx.Tx, productID, locationID string, qty int) (bool, error)
	ReserveStockTx(ctx context.Context, tx pgx.Tx, productID, locationID string, qty int) (bool, error)
	UnreserveStockTx(ctx context.Context, tx pgx.Tx, productID, locationID string, qty int) (bool, error)
	IssueReservedStockTx(ctx context.Context, tx pgx.Tx, productID, locationID string, qty int) (bool, error)
	CorrectStockTx(ctx context.Context, tx pgx.Tx, productID, locationID string, delta int) (bool, error)
	// InsertTransactionTx appends a ledger row; an empty GroupID starts a new group.
	InsertTransactionTx(ctx context.Context, tx pgx.Tx, t Transaction) (Transaction, error)

	InsertReservationTx(ctx context.Context, tx pgx.Tx, n NewReservation) (Reservation, error)
	LockReservationTx(ctx context.Context, tx pgx.Tx, id string) (Reservation, error)
	CloseReservationTx(ctx context.Context, tx pgx.Tx, id, status string, reason *string) (Reservation, error)
	GetReservation(ctx context.Context, id string) (Reservation, error)

	ListStock(ctx context.Context, f StockFilter) (Result[Balance], error)
	ListTransactions(ctx context.Context, f TransactionFilter) (Result[Transaction], error)
	ListReservations(ctx context.Context, f ReservationFilter) (Result[Reservation], error)

	InsertGoodsReceiptTx(ctx context.Context, tx pgx.Tx, orderID, supplierID string, deliveryNote, receivedBy *string) (GoodsReceipt, error)
	InsertGoodsReceiptLineTx(ctx context.Context, tx pgx.Tx, receiptID string, l GoodsReceiptLine) (GoodsReceiptLine, error)
	AddReceiptAssetTx(ctx context.Context, tx pgx.Tx, receiptLineID, assetID string) error
	// GetGoodsReceipt returns a receipt with its lines and the assets they created.
	GetGoodsReceipt(ctx context.Context, id string) (GoodsReceipt, error)
	ListGoodsReceipts(ctx context.Context, orderID string, page Page) (Result[GoodsReceipt], error)
}

// Directory answers the Organization questions inventory needs.
type Directory interface {
	ActiveLocations(ctx context.Context, ids []string) (map[string]bool, error)
	ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error)
	ActiveTeams(ctx context.Context, ids []string) (map[string]bool, error)
}

// ProductInfo is what inventory needs to know about a Product.
type ProductInfo struct {
	ID           string
	Name         string
	Active       bool
	Serialized   bool
	StockManaged bool
	AssetManaged bool
}

// Products answers the Products questions inventory needs (adapter over products/public).
type Products interface {
	Products(ctx context.Context, ids []string) (map[string]ProductInfo, error)
}

// AssetView is the minimal asset view reservations need.
type AssetView struct {
	ID        string
	ProductID string
	Status    string
}

// AssetAssignee names who receives a reserved asset.
type AssetAssignee struct {
	Type string
	ID   string
}

// GoodsReceipt is an immutable record of delivered goods.
type GoodsReceipt struct {
	ID           string
	Reference    string
	OrderID      string
	SupplierID   string
	DeliveryNote *string
	ReceivedBy   *string
	CreatedAt    time.Time
	Lines        []GoodsReceiptLine
}

// GoodsReceiptLine is the delivery of one purchase order line.
type GoodsReceiptLine struct {
	ID                string
	OrderLineID       string
	ProductID         string
	Quantity          int
	StorageLocationID *string
	AssetIDs          []string
}

// ReceivedAsset describes goods that become an Asset.
type ReceivedAsset struct {
	ProductID     string
	SerialNumber  string
	AssetTag      string
	SupplierID    string
	PurchasedAt   *time.Time
	WarrantyUntil *time.Time
	LocationID    *string
	// Available registers the asset as available instead of received (still to be checked).
	Available bool
	// SourceID is the goods receipt the asset came from.
	SourceID string
}

// OrderLine is a purchase order line as goods receipt sees it.
type OrderLine struct {
	ID               string
	ProductID        string
	Quantity         int
	ReceivedQuantity int
}

// OrderView is a purchase order as goods receipt sees it.
type OrderView struct {
	ID         string
	Reference  string
	SupplierID string
	Status     string
	Lines      []OrderLine
}

// OrderReceipt is the quantity booked on one order line.
type OrderReceipt struct {
	LineID   string
	Quantity int
}

// Orders is the Procurement contract (procurement/public adapter). It reports
// over-delivery as ErrOverReceipt and an order in the wrong status as
// ErrOrderNotReceivable, and an unknown order as ErrNotFound.
type Orders interface {
	Order(ctx context.Context, id string) (OrderView, error)
	RecordReceiptInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, orderID string, lines []OrderReceipt) error
}

// Assets is the Assets contract (assets/public adapter).
type Assets interface {
	// CreateReceivedInTx registers one asset from delivered goods and returns its id;
	// a duplicate serial number or asset tag is ErrDuplicateAsset.
	CreateReceivedInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID string, in ReceivedAsset) (string, error)
	Assets(ctx context.Context, ids []string) (map[string]AssetView, error)
	ReserveInTx(ctx context.Context, tx pgx.Tx, c audit.Actor, correlationID, assetID string) error
	ReleaseReservationInTx(ctx context.Context, tx pgx.Tx, c audit.Actor, correlationID, assetID string) error
	AssignReservedInTx(ctx context.Context, tx pgx.Tx, c audit.Actor, correlationID, assetID string, to AssetAssignee, note string) error
}
