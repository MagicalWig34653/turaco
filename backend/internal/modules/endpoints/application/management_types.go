package application

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
)

// Normalized value sets of the management model (docs/integrations/intune-assignment-intelligence.md).
var (
	ArtifactKinds      = []string{"application", "configuration_profile", "compliance_policy", "endpoint_security_policy", "script", "remediation"}
	TargetKinds        = []string{"group", "all_devices", "all_users"}
	AssignmentModes    = []string{"include", "exclude"}
	AssignmentIntents  = []string{"required", "available", "uninstall", "none"}
	FilterModes        = []string{"include", "exclude", "none"}
	ObservationStates  = []string{"applied", "pending", "failed", "conflict", "not_applicable", "unknown"}
	providerErrorState = map[string]bool{"failed": true, "conflict": true}
)

// FindingProviderReportedError is raised for a Device on which the provider itself reports a failed or
// conflicting artifact. It is the provider's statement and stays distinct from the data-quality kinds
// Turaco derives.
const FindingProviderReportedError = "provider_reported_error"

// FindingAssignmentIneffective is Turaco's own finding: an include assignment covers the Device, the evaluator
// expects the artifact to apply (confidence high or medium) and yet the provider shows no observation, or
// not_applicable, for longer than IneffectiveAfter. It is derived from local data after each management run
// and stays distinct from FindingProviderReportedError (the provider's own failed/conflict statement).
const FindingAssignmentIneffective = "assignment_ineffective"

const (
	// MaxSnapshotArtifacts, MaxSnapshotFilters, MaxSnapshotObservations and MaxSnapshotMemberships bound one
	// management snapshot; a larger one is refused.
	MaxSnapshotArtifacts    = 20000
	MaxSnapshotFilters      = 20000
	MaxSnapshotObservations = 1000000
	MaxSnapshotMemberships  = 1000000
	// MaxAssignmentsPerArtifact bounds the assignments of one artifact; an artifact above it keeps its
	// previous assignments.
	MaxAssignmentsPerArtifact = 1000
	// MaxFilterRuleLength bounds a filter rule in characters; a longer rule is never truncated (that would
	// change its meaning), the filter is rejected instead.
	MaxFilterRuleLength = 2000
	// ObservationBatchSize and MembershipBatchSize are the rows written per transaction.
	ObservationBatchSize = 500
	MembershipBatchSize  = 1000
	// ReconcileBatchSize is the devices whose provider-reported-error finding is reconciled per transaction.
	ReconcileBatchSize = 500
	// StaleChunkSize is the rows of stale memberships or observations closed or retired per transaction.
	StaleChunkSize = 5000
	// DefaultSyncCooldown is the minimum time between the end of one manual provider synchronization and
	// the start of the next.
	DefaultSyncCooldown = 30 * time.Second
	// MaxClosedAssignments bounds the closed assignment rows an artifact read returns (most recent first).
	MaxClosedAssignments = 200
	// PlaceholderArtifactName replaces an artifact or filter name that is empty or unsafe.
	PlaceholderArtifactName = "unnamed-object"
)

// ManagementSnapshot is a view of a provider's management data. Only a Complete one may tombstone
// artifacts and filters or close memberships missing from it (and then only when the corresponding list
// is not empty, which is treated as a provider failure, and the tombstone guard allows it).
type ManagementSnapshot struct {
	Provider string
	// Source is SourceSync (live provider) or SourceImport (file import).
	Source   string
	Complete bool
	intune.ManagementSnapshot
}

// ManagementResult reports what a management ingestion run did.
type ManagementResult struct {
	FiltersCreated    int
	FiltersUpdated    int
	FiltersUnchanged  int
	FiltersTombstoned int
	FiltersRejected   int

	ArtifactsCreated    int
	ArtifactsUpdated    int
	ArtifactsUnchanged  int
	ArtifactsTombstoned int
	ArtifactsRejected   int
	// ArtifactsLinked counts applications linked to a Software Product by alias match.
	ArtifactsLinked int

	AssignmentsOpened    int
	AssignmentsClosed    int
	AssignmentsUnchanged int
	AssignmentsRejected  int
	// AssignmentEvents counts the ManagementAssignmentChanged events published (one per artifact).
	AssignmentEvents int

	ObservationsCreated   int
	ObservationsChanged   int
	ObservationsUnchanged int
	// ObservationsSkipped counts observations dropped as invalid, duplicate or for an unknown or removed device or artifact.
	ObservationsSkipped int

	MembershipsOpened    int
	MembershipsClosed    int
	MembershipsUnchanged int
	MembershipsSkipped   int

	// ManagementTombstonesSkipped counts artifacts, filters and memberships that were missing from the
	// snapshot but kept because the tombstone guard refused to remove more than half.
	ManagementTombstonesSkipped int

	// ObservationsRetired counts observations a complete snapshot no longer reported.
	ObservationsRetired int

	ProviderFindingsRaised   int
	ProviderFindingsResolved int
	// IneffectiveFindingsRaised and IneffectiveFindingsResolved count the Turaco-derived assignment_ineffective
	// findings the reconcile step after the run raised and resolved; IneffectiveDevicesSkipped counts live Devices
	// beyond MaxReconcileDevices that this run did not evaluate.
	IneffectiveFindingsRaised   int
	IneffectiveFindingsResolved int
	IneffectiveDevicesSkipped   int
	// ManagementErrors counts reads or ingestions of the management data that failed (Sync only).
	ManagementErrors int
}

// Filter is a normalized provider assignment filter.
type Filter struct {
	ID                string
	Provider          string
	ExternalID        string
	Name              string
	Platform          string
	Rule              string
	Revision          *string
	Source            string
	ObservedAt        time.Time
	LastSyncedAt      time.Time
	DeletedObservedAt *time.Time
	Version           int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Artifact is a provider-managed object that can be assigned.
type Artifact struct {
	ID                string
	Provider          string
	ExternalID        string
	Kind              string
	Name              string
	Platform          string
	SoftwareProductID *string
	Revision          *string
	Source            string
	ObservedAt        time.Time
	LastSyncedAt      time.Time
	DeletedObservedAt *time.Time
	Version           int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// FilterSummary is the part of a filter shown beside an assignment.
type FilterSummary struct {
	ID       string
	Name     string
	Platform string
	Rule     string
	Deleted  bool
}

// Assignment is one row of an assignment's interval history; it is current while ValidUntil is nil.
type Assignment struct {
	ID                    string
	ArtifactID            string
	ProviderAssignmentID  string
	TargetKind            string
	TargetGroupExternalID *string
	Mode                  string
	Intent                string
	FilterID              *string
	FilterMode            string
	Source                string
	ObservedAt            time.Time
	LastSyncedAt          time.Time
	ValidFrom             time.Time
	ValidUntil            *time.Time
	Filter                *FilterSummary
}

// Current reports whether the row is the assignment's current state.
func (a Assignment) Current() bool { return a.ValidUntil == nil }

// ArtifactDetail is an artifact with its assignments (current and closed) and the number of live
// devices per observation state.
type ArtifactDetail struct {
	Artifact          Artifact
	Assignments       []Assignment
	ObservationCounts map[string]int
}

// Observation is the provider's result for an artifact on a device.
type Observation struct {
	ID              string
	ArtifactID      string
	ArtifactName    string
	ArtifactKind    string
	ArtifactDeleted bool
	DeviceID        string
	NormalizedState string
	RawStatus       string
	Source          string
	ObservedAt      time.Time
	LastSyncedAt    time.Time
}

// ArtifactFilter selects artifacts; all fields are optional.
type ArtifactFilter struct {
	Kind     string
	Platform string
	// Query matches the name by prefix.
	Query          string
	IncludeDeleted bool
	Page           Page
}

// FilterListFilter selects management filters.
type FilterListFilter struct {
	Platform       string
	Query          string
	IncludeDeleted bool
	Page           Page
}

type ArtifactResult struct {
	Items      []Artifact
	NextCursor string
}

type ManagementFilterResult struct {
	Items      []Filter
	NextCursor string
}

type ObservationResult struct {
	Items      []Observation
	NextCursor string
}

// NewFilter and NewArtifact are the inputs of the insert methods.
type NewFilter struct {
	Provider, ExternalID, Name, Platform, Rule, Source string
	Revision                                           *string
	ObservedAt, SyncedAt                               time.Time
}

type NewArtifact struct {
	Provider, ExternalID, Kind, Name, Platform, Source string
	SoftwareProductID, Revision                        *string
	ObservedAt, SyncedAt                               time.Time
}

// AssignmentInput is a validated, normalized assignment of a snapshot. FilterID is already resolved.
type AssignmentInput struct {
	ProviderAssignmentID  string
	TargetKind            string
	TargetGroupExternalID *string
	Mode                  string
	Intent                string
	FilterID              *string
	FilterMode            string
}

// same reports whether the meaningful fields are equal.
func (a AssignmentInput) same(c Assignment) bool {
	return a.TargetKind == c.TargetKind && sameStr(a.TargetGroupExternalID, c.TargetGroupExternalID) && a.Mode == c.Mode &&
		a.Intent == c.Intent && sameStr(a.FilterID, c.FilterID) && a.FilterMode == c.FilterMode
}

// ObjectRef identifies a stored artifact or device by id; Deleted is set for a tombstone.
type ObjectRef struct {
	ID      string
	Deleted bool
}

// ObservationInput is a validated observation with resolved ids.
type ObservationInput struct {
	ArtifactID, DeviceID, State, RawStatus string
	ObservedAt                             time.Time
}

// ObservationOutcome is what an upsert did to one observation row.
type ObservationOutcome struct {
	ObservationInput
	Created bool
	// Changed is set when the row is new or its normalized state changed to a newer observation. A newer
	// raw status alone is stored on the current row but is not a change (and has no history row); an
	// observation older than the stored one changes neither state nor raw status.
	Changed bool
}

// ProviderErrors is a device's count of live failed and conflicting observations and whether it has an
// open provider_reported_error finding.
type ProviderErrors struct {
	Failed, Conflict int
	FindingOpen      bool
}

// MembershipInput is a validated device group membership with a resolved device id.
type MembershipInput struct{ DeviceID, GroupExternalID string }

// ManagementStore is the persistence port of the management model; it is part of Store. Mutating
// methods run in the caller's transaction so state, audit and events commit together.
type ManagementStore interface {
	LockFilterByExternalTx(ctx context.Context, tx pgx.Tx, provider, externalID string) (*Filter, error)
	InsertFilterTx(ctx context.Context, tx pgx.Tx, n NewFilter) (Filter, bool, error)
	UpdateFilterTx(ctx context.Context, tx pgx.Tx, f Filter) (Filter, error)
	TouchFilterTx(ctx context.Context, tx pgx.Tx, id string, observedAt, syncedAt time.Time, source string) error
	// ResolveFiltersTx returns provider external id -> filter id of the live filters that were seen at or
	// after since (the run's time): a filter rejected or not reported by this run is not returned.
	ResolveFiltersTx(ctx context.Context, tx pgx.Tx, provider string, externalIDs []string, since time.Time) (map[string]string, error)
	FilterTombstoneCandidatesTx(ctx context.Context, tx pgx.Tx, provider string, before time.Time, keep []string) (ids []string, live int, err error)
	TombstoneFiltersTx(ctx context.Context, tx pgx.Tx, ids []string, at time.Time) (int, error)

	LockArtifactByExternalTx(ctx context.Context, tx pgx.Tx, provider, externalID string) (*Artifact, error)
	InsertArtifactTx(ctx context.Context, tx pgx.Tx, n NewArtifact) (Artifact, bool, error)
	UpdateArtifactTx(ctx context.Context, tx pgx.Tx, a Artifact) (Artifact, error)
	TouchArtifactTx(ctx context.Context, tx pgx.Tx, id string, observedAt, syncedAt time.Time, source string) error
	ArtifactTombstoneCandidatesTx(ctx context.Context, tx pgx.Tx, provider string, before time.Time, keep []string) (ids []string, live int, err error)
	// TombstoneArtifactsTx tombstones the artifacts and closes their current assignments at at. It returns the
	// number of assignments closed per artifact id (only artifacts that had some) and the ids of the live devices
	// that have an observation of these artifacts.
	TombstoneArtifactsTx(ctx context.Context, tx pgx.Tx, ids []string, at time.Time) (closed map[string]int, devices []string, err error)

	// CurrentAssignmentsTx returns and locks the artifact's current assignment rows.
	CurrentAssignmentsTx(ctx context.Context, tx pgx.Tx, artifactID string) ([]Assignment, error)
	InsertAssignmentTx(ctx context.Context, tx pgx.Tx, artifactID string, a AssignmentInput, source string, validFrom, observedAt time.Time) error
	CloseAssignmentsTx(ctx context.Context, tx pgx.Tx, ids []string, at time.Time) error
	TouchAssignmentsTx(ctx context.Context, tx pgx.Tx, ids []string, observedAt, syncedAt time.Time, source string) error

	// ResolveArtifactsTx and ResolveDevicesTx return provider external id -> stored object.
	ResolveArtifactsTx(ctx context.Context, tx pgx.Tx, provider string, externalIDs []string) (map[string]ObjectRef, error)
	ResolveDevicesTx(ctx context.Context, tx pgx.Tx, provider string, externalIDs []string) (map[string]ObjectRef, error)
	// UpsertObservationsTx stores the observations (one row per artifact and device, no duplicates in in).
	UpsertObservationsTx(ctx context.Context, tx pgx.Tx, in []ObservationInput, source string, syncedAt time.Time) ([]ObservationOutcome, error)
	AppendObservationHistoryTx(ctx context.Context, tx pgx.Tx, rows []ObservationInput, source string) error
	// StaleObservationsTx counts the provider's active (not retired) observations not seen since before and all active ones.
	StaleObservationsTx(ctx context.Context, tx pgx.Tx, provider string, before time.Time) (stale, live int, err error)
	// RetireStaleObservationsTx retires up to limit of them and returns the ids of the live devices they belong to.
	RetireStaleObservationsTx(ctx context.Context, tx pgx.Tx, provider string, before, at time.Time, limit int) (retired int, devices []string, err error)
	// ProviderErrorsTx returns, per device, the live failed/conflict observation counts (live artifacts, active observations only).
	ProviderErrorsTx(ctx context.Context, tx pgx.Tx, deviceIDs []string) (map[string]ProviderErrors, error)

	// UpsertMembershipsTx opens the memberships that are not current and refreshes the ones that are.
	UpsertMembershipsTx(ctx context.Context, tx pgx.Tx, provider, source string, in []MembershipInput, at time.Time) (opened int, err error)
	// StaleMembershipsTx counts the provider's current memberships not seen since before and all current ones.
	StaleMembershipsTx(ctx context.Context, tx pgx.Tx, provider string, before time.Time) (stale, live int, err error)
	// CloseStaleMembershipsTx closes at most limit of them and returns how many.
	CloseStaleMembershipsTx(ctx context.Context, tx pgx.Tx, provider string, before, at time.Time, limit int) (int, error)

	ListArtifacts(ctx context.Context, f ArtifactFilter) (ArtifactResult, error)
	GetArtifact(ctx context.Context, id string) (Artifact, error)
	ArtifactAssignments(ctx context.Context, artifactID string) ([]Assignment, error)
	ArtifactObservationCounts(ctx context.Context, artifactID string) (map[string]int, error)
	ListManagementFilters(ctx context.Context, f FilterListFilter) (ManagementFilterResult, error)
	ListDeviceObservations(ctx context.Context, deviceID string, page Page) (ObservationResult, error)
}
