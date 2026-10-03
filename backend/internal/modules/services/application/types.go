// Package application holds the Services use cases: the IT Service record
// (owner, support team, criticality, status) and its dependencies, which are
// platform Relationships (docs/product/f7-infrastructure-change-design.md,
// slice 2). Dependencies and the impact view traverse them with the bounds of
// platform/relationships.
package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

// Record and relationship type names used with platform/relationships.
const (
	NodeService  = "service"
	NodeVM       = "vm"
	NodeAsset    = "asset"
	NodeLocation = "location"

	RelDependsOn = "DEPENDS_ON"
	RelRunsOn    = "RUNS_ON"
)

// Triples are the relationships this module registers: a Service depends on
// another Service, a Virtual Machine, an Asset or a Location (Site); a Virtual
// Machine runs on an Asset (its hypervisor).
var Triples = []relationships.Triple{
	{SourceType: NodeService, Type: RelDependsOn, TargetType: NodeService},
	{SourceType: NodeService, Type: RelDependsOn, TargetType: NodeVM},
	{SourceType: NodeService, Type: RelDependsOn, TargetType: NodeAsset},
	{SourceType: NodeService, Type: RelDependsOn, TargetType: NodeLocation},
	{SourceType: NodeVM, Type: RelRunsOn, TargetType: NodeAsset},
}

// DependencyTargets are the record types a Service may depend on.
var DependencyTargets = []string{NodeService, NodeVM, NodeAsset, NodeLocation}

// Criticality levels and Service statuses.
const (
	CriticalityLow      = "low"
	CriticalityMedium   = "medium"
	CriticalityHigh     = "high"
	CriticalityCritical = "critical"

	StatusOperational = "operational"
	StatusDegraded    = "degraded"
	StatusOutage      = "outage"
	StatusPlanned     = "planned"
	StatusRetired     = "retired"
)

var (
	Criticalities = []string{CriticalityLow, CriticalityMedium, CriticalityHigh, CriticalityCritical}
	Statuses      = []string{StatusOperational, StatusDegraded, StatusOutage, StatusPlanned, StatusRetired}
	// SettableStatuses are the statuses ChangeStatus accepts; retiring has its own operation.
	SettableStatuses = []string{StatusOperational, StatusDegraded, StatusOutage, StatusPlanned}
	// StatusReasons are the reason codes ChangeStatus accepts.
	StatusReasons = []string{"incident", "maintenance", "recovered", "rollout", "correction", "other"}
	// RetireReasons are the reason codes Retire accepts.
	RetireReasons = []string{"replaced", "decommissioned", "merged", "error_correction", "other"}
	// DependencyRemovalReasons are the reason codes RemoveDependency accepts.
	DependencyRemovalReasons = []string{"no_longer_needed", "replaced", "error_correction", "other"}
)

// Reason codes written by the module itself.
const (
	reasonServiceRetired = "service_retired"
	reasonVMLinkChanged  = "hypervisor_changed"
	reasonVMLinkCleared  = "hypervisor_cleared"
	reasonVMDecommission = "vm_decommissioned"
)

const (
	DefaultLimit = 50
	MaxLimit     = 200
	maxName      = 100
	maxDesc      = 2000
	// MaxDependencyList bounds the dependencies and dependents shown per Service.
	MaxDependencyList = 200
	// MaxLookupIDs bounds the ids of one lookup through another module's contract.
	MaxLookupIDs = 500
)

// Service is an IT Service.
type Service struct {
	ID            string
	Reference     string
	Name          string
	Description   *string
	OwnerUserID   *string
	OwnerTeamID   *string
	SupportTeamID *string
	Criticality   string
	Status        string
	StatusReason  *string
	RetiredAt     *time.Time
	Version       int
	CreatedBy     *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Input creates a Service.
type Input struct {
	Name          string
	Description   string
	OwnerUserID   string
	OwnerTeamID   string
	SupportTeamID string
	Criticality   string
	// Status defaults to operational; planned is allowed for a Service not yet live.
	Status string
}

// Details changes a Service; nil fields stay unchanged, an empty string clears
// the description and the owner and team references.
type Details struct {
	Name          *string
	Description   *string
	OwnerUserID   *string
	OwnerTeamID   *string
	SupportTeamID *string
	Criticality   *string
}

// Filter selects Services. Retired Services are listed only for Status
// "retired" or IncludeRetired. TeamID matches the owner or support team.
type Filter struct {
	Status         string
	Criticality    string
	OwnerUserID    string
	TeamID         string
	Query          string
	IncludeRetired bool
	Page           Page
}

// NodeInfo describes a record on a dependency or impact path. Fields the
// caller may not see stay nil: names of Virtual Machines and Locations need
// infrastructure.view, Asset references and statuses need assets.view.
// Missing means the lookup ran and found no such record.
type NodeInfo struct {
	Type        string
	ID          string
	Name        *string
	Reference   *string
	Status      *string
	Criticality *string
	Missing     bool
}

// Link is one dependency edge with the record on its other end.
type Link struct {
	RelationshipID string
	Type           string
	Confidence     string
	Since          time.Time
	Node           NodeInfo
}

// Detail is a Service with its dependencies and dependents (at most
// MaxDependencyList each; the flags say when more exist).
type Detail struct {
	Service            Service
	Dependencies       []Link
	Dependents         []Link
	DependenciesCutOff bool
	DependentsCutOff   bool
}

// PathEdge is one step on the path from the start of an impact walk.
type PathEdge struct {
	RelationshipID string
	FromType       string
	FromID         string
	ToType         string
	ToID           string
	Type           string
	Confidence     string
}

// ImpactNode is a record reached by an impact walk.
type ImpactNode struct {
	NodeInfo
	Depth int
	// Confidence is that of the last edge on the path.
	Confidence string
	Path       []PathEdge
}

// Directions of an impact walk. Downstream finds what is affected when the
// start record is down (its dependents); upstream finds what the start record
// depends on.
const (
	Downstream = "downstream"
	Upstream   = "upstream"
)

// ImpactInput starts an impact walk. Depth 0 means the maximum (6).
type ImpactInput struct {
	Type      string
	ID        string
	Direction string
	Depth     int
}

// ImpactResult is a bounded impact walk. Truncated is true when DepthLimited
// (more nodes exist beyond the depth) or NodeLimited (the 500 node cap, or the
// edge scan limit, cut the result).
type ImpactResult struct {
	Start        NodeInfo
	Direction    string
	MaxDepth     int
	Nodes        []ImpactNode
	Truncated    bool
	DepthLimited bool
	NodeLimited  bool
}

// Principal is the caller's authority. View/Manage are services.view and
// services.manage. InfraView (infrastructure.view|manage) shows names of
// Virtual Machines and Locations, AssetsView (assets.view|manage) Asset
// references.
type Principal struct {
	UserID     string
	View       bool
	Manage     bool
	InfraView  bool
	AssetsView bool
}

func (p Principal) canView() bool { return p.View || p.Manage }

func (p Principal) require(manage bool) error {
	if manage && !p.Manage || !manage && !p.canView() {
		return ErrForbidden
	}
	return nil
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
		return errors.New("services: correlation id is required")
	}
	return nil
}

var (
	ErrNotFound         = errors.New("services: not found")
	ErrForbidden        = errors.New("services: forbidden")
	ErrInvalidCursor    = errors.New("services: invalid cursor")
	ErrVersionConflict  = errors.New("services: version conflict")
	ErrConflict         = errors.New("services: a service with this name already exists")
	ErrRetired          = errors.New("services: the service is retired")
	ErrReferenceInvalid = errors.New("services: referenced record does not exist or is not usable")
	ErrDependencyCycle  = errors.New("services: the dependency would create a cycle")
	ErrCycleCheck       = errors.New("services: the dependency graph is too large to check for cycles")
)

// InvalidInputError carries a user-safe validation message.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return "services: invalid input: " + e.Message }

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

// Result is one page of Services; NextCursor is empty on the last page.
type Result[T any] struct {
	Items      []T
	NextCursor string
}

// Directory is what Services asks the Organization module.
type Directory interface {
	ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error)
	ActiveTeams(ctx context.Context, ids []string) (map[string]bool, error)
	ActiveLocations(ctx context.Context, ids []string) (map[string]bool, error)
	LocationNames(ctx context.Context, ids []string) (map[string]string, error)
}

// VMInfo is what Services needs to know about a Virtual Machine.
type VMInfo struct {
	ID                string
	Name              string
	State             string
	HypervisorAssetID *string
}

// Decommissioned reports that the VM is a tombstone.
func (v VMInfo) Decommissioned() bool { return v.State == "decommissioned" }

// Infrastructure is what Services asks the Infrastructure module (public contract).
type Infrastructure interface {
	VMs(ctx context.Context, ids []string) (map[string]VMInfo, error)
}

// AssetInfo is what Services needs to know about an Asset.
type AssetInfo struct {
	ID        string
	Reference string
	Status    string
}

// Usable reports whether a Service may depend on the Asset: disposed, lost and
// retired assets are gone for good.
func (a AssetInfo) Usable() bool {
	switch a.Status {
	case "disposed", "lost", "retired":
		return false
	}
	return true
}

// Assets is what Services asks the Assets module (public contract).
type Assets interface {
	Assets(ctx context.Context, ids []string) (map[string]AssetInfo, error)
}

// Store persists Services. Tx methods run inside InTx.
type Store interface {
	InTx(ctx context.Context, fn func(tx pgx.Tx) error) error
	// Q reads relationships outside a transaction.
	Q() relationships.Querier

	InsertTx(ctx context.Context, tx pgx.Tx, s Service) (Service, error)
	LockTx(ctx context.Context, tx pgx.Tx, id string) (Service, error)
	UpdateTx(ctx context.Context, tx pgx.Tx, s Service) (Service, error)
	// LockDependencyGraphTx serializes service-to-service dependency changes
	// until the transaction ends, so two concurrent additions cannot close a cycle.
	LockDependencyGraphTx(ctx context.Context, tx pgx.Tx) error
	// LockVMLinkTx serializes the relationship sync of one Virtual Machine.
	LockVMLinkTx(ctx context.Context, tx pgx.Tx, vmID string) error
	Get(ctx context.Context, id string) (Service, error)
	ByIDs(ctx context.Context, ids []string) ([]Service, error)
	List(ctx context.Context, f Filter) (Result[Service], error)
}
