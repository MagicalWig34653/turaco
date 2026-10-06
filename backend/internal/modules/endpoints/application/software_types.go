package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Software Approval Status of a Software Product (F9 G1, docs/domain/state-machines.md).
const (
	ProductCandidate  = "candidate"
	ProductApproved   = "approved"
	ProductDeprecated = "deprecated"
	ProductRetired    = "retired"
	ProductBlocked    = "blocked"
)

// ProductStatuses lists every Software Approval Status.
var ProductStatuses = []string{ProductCandidate, ProductApproved, ProductDeprecated, ProductRetired, ProductBlocked}

// Version approval statuses. registered is a version nobody asked to approve yet.
const (
	VersionRegistered = "registered"
	VersionPending    = "pending"
	VersionApproved   = "approved"
	VersionRejected   = "rejected"
	VersionRevoked    = "revoked"
)

// VersionStatuses lists every version approval status.
var VersionStatuses = []string{VersionRegistered, VersionPending, VersionApproved, VersionRejected, VersionRevoked}

// Software Package statuses: requested is Turaco's request before the provider answered; the others are the
// provider's report.
const (
	PackageRequested = "requested"
	PackageBuilding  = "building"
	PackagePackaged  = "packaged"
	PackagePublished = "published"
	PackageFailed    = "failed"
)

// PackageStatuses lists every package status.
var PackageStatuses = []string{PackageRequested, PackageBuilding, PackagePackaged, PackagePublished, PackageFailed}

// FindingPackageHashMismatch is the Endpoint Finding about a Software Package whose provider-reported installer
// hash differs from the approved hash. It has a package, not a Device, as subject and is therefore not one of
// the Device FindingKinds; it is shown with the package.
// Its detail names the reason: hash_differs (the reported installer hash) or binding_differs (a reported
// product key, version, publisher, install command hash or detection rule hash).
const FindingPackageHashMismatch = "package_hash_mismatch"

// FindingPackagePublishedAfterRevoke is the Endpoint Finding about a Software Package whose publication Turaco
// does not accept: the provider reports it published although the version or product approval was withdrawn,
// nobody asked Turaco to publish it or its Management Artifact belongs to another package; or the approval was
// withdrawn while a publication was in flight. Its detail carries the reason code. Resolved by people only.
const FindingPackagePublishedAfterRevoke = "package_published_after_revoke"

// Reason codes of package_published_after_revoke.
const (
	PublishRejectVersionNotApproved = "version_not_approved"
	PublishRejectProductNotApproved = "product_not_approved"
	PublishRejectWithoutRequest     = "published_without_request"
	PublishRejectArtifactTaken      = "artifact_already_linked"
	PublishRejectInFlight           = "publish_in_flight"
)

// Reason codes of the software decisions. Codes, never free text, so audit carries no user-typed content.
var (
	ProductDeprecateReasons = []string{"superseded", "vendor_end_of_support", "policy", "other"}
	ProductRetireReasons    = []string{"no_longer_used", "superseded", "vendor_end_of_life", "other"}
	ProductBlockReasons     = []string{"security_risk", "license", "vendor_unsupported", "policy", "duplicate", "other"}
	ProductUnblockReasons   = []string{"re_evaluation", "error_correction", "other"}
	VersionRejectReasons    = []string{"hash_unverified", "untrusted_source", "license", "security_risk", "policy", "other"}
	VersionRevokeReasons    = []string{"security_risk", "defect", "superseded", "policy", "other"}
)

// Software events.
const (
	EventSoftwareVersionApprovalRequested       = "SoftwareVersionApprovalRequested"
	EventSoftwareVersionApprovalRequestedFanOut = "SoftwareVersionApprovalRequestedFanOut"
	EventSoftwareVersionApproved                = "SoftwareVersionApproved"
	EventSoftwareVersionRevoked                 = "SoftwareVersionRevoked"
	EventSoftwarePackagePublished               = "SoftwarePackagePublished"
)

// Software permissions.
const (
	PermSoftwareView    = "software.view"
	PermSoftwareApprove = "software.approve"
	PermSoftwarePackage = "software.package"
)

const (
	// SoftwarePackageSyncJobType reads package status from the Software Management Provider.
	SoftwarePackageSyncJobType    = "endpoints.software_package_sync"
	SoftwarePackageSyncJobTimeout = 5 * time.Minute
	SoftwarePackageSyncInterval   = 15 * time.Minute
	// softwareSyncBatch is the number of packages asked about per provider call.
	softwareSyncBatch = 100
	// MaxCatalogResults bounds a catalog search answer.
	MaxCatalogResults = 50
	// DefaultCatalogSearchLimit catalog searches per user and DefaultCatalogSearchWindow bound the load one user
	// puts on the Software Management Provider (per API process).
	DefaultCatalogSearchLimit  = 10
	DefaultCatalogSearchWindow = time.Minute
	// softwareSyncStateKey is the provider_sync_state key of the package synchronization cooldown.
	softwareSyncStateKey = "software-package-sync"
)

var (
	// ErrSeparationOfDuties means the caller registered or requested the approval of this version and may not
	// approve it.
	ErrSeparationOfDuties = errors.New("endpoints: the person who registered or requested a version cannot approve it")
	// ErrRateLimited means the caller searched the software catalog too often in the current window.
	ErrRateLimited = errors.New("endpoints: too many catalog searches; try again shortly")
)

// GateError means an operation is refused because a precondition on approval or hash does not hold. Code is
// one of product_not_approved, product_blocked, version_not_approved, package_not_ready, hash_mismatch.
type GateError struct{ Code string }

func (e *GateError) Error() string { return "endpoints: refused: " + e.Code }

// InvalidTransitionError means the operation is not allowed from the current status.
type InvalidTransitionError struct{ Operation, From string }

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("endpoints: %s is not allowed from %s", e.Operation, e.From)
}

// SoftwareProduct is a normalized Software Product with its Software Approval Status.
type SoftwareProduct struct {
	ID             string
	Name           string
	Publisher      *string
	ApprovalStatus string
	ApprovalReason *string
	Version        int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// SoftwareVersion is one immutable installable binding with its approval state.
type SoftwareVersion struct {
	ID                   string
	ProductID            string
	ProductName          string
	ProductVersion       string
	InstallerSHA256      string
	InstallerURL         string
	Publisher            *string
	InstallCommand       string
	InstallCommandSHA256 string
	DetectionRule        string
	DetectionRuleSHA256  string
	BindingSHA256        string
	// DefinitionRedacted means InstallerURL, InstallCommand and DetectionRule were removed for a reader who
	// holds software.view only (their hashes stay).
	DefinitionRedacted bool
	RegisteredBy       string
	ApprovalStatus     string
	ApprovalReason     *string
	RequestedBy        *string
	RequestedAt        *time.Time
	DecidedBy          *string
	DecidedAt          *time.Time
	Version            int
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// SoftwareTransition is one append-only decision about a product or a version. InstallerSHA256 and
// BindingSHA256 are set for version approvals: the binding the decision was made for.
type SoftwareTransition struct {
	ID              string
	SubjectID       string
	FromStatus      string
	ToStatus        string
	Operation       string
	Reason          *string
	InstallerSHA256 string
	BindingSHA256   string
	ActorUserID     *string
	ActorSystem     *string
	CorrelationID   string
	CreatedAt       time.Time
}

// SoftwarePackage links a Software Version to a package of a Software Management Provider and, once
// published and synchronized, to the Management Artifact it became.
type SoftwarePackage struct {
	ID                           string
	Provider                     string
	ProviderPackageID            *string
	VersionID                    string
	Status                       string
	InstallerSHA256              *string
	ManagementProvider           *string
	ManagementArtifactExternalID *string
	ManagementArtifactID         *string
	Source                       string
	ObservedAt                   *time.Time
	LastSyncedAt                 *time.Time
	RequestedBy                  string
	PublishRequestedBy           *string
	PublishRequestedAt           *time.Time
	// PackageAttempt and PublishAttempt are part of the provider operation keys (see PackageVersion and
	// PublishPackage). PublishedAt is set once, when Turaco accepted the package as published.
	PackageAttempt int
	PublishAttempt int
	PublishedAt    *time.Time
	Version        int
	CreatedAt      time.Time
	UpdatedAt      time.Time
	// Derived on reads: HashMismatch and PublishedAfterRevoke report an open package finding of that kind;
	// VersionRevoked and ProductBlocked report the current approval of the version and its product. They are
	// the gate inputs of G2 assignments (docs/product/f9-software-lifecycle-design.md).
	HashMismatch         bool
	PublishedAfterRevoke bool
	VersionRevoked       bool
	ProductBlocked       bool
}

// publishInFlight reports a publish request without a provider report since.
func (p SoftwarePackage) publishInFlight() bool {
	return p.PublishRequestedAt != nil && p.PublishedAt == nil && (p.ObservedAt == nil || p.ObservedAt.Before(*p.PublishRequestedAt))
}

// PackageObservation is one append-only row of a package's reported state.
type PackageObservation struct {
	PackageID                    string
	Status                       string
	ProviderPackageID            *string
	InstallerSHA256              *string
	ManagementArtifactExternalID *string
	Source                       string
	ObservedAt                   time.Time
}

// NewSoftwareVersion is the input of RegisterVersion.
type NewSoftwareVersion struct {
	ProductID       string
	ProductVersion  string
	InstallerSHA256 string
	InstallerURL    string
	Publisher       string
	InstallCommand  string
	DetectionRule   string
}

// SoftwareVersionDetail is a version with its approval history and packages.
type SoftwareVersionDetail struct {
	Version   SoftwareVersion
	Approvals []SoftwareTransition
	Packages  []SoftwarePackage
}

// CatalogEntry is one validated catalog hit of the Software Management Provider.
type CatalogEntry struct {
	ProviderID    string
	Name          string
	Publisher     *string
	LatestVersion *string
	SourceURL     *string
}

// SoftwarePackageSyncResult counts what one package synchronization did.
type SoftwarePackageSyncResult struct {
	Checked, Changed, Linked, FindingsRaised, FindingsResolved int
	// Stale counts reports older than the stored observation (only freshness refreshed); Errors counts packages
	// whose report could not be applied (the run continues with the next package).
	Stale, Errors int
}

// Software filters; every field is optional.
type SoftwareProductFilter struct {
	Status string
	Page   Page
}

type SoftwareVersionFilter struct {
	ProductID string
	Status    string
	Page      Page
}

type SoftwarePackageFilter struct {
	Status    string
	VersionID string
	Page      Page
}

type SoftwareProductResult struct {
	Items      []SoftwareProduct
	NextCursor string
}

type SoftwareVersionResult struct {
	Items      []SoftwareVersion
	NextCursor string
}

type SoftwarePackageResult struct {
	Items      []SoftwarePackage
	NextCursor string
}

// SoftwareStore persists software approvals and packages. Mutating methods run in the caller's transaction.
type SoftwareStore interface {
	// Lock order everywhere: package -> version -> product. Lock* take FOR NO KEY UPDATE (the row is mutated),
	// Share* take FOR SHARE (a gate read that must not change until commit).
	// LockProductTx returns the product FOR NO KEY UPDATE; ErrNotFound for an unknown id.
	LockProductTx(ctx context.Context, tx pgx.Tx, id string) (SoftwareProduct, error)
	// ShareProductTx returns the product FOR SHARE; ErrNotFound for an unknown id.
	ShareProductTx(ctx context.Context, tx pgx.Tx, id string) (SoftwareProduct, error)
	// UpdateProductApprovalTx stores the approval status and reason with version+1.
	UpdateProductApprovalTx(ctx context.Context, tx pgx.Tx, id, status string, reason *string) (SoftwareProduct, error)
	AppendProductTransitionTx(ctx context.Context, tx pgx.Tx, t SoftwareTransition) error
	// InsertVersionTx creates the version, or returns the existing one with the same binding (created false).
	InsertVersionTx(ctx context.Context, tx pgx.Tx, v SoftwareVersion) (SoftwareVersion, bool, error)
	// LockVersionTx returns the version FOR NO KEY UPDATE; ErrNotFound for an unknown id.
	LockVersionTx(ctx context.Context, tx pgx.Tx, id string) (SoftwareVersion, error)
	// ShareVersionTx returns the version FOR SHARE; ErrNotFound for an unknown id.
	ShareVersionTx(ctx context.Context, tx pgx.Tx, id string) (SoftwareVersion, error)
	// UpdateVersionApprovalTx stores the approval fields with version+1; the binding never changes.
	UpdateVersionApprovalTx(ctx context.Context, tx pgx.Tx, v SoftwareVersion) (SoftwareVersion, error)
	AppendVersionApprovalTx(ctx context.Context, tx pgx.Tx, t SoftwareTransition) error
	GetSoftwareVersion(ctx context.Context, id string) (SoftwareVersion, error)
	VersionApprovals(ctx context.Context, versionID string) ([]SoftwareTransition, error)
	ListSoftwareProducts(ctx context.Context, f SoftwareProductFilter) (SoftwareProductResult, error)
	ListSoftwareVersions(ctx context.Context, f SoftwareVersionFilter) (SoftwareVersionResult, error)
	ListSoftwarePackages(ctx context.Context, f SoftwarePackageFilter) (SoftwarePackageResult, error)

	// InsertPackageTx creates the requested package of a version, or returns the existing one locked (created false).
	InsertPackageTx(ctx context.Context, tx pgx.Tx, provider, versionID, requestedBy string) (SoftwarePackage, bool, error)
	// LockPackageOfVersionTx returns the provider's package of a version FOR NO KEY UPDATE (found false: none).
	LockPackageOfVersionTx(ctx context.Context, tx pgx.Tx, provider, versionID string) (SoftwarePackage, bool, error)
	// LockPackageTx returns the package FOR NO KEY UPDATE; ErrNotFound for an unknown id.
	LockPackageTx(ctx context.Context, tx pgx.Tx, id string) (SoftwarePackage, error)
	// UpdatePackageTx stores every mutable column with version+1.
	UpdatePackageTx(ctx context.Context, tx pgx.Tx, p SoftwarePackage) (SoftwarePackage, error)
	// TouchPackageTx refreshes only the freshness of a package (observed_at, source, last_synced_at; nil keeps
	// the stored value) without changing its version.
	TouchPackageTx(ctx context.Context, tx pgx.Tx, id string, observedAt *time.Time, source string, lastSyncedAt *time.Time) (SoftwarePackage, error)
	// PackageIDByArtifactTx returns the package that holds the Management Artifact external id, or "".
	PackageIDByArtifactTx(ctx context.Context, tx pgx.Tx, managementProvider, externalID string) (string, error)
	// PublishingPackagesTx returns the packages of a version (or of every version of a product, when versionID
	// is empty) with a publish request and no provider report since.
	PublishingPackagesTx(ctx context.Context, tx pgx.Tx, versionID, productID string) ([]string, error)
	AppendPackageObservationTx(ctx context.Context, tx pgx.Tx, o PackageObservation) error
	// SyncablePackages returns the provider's packages that have a provider id, by id after the cursor.
	SyncablePackages(ctx context.Context, provider, after string, limit int) ([]SoftwarePackage, error)
	// RecordProviderReferenceTx records the provider's package id as the package's platform external reference
	// (system = provider, entity type software_package); the id is set once and never changed (a retry after a
	// failed attempt keeps the first attempt's id there; the package row carries the current one).
	RecordProviderReferenceTx(ctx context.Context, tx pgx.Tx, provider, packageID, providerPackageID string) error
	// ArtifactIDByExternalTx returns the live Management Artifact with that external id, or nil.
	ArtifactIDByExternalTx(ctx context.Context, tx pgx.Tx, provider, externalID string) (*string, error)
	// OpenPackageFindingTx raises the package finding or refreshes its detail; it reports whether it was raised.
	OpenPackageFindingTx(ctx context.Context, tx pgx.Tx, kind, packageID string, detail []byte) (string, bool, error)
	// ResolvePackageFindingTx resolves the open package finding, if any, and reports whether one was open.
	ResolvePackageFindingTx(ctx context.Context, tx pgx.Tx, kind, packageID string) (bool, error)
}
