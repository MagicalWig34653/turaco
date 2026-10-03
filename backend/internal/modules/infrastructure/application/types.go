// Package application holds the Infrastructure use cases: Buildings, Rooms and
// Racks below an Organization Location (the Site), Rack Placements of Assets
// and Virtual Machines (docs/product/f7-infrastructure-change-design.md).
package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Rack faces.
const (
	FaceFront = "front"
	FaceRear  = "rear"
)

// Placement removal reason codes. ReasonMoved is set by MoveAsset only.
const (
	ReasonMoved           = "moved"
	ReasonRelocated       = "relocated"
	ReasonReplaced        = "replaced"
	ReasonDecommissioned  = "decommissioned"
	ReasonErrorCorrection = "error_correction"
	ReasonOther           = "other"
)

// RemovalReasons are the codes a caller may give to RemoveAsset.
var RemovalReasons = []string{ReasonRelocated, ReasonReplaced, ReasonDecommissioned, ReasonErrorCorrection, ReasonOther}

// Virtual Machine states and decommission reason codes.
const (
	VMRunning        = "running"
	VMStopped        = "stopped"
	VMUnknown        = "unknown"
	VMDecommissioned = "decommissioned"
)

// VMStates lists every state; VMSettableStates the ones ChangeVMState accepts.
var (
	VMStates         = []string{VMRunning, VMStopped, VMUnknown, VMDecommissioned}
	VMSettableStates = []string{VMRunning, VMStopped, VMUnknown}
	// VMDecommissionReasons are the codes DecommissionVM accepts.
	VMDecommissionReasons = []string{"retired", "migrated", "deleted", "other"}
)

const (
	DefaultLimit = 50
	MaxLimit     = 200
	// MaxRackHeight is the highest rack in units.
	MaxRackHeight = 60
	// MaxVCPU and MaxMemoryMB bound hand-entered VM sizes.
	MaxVCPU     = 1024
	MaxMemoryMB = 16_777_216
	maxName     = 100
	maxAddress  = 200
	maxFloor    = 20
	maxNetNote  = 500
	maxNotes    = 2000
	maxHost     = 253
	// MaxTreeSites bounds the site tree; at most MaxTreeSites*20 buildings are returned.
	MaxTreeSites = 500
	// MaxWarnings bounds the placement warnings returned; MaxWarningScan bounds
	// the active placements checked per request, in batches of warningBatch.
	MaxWarnings    = 100
	MaxWarningScan = 5000
	warningBatch   = 500
)

// Building is a physical building at a Site (an Organization Location).
type Building struct {
	ID             string
	SiteLocationID string
	Name           string
	AddressNote    *string
	Active         bool
	Version        int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Room is a room in a Building.
type Room struct {
	ID         string
	BuildingID string
	Name       string
	Floor      *string
	Active     bool
	Version    int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Rack is a rack in a Room with a fixed height in units (U).
type Rack struct {
	ID        string
	RoomID    string
	Name      string
	HeightU   int
	Active    bool
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Placement records where an Asset sits in a Rack. A removed or moved
// placement stays as history (RemovedAt set).
type Placement struct {
	ID            string
	RackID        string
	AssetID       string
	UPosition     int
	HeightU       int
	Face          string
	PlacedBy      *string
	PlacedAt      time.Time
	RemovedAt     *time.Time
	RemovedBy     *string
	RemovalReason *string
	PreviousID    *string
	Version       int
}

// Top is the highest unit the placement occupies.
func (p Placement) Top() int { return p.UPosition + p.HeightU - 1 }

// PlacementInput describes where an Asset goes.
type PlacementInput struct {
	RackID    string
	AssetID   string
	UPosition int
	HeightU   int
	Face      string
}

// RackDetail is a Rack with its active placements (the rack elevation data).
type RackDetail struct {
	Rack       Rack
	Placements []Placement
}

// VirtualMachine is a hand-entered Virtual Machine.
type VirtualMachine struct {
	ID                 string
	Name               string
	State              string
	HypervisorAssetID  *string
	VCPU               int
	MemoryMB           int
	ManagementAddress  *string
	NetworkNote        *string
	Notes              *string
	DecommissionReason *string
	DecommissionedAt   *time.Time
	Version            int
	CreatedBy          *string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// VMInput creates a Virtual Machine.
type VMInput struct {
	Name              string
	State             string
	HypervisorAssetID *string
	VCPU              int
	MemoryMB          int
	ManagementAddress string
	NetworkNote       string
	Notes             string
}

// VMDetails changes details; nil fields stay unchanged, an empty string clears
// an optional text.
type VMDetails struct {
	Name              *string
	VCPU              *int
	MemoryMB          *int
	ManagementAddress *string
	NetworkNote       *string
	Notes             *string
}

// VMFilter selects Virtual Machines.
type VMFilter struct {
	State             string
	HypervisorAssetID string
	Query             string
	Page              Page
}

// AssetLocation answers "where is this Asset?": its active placement with the
// rack, room, building and site above it.
type AssetLocation struct {
	PlacementID    string
	RackID         string
	RackName       string
	RoomID         string
	RoomName       string
	Floor          *string
	BuildingID     string
	BuildingName   string
	SiteLocationID string
	UPosition      int
	HeightU        int
	Face           string
	PlacedAt       time.Time
}

// SiteSummary is one Site (Organization Location) in the infrastructure tree.
type SiteSummary struct {
	LocationID string
	Name       string
	Buildings  []BuildingSummary
}

// BuildingSummary counts what is below a Building.
type BuildingSummary struct {
	ID      string
	Name    string
	Active  bool
	Rooms   int
	Racks   int
	Placed  int
	SiteID  string
	Version int
}

// TreeResult is the infrastructure tree; Truncated reports that more Sites or
// Buildings exist than the bounded tree returns.
type TreeResult struct {
	Sites     []SiteSummary
	Truncated bool
}

// PlacementRef is an active placement with the name of its Rack.
type PlacementRef struct {
	Placement Placement
	RackName  string
}

// PlacementWarning is an active placement whose Asset can no longer be in a
// rack (disposed, lost, retired) or no longer exists. AssetStatus is the
// Asset status, or "missing".
type PlacementWarning struct {
	Placement   Placement
	RackName    string
	AssetRef    string
	AssetStatus string
}

// WarningsResult is the bounded list of placement warnings. Truncated reports
// that the scan or the list limit stopped before all placements were checked.
type WarningsResult struct {
	Items     []PlacementWarning
	Truncated bool
}

// Principal is the caller's authority: View (infrastructure.view) reads,
// Manage (infrastructure.manage) also changes. AssetsView (assets.view or
// assets.manage) is needed for the Asset "where is it" lookup.
type Principal struct {
	UserID     string
	View       bool
	Manage     bool
	AssetsView bool
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
		return errors.New("infrastructure: correlation id is required")
	}
	return nil
}

var (
	ErrNotFound         = errors.New("infrastructure: not found")
	ErrForbidden        = errors.New("infrastructure: forbidden")
	ErrInvalidCursor    = errors.New("infrastructure: invalid cursor")
	ErrVersionConflict  = errors.New("infrastructure: version conflict")
	ErrConflict         = errors.New("infrastructure: conflict")
	ErrReferenceInvalid = errors.New("infrastructure: referenced record does not exist or is not usable")
	ErrArchived         = errors.New("infrastructure: the record or its parent is archived")
	ErrNotEmpty         = errors.New("infrastructure: the record still has active children or placements")
	ErrOccupied         = errors.New("infrastructure: the units are already occupied")
	ErrAssetPlaced      = errors.New("infrastructure: the asset already has an active placement")
	ErrAssetUnusable    = errors.New("infrastructure: the asset does not exist or is disposed, lost or retired")
	ErrPlacementClosed  = errors.New("infrastructure: the placement is no longer active")
	ErrDecommissioned   = errors.New("infrastructure: the virtual machine is decommissioned")
)

// InvalidInputError carries a user-safe validation message.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return "infrastructure: invalid input: " + e.Message }

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

// Directory is what Infrastructure asks the Organization module.
type Directory interface {
	ActiveLocations(ctx context.Context, ids []string) (map[string]bool, error)
	LocationNames(ctx context.Context, ids []string) (map[string]string, error)
}

// AssetInfo is what Infrastructure needs to know about an Asset.
type AssetInfo struct {
	ID        string
	Reference string
	Status    string
}

// Usable reports whether the Asset may be placed or host a VM: disposed, lost
// and retired assets are gone for good.
func (a AssetInfo) Usable() bool {
	switch a.Status {
	case "disposed", "lost", "retired":
		return false
	}
	return true
}

// Assets is what Infrastructure asks the Assets module (public contract).
type Assets interface {
	Assets(ctx context.Context, ids []string) (map[string]AssetInfo, error)
}

// Store persists the Infrastructure records. Tx methods run inside InTx.
type Store interface {
	InTx(ctx context.Context, fn func(tx pgx.Tx) error) error

	InsertBuildingTx(ctx context.Context, tx pgx.Tx, siteLocationID, name string, addressNote *string) (Building, error)
	LockBuildingTx(ctx context.Context, tx pgx.Tx, id string) (Building, error)
	UpdateBuildingTx(ctx context.Context, tx pgx.Tx, b Building) (Building, error)
	GetBuilding(ctx context.Context, id string) (Building, error)
	ListBuildings(ctx context.Context, siteLocationID string, includeArchived bool, page Page) (Result[Building], error)
	CountActiveRoomsTx(ctx context.Context, tx pgx.Tx, buildingID string) (int, error)

	InsertRoomTx(ctx context.Context, tx pgx.Tx, buildingID, name string, floor *string) (Room, error)
	LockRoomTx(ctx context.Context, tx pgx.Tx, id string) (Room, error)
	UpdateRoomTx(ctx context.Context, tx pgx.Tx, r Room) (Room, error)
	GetRoom(ctx context.Context, id string) (Room, error)
	ListRooms(ctx context.Context, buildingID string, includeArchived bool, page Page) (Result[Room], error)
	CountActiveRacksTx(ctx context.Context, tx pgx.Tx, roomID string) (int, error)

	InsertRackTx(ctx context.Context, tx pgx.Tx, roomID, name string, heightU int) (Rack, error)
	LockRackTx(ctx context.Context, tx pgx.Tx, id string) (Rack, error)
	UpdateRackTx(ctx context.Context, tx pgx.Tx, r Rack) (Rack, error)
	GetRack(ctx context.Context, id string) (Rack, error)
	ListRacks(ctx context.Context, roomID string, includeArchived bool, page Page) (Result[Rack], error)
	CountActivePlacementsTx(ctx context.Context, tx pgx.Tx, rackID string) (int, error)

	// InsertPlacementTx adds an active placement and occupies its units. The
	// caller holds the Rack's row lock. ErrAssetPlaced and ErrOccupied report
	// the two database guarantees.
	InsertPlacementTx(ctx context.Context, tx pgx.Tx, in PlacementInput, placedBy string, previousID *string) (Placement, error)
	LockPlacementTx(ctx context.Context, tx pgx.Tx, id string) (Placement, error)
	// ClosePlacementTx sets removed_at and frees the units.
	ClosePlacementTx(ctx context.Context, tx pgx.Tx, id, reason, removedBy string) (Placement, error)
	GetPlacement(ctx context.Context, id string) (Placement, error)
	ActivePlacements(ctx context.Context, rackID string) ([]Placement, error)
	// ActivePlacementsAfter pages through all active placements by id.
	ActivePlacementsAfter(ctx context.Context, afterID string, limit int) ([]PlacementRef, error)
	ListPlacements(ctx context.Context, rackID string, includeRemoved bool, page Page) (Result[Placement], error)
	WhereIs(ctx context.Context, assetID string) (AssetLocation, error)

	InsertVMTx(ctx context.Context, tx pgx.Tx, vm VirtualMachine) (VirtualMachine, error)
	LockVMTx(ctx context.Context, tx pgx.Tx, id string) (VirtualMachine, error)
	UpdateVMTx(ctx context.Context, tx pgx.Tx, vm VirtualMachine) (VirtualMachine, error)
	GetVM(ctx context.Context, id string) (VirtualMachine, error)
	ListVMs(ctx context.Context, f VMFilter) (Result[VirtualMachine], error)

	Tree(ctx context.Context, includeArchived bool) (TreeResult, error)
}
