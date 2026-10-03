package application

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application/evaluation"
)

// Limits of the read views (F6 slice 3). The views compute on demand from local normalized data.
const (
	// MaxEvalDevices bounds the Devices evaluated for one request; a larger candidate set is cut and reported as truncated.
	MaxEvalDevices = 500
	// MaxViewScan bounds the artifacts looked at for one page of a filtered device view.
	MaxViewScan = 2000
	// MaxViewExamples bounds the example lists of counts.
	MaxViewExamples = 10
	// MaxGroupViewArtifacts bounds one page of the Directory Group view (every artifact evaluates all candidate devices).
	MaxGroupViewArtifacts = 50
	// MaxUserViewDevices bounds the Devices of one User shown beside each artifact.
	MaxUserViewDevices = 20
	// MaxUserViewAssets bounds the Assets of a User looked at to find those Devices (a User also holds
	// monitors and phones that are not endpoint Devices).
	MaxUserViewAssets = 200
	// StaleObservation is the age after which an observation or input is shown as stale.
	StaleObservation = evaluation.FreshnessLimit
)

// Expected applicability results and the three states are named here so the HTTP layer stays literal-free.
const (
	ExpectedApplicable    = evaluation.Applicable
	ExpectedExcluded      = evaluation.Excluded
	ExpectedNotApplicable = evaluation.NotApplicable
	ExpectedUnknown       = evaluation.UnknownResult
)

// Mismatches between Assigned / Expected Applicable / Observed.
const (
	MismatchAssignedNotObserved = "assigned_not_observed"
	MismatchExpectedNotApplied  = "expected_not_applied"
	MismatchObservedNotExpected = "observed_not_expected"
)

var (
	// ExpectedResults lists the evaluator results.
	ExpectedResults = []string{ExpectedApplicable, ExpectedExcluded, ExpectedNotApplicable, ExpectedUnknown}
	Mismatches      = []string{MismatchAssignedNotObserved, MismatchExpectedNotApplied, MismatchObservedNotExpected}
	// ObservedNone selects Devices/artifacts without any observation in the state filter.
	ObservedNone = "none"
)

// DirectoryGroup is a Directory Group as Endpoints may see it (Organization public contract).
type DirectoryGroup struct {
	ID         string
	ExternalID string
	Name       string
	ObservedAt time.Time
}

// NestingEdge says Child is a direct member group of Parent.
type NestingEdge struct{ ChildID, ParentID string }

// UserMembership is a current membership of a User in a Directory Group.
type UserMembership struct {
	UserID, GroupID string
	ObservedAt      time.Time
}

// Directory answers the Organization questions the management views ask: Directory Groups, nesting, User
// memberships and User display names. It authorizes nothing; the service decides what may be shown. Every lookup
// is limited to the groups and identities of one provider key (the endpoint provider), is bounded, and reports
// truncated=true when rows were cut: the caller must then not treat the answer as complete.
type Directory interface {
	GroupsByExternalIDs(ctx context.Context, providerKey string, externalIDs []string) ([]DirectoryGroup, bool, error)
	GroupsByIDs(ctx context.Context, providerKey string, ids []string) ([]DirectoryGroup, bool, error)
	NestingUp(ctx context.Context, providerKey string, groupIDs []string) ([]NestingEdge, bool, error)
	NestingDown(ctx context.Context, providerKey string, groupIDs []string) ([]NestingEdge, bool, error)
	UserMemberships(ctx context.Context, providerKey string, userIDs []string) ([]UserMembership, bool, error)
	GroupMembers(ctx context.Context, providerKey string, groupIDs []string, limit int) ([]UserMembership, bool, error)
	// UsersWithIdentity tells which Users have synced directory data of the provider at all; for the others
	// memberships are unknown, not empty.
	UsersWithIdentity(ctx context.Context, providerKey string, userIDs []string) (map[string]bool, error)
	// UserNames returns id -> display name for existing Users.
	UserNames(ctx context.Context, ids []string) (map[string]string, error)
}

// AssetHolders answers who holds an Asset (Assets public contract). The holder is personal data.
type AssetHolders interface {
	UserHolders(ctx context.Context, assetIDs []string) (map[string]string, error)
	AssetsHeldByUsers(ctx context.Context, userIDs []string, limit int) (map[string][]string, error)
}

// DeviceMembership is a current membership of a Device in a provider group.
type DeviceMembership struct {
	DeviceID, GroupExternalID string
	LastSyncedAt              time.Time
	// ObservedFrom is when the current membership interval started.
	ObservedFrom time.Time
}

// ReachQuery selects the live artifacts that have a current assignment reaching the given targets, or (with
// DeviceID) an active observation on that Device. Keyset: ascending id after AfterID.
type ReachQuery struct {
	// Provider is the management provider whose assignments are looked at.
	Provider         string
	DeviceID         string
	Kind             string
	GroupExternalIDs []string
	AllDevices       bool
	AllUsers         bool
	// AnyGroup also selects artifacts with any group target (used when the Device's User, and so its groups, is unknown).
	AnyGroup bool
	AfterID  string
	Limit    int
}

// ViewStore is the read port of the management views.
type ViewStore interface {
	// ReadSnapshot runs fn with a ctx under which every ViewStore read (and GetDevice, GetArtifact and
	// ArtifactObservationCounts) shares one READ ONLY REPEATABLE READ transaction, so one view sees one consistent
	// state of the Endpoints tables. Reads of other modules are not part of it (eventually consistent).
	ReadSnapshot(ctx context.Context, fn func(ctx context.Context) error) error
	DevicesByIDs(ctx context.Context, ids []string) ([]Device, error)
	LiveDevicesByAssetIDs(ctx context.Context, assetIDs []string, limit int) ([]Device, error)
	LiveDevicesInGroups(ctx context.Context, provider string, groupExternalIDs []string, limit int) ([]Device, error)
	LiveDevicesFirst(ctx context.Context, limit int) ([]Device, error)
	DeviceMemberships(ctx context.Context, deviceIDs []string) ([]DeviceMembership, error)
	// DevicesWithMembershipHistory returns the Devices that ever had a synced group membership of the provider
	// (current or closed). For the others memberships are unknown, not empty.
	DevicesWithMembershipHistory(ctx context.Context, provider string, deviceIDs []string) ([]string, error)
	ReachableArtifacts(ctx context.Context, q ReachQuery) ([]Artifact, error)
	ArtifactsTargetingGroups(ctx context.Context, provider string, groupExternalIDs []string, afterID string, limit int) ([]Artifact, error)
	// CurrentAssignmentsOf returns the current assignments (with filter summaries) per artifact id.
	CurrentAssignmentsOf(ctx context.Context, artifactIDs []string) (map[string][]Assignment, error)
	// ObservationsOf returns the active observations of the artifacts on the devices.
	ObservationsOf(ctx context.Context, artifactIDs, deviceIDs []string) ([]Observation, error)

	// History reads (F6 slice 4). Every method returns at most limit rows newest first, strictly before the cursor.
	AssignmentEvents(ctx context.Context, q AssignmentEventQuery) (rows []AssignmentEventRow, scopeTruncated bool, err error)
	ObservationEvents(ctx context.Context, deviceID string, after *HistoryCursor, limit int) ([]ObservationEventRow, error)
	MembershipEvents(ctx context.Context, deviceID string, after *HistoryCursor, limit int) ([]MembershipEventRow, error)

	// Finding reconciliation reads (F6 slice 4).
	LiveDevicesPage(ctx context.Context, provider, afterID string, limit int) ([]Device, error)
	CountLiveDevicesAfter(ctx context.Context, provider, afterID string) (int, error)
	// ArtifactsWithObservations returns the ids among artifactIDs for which the provider reports an active observation on any Device.
	ArtifactsWithObservations(ctx context.Context, artifactIDs []string) (map[string]bool, error)
	// ObservationStateSince returns, per pair, when the current observed state began (the newest history row).
	ObservationStateSince(ctx context.Context, pairs []ObsPair) (map[ObsPair]time.Time, error)
	// ObservationsRetiredAt returns, per pair, when its observation was retired (pairs without a retired observation are absent).
	ObservationsRetiredAt(ctx context.Context, pairs []ObsPair) (map[ObsPair]time.Time, error)
	// IneffectiveCursor is the id after which the next assignment_ineffective pass starts ("" = from the beginning).
	IneffectiveCursor(ctx context.Context, provider string) (string, error)
	SaveIneffectiveCursorTx(ctx context.Context, tx pgx.Tx, provider, cursor string) error
}

// ---- view results ----

// GroupRef names a Directory Group inside a view. Without organization.directory.view the group is
// Redacted: neither external id nor name is returned.
type GroupRef struct {
	ExternalID *string
	Name       *string
	Redacted   bool
}

// UserRef identifies a User inside a view; it is shown only to callers who may view Directory Users and Assets.
type UserRef struct {
	ID       *string
	Name     *string
	Redacted bool
}

// ExpectedApplicability is Turaco's own evaluation (never provider state).
type ExpectedApplicability struct {
	Result      string
	Confidence  string
	Reasons     []string
	EvaluatedAt time.Time
}

// ObservedState is the provider's latest result with freshness.
type ObservedState struct {
	State        string
	RawStatus    string
	Source       string
	ObservedAt   time.Time
	LastSyncedAt time.Time
	Stale        bool
}

// AssignedTarget is a current assignment that addresses the evaluated subject.
type AssignedTarget struct {
	AssignmentID string
	TargetKind   string
	Group        *GroupRef
	Mode         string
	Intent       string
	FilterMode   string
	Filter       *FilterSummary
	// Match is yes or unknown (the subject might be addressed; its User is unknown).
	Match   string
	Origins []string
	// Nested is set when the assignment targets a parent of the viewed Directory Group.
	Nested       bool
	Source       string
	LastSyncedAt time.Time
}

// DeviceArtifactStatus keeps Assigned, Expected Applicable and Observed apart for one artifact on one Device.
type DeviceArtifactStatus struct {
	Artifact    Artifact
	Assigned    bool
	Assignments []AssignedTarget
	Expected    ExpectedApplicability
	Observed    *ObservedState
	Mismatch    string
}

// DeviceManagementFilter selects the artifacts of the device view.
type DeviceManagementFilter struct {
	Kind string
	// State is an observed state or ObservedNone.
	State string
	// Expected is an evaluator result.
	Expected string
	Mismatch string
	Page     Page
}

// DeviceManagement is one page of the Device view.
type DeviceManagement struct {
	Items      []DeviceArtifactStatus
	NextCursor string
	// Truncated is set when the result may be incomplete: the scan limit ended the page early (NextCursor then
	// continues the scan) or a bounded directory input (memberships, nesting) was cut.
	Truncated bool
}

// PathStep is one explained step of an Assignment Path. Group references are redacted for callers without
// organization.directory.view; User origin is kept (it explains the result) but the User stays anonymous.
type PathStep struct {
	Kind         string
	Origin       string
	Group        *GroupRef
	AssignmentID string
	Mode         string
	Intent       string
	TargetKind   string
	FilterResult string
	Result       string
}

// AssignmentPathView is the explainable "why" of one expected applicability.
type AssignmentPathView struct {
	Device   Device
	Artifact Artifact
	User     *UserRef
	Expected ExpectedApplicability
	Path     []PathStep
	// Assignments lists the evaluated assignments that addressed the Device or might have.
	Assignments []AssignedTarget
	Observed    *ObservedState
}

// ResultCounts counts evaluated Devices by evaluator result.
type ResultCounts map[string]int

// DeviceExample is one example Device in a count. Examples are returned only to callers with device access.
type DeviceExample struct {
	DeviceID   string
	Name       string
	Result     string
	Confidence string
	Observed   string
}

// Evaluation describes how a computed count was obtained.
type Evaluation struct {
	// Shown is false when the caller may not see Devices; the counts and examples are then empty.
	Shown bool
	// Evaluated is the number of candidate Devices evaluated; Truncated says there were more candidates.
	Evaluated int
	Truncated bool
	Expected  ResultCounts
	Observed  map[string]int
	Examples  []DeviceExample
}

// GroupArtifact is one artifact that targets a Directory Group.
type GroupArtifact struct {
	Artifact    Artifact
	Assignments []AssignedTarget
	Evaluation  Evaluation
}

// GroupManagement is one page of the Directory Group view.
type GroupManagement struct {
	GroupID    string
	ExternalID string
	Name       string
	Items      []GroupArtifact
	NextCursor string
	// CandidateDevices is the number of Devices reached by the group (members, nested groups, members' Devices), capped.
	CandidateDevices int
	// CandidatesTruncated is set when any bounded input was cut (candidate Devices, members, group nesting): the
	// artifact list and the counts may then be incomplete.
	CandidatesTruncated bool
}

// UserDevice is the per-Device result for a User's artifact.
type UserDevice struct {
	DeviceID string
	Name     string
	Expected ExpectedApplicability
	Observed *ObservedState
}

// UserArtifact is an artifact reaching a User, with user targeting and the User's Devices apart.
type UserArtifact struct {
	Artifact Artifact
	// Targeting lists the assignments that address the User (not the Devices).
	Targeting []AssignedTarget
	// UserResult is applicable, excluded or not_applicable for the User ignoring Device filters; filters are listed on the assignments.
	UserResult string
	Devices    []UserDevice
}

// UserManagement is one page of the User view.
type UserManagement struct {
	UserID           string
	Name             string
	Items            []UserArtifact
	NextCursor       string
	DevicesTruncated bool
	// Truncated is set when the User's memberships or the group nesting were cut: the artifact list may be incomplete.
	Truncated bool
	// DevicesShown is false when the caller may not see the User's Devices.
	DevicesShown bool
}

// ArtifactTargets is the reverse lookup of an artifact.
type ArtifactTargets struct {
	Artifact    Artifact
	Assignments []AssignedTarget
	Evaluation  Evaluation
	// ObservedTotal counts the live Devices per observed state over all Devices, not only the evaluated ones.
	ObservedTotal map[string]int
}

// ObsPair identifies the observation of an artifact on a Device.
type ObsPair struct{ ArtifactID, DeviceID string }
