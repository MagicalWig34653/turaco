// Package application holds the Procurement use cases: suppliers, internal
// acquisition needs (Procurement Requests) and Purchase Orders with their
// approval and receipt tracking (docs/product/f4-inventory-design.md).
package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Purchase Order statuses (docs/domain/state-machines.md).
const (
	POStatusDraft             = "draft"
	POStatusPendingApproval   = "pending_approval"
	POStatusApproved          = "approved"
	POStatusSent              = "sent"
	POStatusAcknowledged      = "acknowledged"
	POStatusPartiallyReceived = "partially_received"
	POStatusReceived          = "received"
	POStatusClosed            = "closed"
	POStatusCancelled         = "cancelled"
)

// POStatuses lists every Purchase Order status.
var POStatuses = []string{POStatusDraft, POStatusPendingApproval, POStatusApproved, POStatusSent, POStatusAcknowledged,
	POStatusPartiallyReceived, POStatusReceived, POStatusClosed, POStatusCancelled}

// Procurement Request (need) statuses.
const (
	NeedOpen      = "open"
	NeedOrdered   = "ordered"
	NeedFulfilled = "fulfilled"
	NeedCancelled = "cancelled"
)

// NeedStatuses lists every need status.
var NeedStatuses = []string{NeedOpen, NeedOrdered, NeedFulfilled, NeedCancelled}

const (
	// SubjectType is the Approval subject type of a Purchase Order.
	SubjectType = "purchase_order"

	DefaultLimit = 50
	MaxLimit     = 200
	MaxQuantity  = 1_000_000
	MaxLines     = 100
	// MaxPriceCents bounds a unit price (1 billion in major units).
	MaxPriceCents = 100_000_000_000
	maxName       = 150
	maxText       = 2000
	maxReason     = 500
)

// Supplier is an organization goods are purchased from.
type Supplier struct {
	ID               string
	Name             string
	AccountReference *string
	Active           bool
	Version          int
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Need is an internal Procurement Request: something that has to be acquired.
type Need struct {
	ID           string
	Reference    string
	ProductID    string
	Quantity     int
	Status       string
	StatusReason *string
	ContextType  *string
	ContextID    *string
	Notes        *string
	RequestedBy  *string
	Version      int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Order is a Purchase Order against one Supplier.
type Order struct {
	ID           string
	Reference    string
	SupplierID   string
	Status       string
	StatusReason *string
	Currency     string
	Notes        *string
	CreatedBy    *string
	SentAt       *time.Time
	ClosedAt     *time.Time
	Version      int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Line is one Purchase Order line.
type Line struct {
	ID               string
	OrderID          string
	LineNo           int
	ProductID        string
	Quantity         int
	UnitPriceCents   int64
	ReceivedQuantity int
	RequestID        *string
	CreatedAt        time.Time
}

// Open is the quantity still expected.
func (l Line) Open() int { return l.Quantity - l.ReceivedQuantity }

// Principal is the caller's procurement authority: View (procurement.view)
// reads, Manage (procurement.manage) also changes.
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
		return errors.New("procurement: correlation id is required")
	}
	return nil
}

var (
	ErrNotFound           = errors.New("procurement: not found")
	ErrForbidden          = errors.New("procurement: forbidden")
	ErrInvalidCursor      = errors.New("procurement: invalid cursor")
	ErrVersionConflict    = errors.New("procurement: version conflict")
	ErrConflict           = errors.New("procurement: conflict")
	ErrProductInvalid     = errors.New("procurement: product does not exist or is inactive")
	ErrSupplierInvalid    = errors.New("procurement: supplier does not exist or is inactive")
	ErrNeedUnavailable    = errors.New("procurement: the procurement request is not open or is for another product")
	ErrNoEligibleApprover = errors.New("procurement: no eligible approver")
	ErrOverReceipt        = errors.New("procurement: received quantity exceeds the ordered quantity")
)

// InvalidTransitionError reports an operation the status does not allow.
type InvalidTransitionError struct {
	Operation string
	From      string
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("procurement: operation %s is not allowed in status %s", e.Operation, e.From)
}

// InvalidInputError carries a user-safe validation message.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return "procurement: invalid input: " + e.Message }

func invalid(format string, args ...any) error {
	return &InvalidInputError{Message: fmt.Sprintf(format, args...)}
}

// Page is keyset pagination over the UUIDv7 primary key.
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

// NeedFilter selects needs.
type NeedFilter struct {
	Status      string
	ProductID   string
	ContextType string
	ContextID   string
	Page        Page
}

// OrderFilter selects purchase orders.
type OrderFilter struct {
	Status     string
	SupplierID string
	Page       Page
}

// Store is the persistence port. Mutating methods run in the caller's
// transaction so changes, audit and events commit together.
type Store interface {
	InTx(ctx context.Context, fn func(tx pgx.Tx) error) error

	InsertSupplierTx(ctx context.Context, tx pgx.Tx, name string, account *string) (Supplier, error)
	LockSupplierTx(ctx context.Context, tx pgx.Tx, id string) (Supplier, error)
	UpdateSupplierTx(ctx context.Context, tx pgx.Tx, s Supplier) (Supplier, error)
	GetSupplier(ctx context.Context, id string) (Supplier, error)
	// SupplierNames returns id -> name for existing suppliers.
	SupplierNames(ctx context.Context, ids []string) (map[string]string, error)
	ActiveSuppliersTx(ctx context.Context, tx pgx.Tx, ids []string) (map[string]bool, error)
	ListSuppliers(ctx context.Context, prefix string, includeInactive bool, page Page) (Result[Supplier], error)

	InsertNeedTx(ctx context.Context, tx pgx.Tx, n Need) (Need, error)
	LockNeedTx(ctx context.Context, tx pgx.Tx, id string) (Need, error)
	UpdateNeedTx(ctx context.Context, tx pgx.Tx, n Need) (Need, error)
	GetNeed(ctx context.Context, id string) (Need, error)
	ListNeeds(ctx context.Context, f NeedFilter) (Result[Need], error)

	InsertOrderTx(ctx context.Context, tx pgx.Tx, supplierID, currency string, notes, createdBy *string) (Order, error)
	LockOrderTx(ctx context.Context, tx pgx.Tx, id string) (Order, error)
	UpdateOrderTx(ctx context.Context, tx pgx.Tx, o Order) (Order, error)
	GetOrder(ctx context.Context, id string) (Order, error)
	ListOrders(ctx context.Context, f OrderFilter) (Result[Order], error)

	// LinesTx returns the lines of an order in line order (FOR UPDATE when lock is set).
	LinesTx(ctx context.Context, tx pgx.Tx, orderID string, lock bool) ([]Line, error)
	Lines(ctx context.Context, orderID string) ([]Line, error)
	InsertLineTx(ctx context.Context, tx pgx.Tx, l Line) (Line, error)
	UpdateLineTx(ctx context.Context, tx pgx.Tx, l Line) (Line, error)
	DeleteLineTx(ctx context.Context, tx pgx.Tx, orderID, lineID string) (bool, error)
	// ReceiveLineTx adds to the received quantity while it stays within the ordered quantity.
	ReceiveLineTx(ctx context.Context, tx pgx.Tx, orderID, lineID string, qty int) (bool, error)
	// ClearLineRequestsTx detaches the needs from all lines of an order and returns their ids.
	ClearLineRequestsTx(ctx context.Context, tx pgx.Tx, orderID string, onlyUnfulfilled bool) ([]string, error)
}

// Directory answers the Organization questions procurement needs.
type Directory interface {
	ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error)
}

// ProductInfo is what procurement needs to know about a Product.
type ProductInfo struct {
	ID     string
	Name   string
	Active bool
	// Serialized, StockManaged and AssetManaged say how goods of this product are tracked once received.
	Serialized   bool
	StockManaged bool
	AssetManaged bool
}

// Tracking says how received goods of a product are tracked.
func (p ProductInfo) Tracking() string {
	switch {
	case p.Serialized && p.AssetManaged:
		return "asset"
	case p.StockManaged && !p.Serialized:
		return "stock"
	}
	return "none"
}

// Products answers the Products questions procurement needs (adapter over products/public).
type Products interface {
	Products(ctx context.Context, ids []string) (map[string]ProductInfo, error)
}

// ApprovalRequest describes the approval step requested for a Purchase Order.
type ApprovalRequest struct {
	SubjectID       string
	Label           string
	ApproverUserID  *string
	ApproverTeamID  *string
	ExcludedUserIDs []string
}

// Approvals is the Approvals contract (approvals/public adapter).
type Approvals interface {
	// RequestInTx creates a pending approval; ErrNoEligibleApprover when nobody could decide it.
	RequestInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID string, r ApprovalRequest) error
	CancelBySubjectInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, subjectID string) error
}
