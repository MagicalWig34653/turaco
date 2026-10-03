// Package application holds the Asset use cases: registering individually
// tracked instances of Products, their lifecycle and their assignment history
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

// Asset lifecycle statuses (docs/domain/state-machines.md).
const (
	StatusReceived  = "received"
	StatusAvailable = "available"
	StatusReserved  = "reserved"
	StatusAssigned  = "assigned"
	StatusReturned  = "returned"
	StatusInRepair  = "in_repair"
	StatusRetired   = "retired"
	StatusDisposed  = "disposed"
	StatusLost      = "lost"
)

// Statuses lists every lifecycle status.
var Statuses = []string{StatusReceived, StatusAvailable, StatusReserved, StatusAssigned, StatusReturned, StatusInRepair, StatusRetired, StatusDisposed, StatusLost}

// Provisioning statuses (separate from the lifecycle).
var ProvisioningStatuses = []string{"not_required", "not_started", "pending", "in_progress", "ready", "failed"}

// Ownership types.
var OwnershipTypes = []string{"owned", "leased", "loaned"}

// Assignee types.
const (
	AssigneeUser     = "user"
	AssigneeTeam     = "team"
	AssigneeLocation = "location"
)

const (
	DefaultLimit = 50
	MaxLimit     = 200
	maxText      = 2000
	maxNote      = 500
	maxSerial    = 100
	maxTag       = 50
)

// Asset is an individually tracked instance of a Product.
type Asset struct {
	ID                 string
	Reference          string
	ProductID          string
	SerialNumber       *string
	AssetTag           *string
	Status             string
	StatusReason       *string
	ProvisioningStatus string
	OwnershipType      string
	SupplierID         *string
	SourceType         *string
	SourceID           *string
	PurchasedAt        *time.Time
	WarrantyUntil      *time.Time
	LocationID         *string
	Notes              *string
	Version            int
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Assignment is one period an Asset was assigned to a User, Team or Location.
type Assignment struct {
	ID           string
	AssetID      string
	AssigneeType string
	AssigneeID   string
	AssignedAt   time.Time
	AssignedBy   *string
	ReturnedAt   *time.Time
	Note         *string
}

// NewAsset is the input of Store.InsertTx; the service validated it.
type NewAsset struct {
	ProductID     string
	SerialNumber  *string
	AssetTag      *string
	Status        string
	OwnershipType string
	SupplierID    *string
	SourceType    *string
	SourceID      *string
	PurchasedAt   *time.Time
	WarrantyUntil *time.Time
	LocationID    *string
	Notes         *string
}

// Principal is the caller's asset authority. View (assets.view) reads all
// assets; Manage (assets.manage) also changes them. Every signed-in User may
// read the assets currently assigned to them.
type Principal struct {
	UserID string
	View   bool
	Manage bool
}

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
		return errors.New("assets: correlation id is required")
	}
	return nil
}

var (
	ErrNotFound         = errors.New("assets: not found")
	ErrForbidden        = errors.New("assets: forbidden")
	ErrInvalidCursor    = errors.New("assets: invalid cursor")
	ErrVersionConflict  = errors.New("assets: version conflict")
	ErrConflict         = errors.New("assets: conflict")
	ErrAssigneeInvalid  = errors.New("assets: assignee does not exist or is not active")
	ErrProductInvalid   = errors.New("assets: product does not exist or is not asset-managed")
	ErrReferenceInvalid = errors.New("assets: referenced supplier or location does not exist")
)

// InvalidTransitionError reports an operation the asset's status does not allow.
type InvalidTransitionError struct {
	Operation string
	From      string
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("assets: operation %s is not allowed in status %s", e.Operation, e.From)
}

// InvalidInputError carries a user-safe validation message.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return "assets: invalid input: " + e.Message }

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

// Result is one page; NextCursor is empty on the last page.
type Result struct {
	Items      []Asset
	NextCursor string
}

// Filter selects assets; all fields are optional.
type Filter struct {
	Status     string
	ProductID  string
	AssigneeID string // active assignment to this User or Team
	LocationID string
	// Query matches the reference, serial number or asset tag by prefix.
	Query string
	// AssignedToUser restricts to assets actively assigned to this User (my assets).
	AssignedToUser string
	Page           Page
}

// Store is the persistence port. Mutating methods run in the caller's
// transaction so lifecycle changes, assignments, audit and events commit together.
type Store interface {
	InsertTx(ctx context.Context, tx pgx.Tx, n NewAsset) (Asset, error)
	// LockTx returns the asset FOR UPDATE; ErrNotFound for an unknown id.
	LockTx(ctx context.Context, tx pgx.Tx, id string) (Asset, error)
	// UpdateTx stores every mutable column with version+1.
	UpdateTx(ctx context.Context, tx pgx.Tx, a Asset) (Asset, error)
	// ActiveAssignmentTx returns the active assignment of an asset, if any.
	ActiveAssignmentTx(ctx context.Context, tx pgx.Tx, assetID string) (*Assignment, error)
	OpenAssignmentTx(ctx context.Context, tx pgx.Tx, n Assignment) (Assignment, error)
	// CloseAssignmentTx ends the active assignment of an asset; it reports whether one was active.
	CloseAssignmentTx(ctx context.Context, tx pgx.Tx, assetID string, at time.Time) (bool, error)

	Get(ctx context.Context, id string) (Asset, error)
	// ByIDs returns the existing assets among the ids (UUIDs; others are ignored) with one query.
	ByIDs(ctx context.Context, ids []string) ([]Asset, error)
	// Lookup finds one asset by exact asset tag, serial number or reference (case-insensitive).
	// A serial number shared by several products is ambiguous and reported as ErrConflict.
	Lookup(ctx context.Context, code string) (Asset, error)
	// BySerial returns up to two assets with exactly this serial number (case-insensitive).
	BySerial(ctx context.Context, serial string) ([]Asset, error)
	List(ctx context.Context, f Filter) (Result, error)
	Assignments(ctx context.Context, assetID string) ([]Assignment, error)
	// UserHolders returns assetID -> User id for the assets that are currently assigned to a User.
	UserHolders(ctx context.Context, assetIDs []string) (map[string]string, error)
	// AssetsHeldByUsers returns userID -> asset ids currently assigned to the User, at most limit assets in total.
	AssetsHeldByUsers(ctx context.Context, userIDs []string, limit int) (map[string][]string, error)
	// InTx runs fn in one transaction.
	InTx(ctx context.Context, fn func(tx pgx.Tx) error) error
}

// Directory answers the Organization questions assets need.
type Directory interface {
	ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error)
	ActiveTeams(ctx context.Context, ids []string) (map[string]bool, error)
	ActiveLocations(ctx context.Context, ids []string) (map[string]bool, error)
	UserNames(ctx context.Context, ids []string) (map[string]string, error)
	TeamNames(ctx context.Context, ids []string) (map[string]string, error)
	LocationNames(ctx context.Context, ids []string) (map[string]string, error)
}

// ProductInfo is what assets need to know about a Product.
type ProductInfo struct {
	ID           string
	Name         string
	Active       bool
	AssetManaged bool
	Serialized   bool
}

// Products answers the Products questions assets need (adapter over products/public).
type Products interface {
	Products(ctx context.Context, ids []string) (map[string]ProductInfo, error)
}
