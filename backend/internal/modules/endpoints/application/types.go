// Package application holds the Endpoints use cases (F6 slice 1): provider-observed Devices with
// their observation history, installed software normalized through aliases, the link of a Device to
// its canonical Asset, and data-quality findings (docs/product/f6-endpoint-intelligence-design.md).
//
// Provider data is observed data: it is stored with source and freshness, never overwrites Asset
// data, and a Device missing from a later snapshot is tombstoned, not deleted.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Normalized value sets of Device attributes.
var (
	OSPlatforms      = []string{"windows", "macos", "ios", "android", "linux", "other"}
	Ownerships       = []string{"corporate", "personal", "unknown"}
	ComplianceStates = []string{"compliant", "noncompliant", "in_grace_period", "unknown"}
)

// Finding kinds and statuses.
const (
	FindingNoAssetMatch      = "no_asset_match"
	FindingSerialConflict    = "serial_conflict"
	FindingDuplicateDevice   = "duplicate_device"
	FindingUnmatchedSoftware = "unmatched_software"

	FindingOpen     = "open"
	FindingResolved = "resolved"
)

// ManagementFindingKinds are the findings derived from management data; reading them needs management access.
var ManagementFindingKinds = []string{FindingProviderReportedError, FindingAssignmentIneffective}

// FindingKinds lists every finding kind.
var FindingKinds = []string{FindingNoAssetMatch, FindingSerialConflict, FindingDuplicateDevice, FindingUnmatchedSoftware, FindingProviderReportedError, FindingAssignmentIneffective}

// Asset link sources.
const (
	LinkSerial = "serial"
	LinkManual = "manual"
)

// Observation sources.
const (
	SourceSync   = "sync"
	SourceImport = "import"
)

// ReasonCodes are the accepted reasons of a manual link or unlink. They are codes, not free text,
// so the audit trail never carries user-typed content.
var ReasonCodes = []string{"serial_confirmed", "correction", "duplicate", "wrong_asset", "other"}

// ManagementStateFilters are the observed states the device list can be filtered by: the ones that need attention.
var ManagementStateFilters = []string{"failed", "conflict", "pending"}

const (
	// MaxCheckinDays bounds the lastCheckinOlderThanDays filter.
	MaxCheckinDays = 3650
	DefaultLimit   = 50
	MaxLimit       = 200
	// BatchSize is the number of devices ingested per transaction.
	BatchSize = 100
	// MaxSoftwarePerDevice bounds the installations kept per device.
	MaxSoftwarePerDevice = 5000
	// MaxBatchInstallations bounds the installations written per transaction; a single device that
	// has more than that is ingested alone.
	MaxBatchInstallations = 2000
	// MaxSnapshotDevices bounds one snapshot; a larger one is refused.
	MaxSnapshotDevices = 50000
	// Tombstone guard: when more than MinGuardedDevices devices of a provider are live and a
	// snapshot would tombstone more than half of them, no tombstone is applied (updates still are).
	MinGuardedDevices = 10
)

// Device is a provider-observed endpoint identity.
type Device struct {
	ID              string
	Provider        string
	ExternalID      string
	Name            string
	SerialNumber    *string
	AssetID         *string
	AssetLinkSource *string
	AutoLinkBlocked bool
	OSPlatform      string
	OSVersion       *string
	Manufacturer    *string
	Model           *string
	Ownership       string
	ComplianceState string
	LastCheckinAt   *time.Time
	Source          string
	ObservedAt      time.Time
	LastSyncedAt    time.Time
	// DeletedObservedAt is set when the device vanished from the provider's snapshot.
	DeletedObservedAt *time.Time
	Version           int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Installation is one observed software installation on a device.
type Installation struct {
	ID              string
	DeviceID        string
	SoftwareProduct *string // product id when an alias matched
	ProductName     *string
	RawName         string
	RawVersion      string
	RawPublisher    *string
	ObservedAt      time.Time
	LastSyncedAt    time.Time
}

// Finding is a data-quality observation about a device.
type Finding struct {
	ID         string
	Kind       string
	DeviceID   string
	DeviceName string
	Status     string
	Detail     json.RawMessage
	RaisedAt   time.Time
	ResolvedAt *time.Time
}

// DeviceDetail is a device with its live software and open findings.
type DeviceDetail struct {
	Device   Device
	Software []Installation
	Findings []Finding
}

// Principal is the caller's endpoint authority: View reads, Manage also changes (manual link,
// import, sync). Endpoint data is not visible to anyone without one of them. AssetsView is the
// caller's assets.view permission: a manual link reveals and binds an Asset, so it needs it too.
//
// ManagementView is endpoint.management.view: it reads Management Artifacts, Assignments and Filters.
// A Device's observations need device access as well (canView).
type Principal struct {
	UserID         string
	View           bool
	Manage         bool
	AssetsView     bool
	ManagementView bool
	// DirectoryView is organization.directory.view: it reveals provider group ids in assignments.
	DirectoryView bool
	// SoftwareView, SoftwareApprove and SoftwarePackage are software.view, software.approve and
	// software.package (F9 G1). Approve and package include the software read access.
	SoftwareView    bool
	SoftwareApprove bool
	SoftwarePackage bool
	// DeploymentsView, DeploymentsManage, DeploymentsExecute and DeploymentsHighImpact are the deployments.*
	// permissions (F9 G2). Manage includes the read access.
	DeploymentsView       bool
	DeploymentsManage     bool
	DeploymentsExecute    bool
	DeploymentsHighImpact bool
}

func (p Principal) canViewDeployments() bool {
	return p.DeploymentsView || p.DeploymentsManage || p.DeploymentsExecute
}

func (p Principal) canViewSoftware() bool {
	return p.SoftwareView || p.SoftwareApprove || p.SoftwarePackage
}

func (p Principal) canView() bool { return p.View || p.Manage }

func (p Principal) canViewManagement() bool { return p.ManagementView || p.Manage }

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
		return errors.New("endpoints: correlation id is required")
	}
	return nil
}

var (
	ErrNotFound        = errors.New("endpoints: not found")
	ErrForbidden       = errors.New("endpoints: forbidden")
	ErrInvalidCursor   = errors.New("endpoints: invalid cursor")
	ErrVersionConflict = errors.New("endpoints: version conflict")
	// ErrConflict means the operation contradicts current state (already linked elsewhere, not linked).
	ErrConflict = errors.New("endpoints: conflict")
	// ErrAssetInvalid means the Asset does not exist or is disposed, lost or retired.
	ErrAssetInvalid = errors.New("endpoints: asset does not exist or is not in use")
	// ErrSyncRunning means another ingestion run of the same provider is in progress. It is a conflict.
	ErrSyncRunning = fmt.Errorf("%w: another run for this provider is in progress", ErrConflict)
	// ErrSyncCooldown means the provider's last synchronization finished too recently.
	ErrSyncCooldown = errors.New("endpoints: the provider was synchronized a moment ago")
	// ErrSyncDisabled means the provider synchronization is not enabled.
	ErrSyncDisabled = errors.New("endpoints: provider synchronization is not enabled")
)

// InvalidInputError carries a user-safe validation message.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return "endpoints: invalid input: " + e.Message }

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

// DeviceFilter selects devices; all fields are optional.
type DeviceFilter struct {
	Platform   string
	Compliance string
	// Query matches the name or serial number by prefix.
	Query string
	// Linked restricts to devices with (true) or without (false) an Asset.
	Linked         *bool
	IncludeDeleted bool
	// ManagementState selects Devices with an active provider observation of that state (failed, conflict or
	// pending) on a live artifact. It needs management access.
	ManagementState string
	// HasFinding selects Devices with an open finding of that kind.
	HasFinding string
	// OSVersionPrefix matches the OS version by prefix.
	OSVersionPrefix string
	// LastCheckinOlderThanDays selects Devices whose last check-in is older than that many days (never checked in: not selected).
	LastCheckinOlderThanDays int
	// LastCheckinBefore is the cutoff derived from LastCheckinOlderThanDays by the service.
	LastCheckinBefore *time.Time
	Page              Page
}

// FindingFilter selects findings; Status defaults to open.
type FindingFilter struct {
	Kind string
	// ExcludeKinds leaves out findings of these kinds (set by the service, never by the caller).
	ExcludeKinds []string
	Status       string
	DeviceID     string
	Page         Page
}

// DeviceResult and FindingResult are one page; NextCursor is empty on the last page.
type DeviceResult struct {
	Items      []Device
	NextCursor string
}

type FindingResult struct {
	Items      []Finding
	NextCursor string
}

// InstallationInput is one installation to store; Key is NormalizeSoftwareName(Name).
type InstallationInput struct {
	Name, Version, Key string
	Publisher          *string
	ProductID          *string
}

// NewDevice is the input of Store.InsertDeviceTx.
type NewDevice struct {
	Provider, ExternalID, Name, OSPlatform, Ownership, ComplianceState, Source string
	SerialNumber, OSVersion, Manufacturer, Model                               *string
	LastCheckinAt                                                              *time.Time
	ObservedAt, SyncedAt                                                       time.Time
}

// History is one row of the observation history (values copied from a Device).
type History struct {
	DeviceID                                             string
	Name, OSPlatform, Ownership, ComplianceState, Source string
	SerialNumber, OSVersion, Manufacturer, Model         *string
	ObservedAt                                           time.Time
}

// ProductInfo is a normalized software product.
type ProductInfo struct {
	ID        string
	Name      string
	Publisher *string
}

// AssetInfo is what Endpoints need to know about an Asset.
type AssetInfo struct {
	ID           string
	SerialNumber *string
	// Status is the Asset's lifecycle status.
	Status string
}

// Terminal reports whether the Asset left use (disposed, lost or retired); such an Asset is not
// linked automatically or by hand.
func (a AssetInfo) Terminal() bool {
	return a.Status == "disposed" || a.Status == "lost" || a.Status == "retired"
}

// Assets is the port to the Assets module (adapter over assets/public).
type Assets interface {
	// FindBySerial returns the single asset with this serial number: ErrAssetNotFound when none,
	// ErrAssetAmbiguous when several share it.
	FindBySerial(ctx context.Context, serial string) (AssetInfo, error)
	// ByID returns the asset and whether it exists.
	ByID(ctx context.Context, assetID string) (AssetInfo, bool, error)
}

// Errors an Assets adapter returns from FindBySerial.
var (
	ErrAssetNotFound  = errors.New("endpoints: no asset with this serial number")
	ErrAssetAmbiguous = errors.New("endpoints: several assets share this serial number")
)

// Store is the persistence port. Mutating methods run in the caller's transaction so state, audit
// and events commit together.
type Store interface {
	ManagementStore
	ViewStore
	SoftwareStore
	DeploymentStore
	InTx(ctx context.Context, fn func(tx pgx.Tx) error) error

	// LockDeviceByExternalTx returns the device FOR UPDATE, or nil when unknown.
	LockDeviceByExternalTx(ctx context.Context, tx pgx.Tx, provider, externalID string) (*Device, error)
	// LockDeviceTx returns the device FOR UPDATE; ErrNotFound for an unknown id.
	LockDeviceTx(ctx context.Context, tx pgx.Tx, id string) (Device, error)
	// InsertDeviceTx creates the device; it reports false (and no device) when a concurrent run created it first.
	InsertDeviceTx(ctx context.Context, tx pgx.Tx, n NewDevice) (Device, bool, error)
	// UpdateDeviceTx stores every mutable column with version+1.
	UpdateDeviceTx(ctx context.Context, tx pgx.Tx, d Device) (Device, error)
	// TouchDeviceTx records a sighting without a meaningful change (freshness only). It never moves
	// last_synced_at backwards.
	TouchDeviceTx(ctx context.Context, tx pgx.Tx, id string, observedAt, syncedAt time.Time, lastCheckin *time.Time, source string) error
	AppendHistoryTx(ctx context.Context, tx pgx.Tx, h History) error
	// TryLockProvider takes the per-provider ingestion lock on a dedicated connection. It reports
	// false when another run holds it. unlock must be called when ok.
	TryLockProvider(ctx context.Context, provider string) (unlock func(), ok bool, err error)
	// LastSyncCompleted returns when the provider's last manual synchronization completed (nil if never);
	// MarkSyncCompleted records it.
	LastSyncCompleted(ctx context.Context, provider string) (*time.Time, error)
	MarkSyncCompleted(ctx context.Context, provider string, at time.Time) error
	// TombstoneCandidatesTx locks (in id order) and returns the ids of the provider's live devices that
	// were not seen since before and are not in keepExternalIDs, plus the number of live devices.
	TombstoneCandidatesTx(ctx context.Context, tx pgx.Tx, provider string, before time.Time, keepExternalIDs []string) (ids []string, live int, err error)
	// TombstoneDevicesTx tombstones the devices (locked in id order) together with their installations,
	// drops their serial-source links and returns the updated devices and the links dropped (before state).
	TombstoneDevicesTx(ctx context.Context, tx pgx.Tx, ids []string, at time.Time) (tombstoned []Device, unlinked []Device, err error)
	// LiveDevicesBySerialTx locks and returns the live devices with the serial number (case-insensitive) except excludeID.
	LiveDevicesBySerialTx(ctx context.Context, tx pgx.Tx, serial, excludeID string) ([]Device, error)
	// OtherLiveDevicesByAssetTx returns ids of other live devices linked to the asset.
	OtherLiveDevicesByAssetTx(ctx context.Context, tx pgx.Tx, assetID, excludeID string) ([]string, error)

	// ResolveAliasesTx returns normalized alias -> product id for the given aliases.
	ResolveAliasesTx(ctx context.Context, tx pgx.Tx, aliases []string) (map[string]string, error)
	// UpsertInstallationsTx stores the observed installations in one statement (clearing tombstones).
	UpsertInstallationsTx(ctx context.Context, tx pgx.Tx, deviceID string, items []InstallationInput, observedAt, syncedAt time.Time) error
	// TombstoneInstallationsTx tombstones the device's live installations not seen since before.
	TombstoneInstallationsTx(ctx context.Context, tx pgx.Tx, deviceID string, before, at time.Time) error
	CountUnmatchedTx(ctx context.Context, tx pgx.Tx, deviceID string) (int, error)
	// InsertProductTx creates a product and its aliases; ErrConflict when the product or an alias exists.
	InsertProductTx(ctx context.Context, tx pgx.Tx, name string, publisher *string, aliases []string) (ProductInfo, error)
	// RelinkInstallationsTx matches unmatched live installations against aliases and returns the
	// number matched and the ids of the devices affected.
	RelinkInstallationsTx(ctx context.Context, tx pgx.Tx) (int, []string, error)

	// OpenFindingTx raises the finding, or refreshes the detail of the open one; it reports whether it was raised.
	OpenFindingTx(ctx context.Context, tx pgx.Tx, kind, deviceID string, detail json.RawMessage) (id string, raised bool, err error)
	// ResolveFindingTx resolves the open finding, if any, and reports whether one was open.
	ResolveFindingTx(ctx context.Context, tx pgx.Tx, kind, deviceID string) (bool, error)

	GetDevice(ctx context.Context, id string) (Device, error)
	ListDevices(ctx context.Context, f DeviceFilter) (DeviceResult, error)
	Installations(ctx context.Context, deviceID string) ([]Installation, error)
	OpenFindings(ctx context.Context, deviceID string) ([]Finding, error)
	ListFindings(ctx context.Context, f FindingFilter) (FindingResult, error)
}
