package application

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Target Sets and Deployment planning (F9 G2, docs/product/f9-software-lifecycle-design.md,
// docs/domain/state-machines.md#deployment).

// Deployment permissions.
const (
	PermDeploymentsView       = "deployments.view"
	PermDeploymentsManage     = "deployments.manage"
	PermDeploymentsExecute    = "deployments.execute"
	PermDeploymentsHighImpact = "deployments.high_impact"
	// PermDeploymentsApprove is what a plan approver must hold (checked at submission and at the decision).
	PermDeploymentsApprove = "deployments.approve"
)

// Deployment events.
const (
	EventTargetSetChanged     = "TargetSetChanged"
	EventDeploymentScheduled  = "DeploymentScheduled"
	EventDeploymentCancelled  = "DeploymentCancelled"
	DeploymentApprovalSubject = "deployment"
)

// Deployment planning statuses (G2). Execution statuses arrive with G3.
const (
	DeploymentDraft           = "draft"
	DeploymentPendingApproval = "pending_approval"
	DeploymentApproved        = "approved"
	DeploymentScheduled       = "scheduled"
	DeploymentCancelled       = "cancelled"
)

// DeploymentStatuses lists every planning status.
var DeploymentStatuses = []string{DeploymentDraft, DeploymentPendingApproval, DeploymentApproved, DeploymentScheduled, DeploymentCancelled}

// DeploymentBindingStatuses are the statuses in which a plan binds its Target Sets: they cannot be changed or
// archived, and other plans compare their targets with it (overlap). It lists the planned G3 execution statuses
// too (running, paused, halted, resolving_targets, ready) so G3 cannot forget them; every check uses this list.
var DeploymentBindingStatuses = []string{DeploymentPendingApproval, DeploymentApproved, DeploymentScheduled,
	"resolving_targets", "ready", "running", "paused", "halted"}

// Deployment intents.
const (
	IntentInstall   = "install"
	IntentUpdate    = "update"
	IntentUninstall = "uninstall"
)

// DeploymentIntents lists every intent.
var DeploymentIntents = []string{IntentInstall, IntentUpdate, IntentUninstall}

// Reason codes.
var (
	DeploymentCancelReasons = []string{"superseded", "no_longer_needed", "plan_error", "security_risk", "other"}
	// ReasonApprovalRejected is the status reason of a plan whose Approval was rejected (back to draft).
	ReasonApprovalRejected = "approval_rejected"
	// ReasonApproverNotAuthorized is the status reason of a plan whose decided Approval could not be verified: it
	// is not the pending one, its status differs from the event, or the decider lacks deployments.approve or took
	// part in the plan (back to draft).
	ReasonApproverNotAuthorized = "approver_not_authorized"
)

// High-impact reasons, in the order they are reported.
const (
	HighImpactUninstall       = "uninstall"
	HighImpactSupersede       = "supersede"
	HighImpactAllDevices      = "all_devices"
	HighImpactNestedRootGroup = "nested_root_group"
	HighImpactTargetCount     = "target_count"
)

// High-impact thresholds: a plan whose rings together target at least HighImpactTargetThreshold Devices or at
// least HighImpactFleetPercent percent of the provider's live Devices is high impact.
const (
	HighImpactTargetThreshold = 200
	HighImpactFleetPercent    = 25
)

// Evaluation bounds of one request: at most MaxEvaluationScans Devices looked at over all Target Sets it evaluates
// and at most EvaluationDeadline wall time; a request that hits either gets an incomplete evaluation. One user runs
// at most one evaluating request at a time (ErrEvaluationBusy).
const (
	MaxEvaluationScans = 200000
	EvaluationDeadline = 20 * time.Second
)

// CodePlanChanged refuses a submission or scheduling whose plan changed after it was validated, or an approved plan
// whose hash differs from the approved one.
const CodePlanChanged = "plan_changed"

// Limits of Target Sets and Deployments.
const (
	// MaxTargetDevices caps an evaluation result and a ring's targets.
	MaxTargetDevices = 5000
	// MaxTargetScan bounds the live Devices looked at for one evaluation.
	MaxTargetScan = MaxSnapshotDevices
	// targetBatch is the number of Devices evaluated per read.
	targetBatch = 1000
	// MaxTargetExamples bounds the example Devices of an evaluation.
	MaxTargetExamples = 10
	MaxRings          = 10
	MaxSoakMinutes    = 30 * 24 * 60
	maxEditors        = 50
	// maxOverlapDeployments bounds the other Deployments compared for overlapping targets; maxOverlapSets the
	// Target Sets of those Deployments evaluated for it (overlap_check_truncated beyond).
	maxOverlapDeployments = 10
	maxOverlapSets        = 5
)

// Validation issue codes. Blocking issues prevent submission and scheduling; warnings never block.
const (
	IssueVersionNotApproved   = "version_not_approved"
	IssueProductNotApproved   = "product_not_approved"
	IssuePackageGateClosed    = "package_gate_closed"
	IssuePackageNotPublished  = "package_not_published"
	IssueNoRings              = "no_rings"
	IssueTargetSetArchived    = "target_set_archived"
	IssueWindowRequired       = "window_required"
	IssueChangeWindowInvalid  = "change_window_invalid"
	IssueApprovalRequired     = "approval_required"
	IssueTooManyTargets       = "too_many_targets"
	IssueNoTargets            = "no_targets"
	IssueEvaluationIncomplete = "evaluation_incomplete"
	IssueOverlap              = "overlapping_deployment"
	IssueHighImpact           = "high_impact"
	// IssueNoWindowHighImpact: a ring without a maintenance window targets a high-impact Target Set or at least
	// the high-impact threshold of Devices.
	IssueNoWindowHighImpact = "no_window_high_impact"
	// IssueOverlapTruncated: more Target Sets of other plans than maxOverlapSets would have to be compared.
	IssueOverlapTruncated = "overlap_check_truncated"
)

var (
	// ErrNoEligibleApprover means the chosen approver is inactive or excluded (owner, creator, editor, submitter).
	ErrNoEligibleApprover = errors.New("endpoints: the approver is not eligible for this deployment")
	// ErrTargetSetInUse means the Target Set belongs to a plan that is submitted, approved or scheduled.
	ErrTargetSetInUse = errors.New("endpoints: the target set is used by a submitted, approved or scheduled deployment")
	// ErrTargetSetNameTaken means another Target Set in use has that name (case-insensitive).
	ErrTargetSetNameTaken = errors.New("endpoints: a target set with this name exists")
	// ErrEditorsFull means the plan has the maximum number of editors and the caller is not one of them.
	ErrEditorsFull = errors.New("endpoints: the deployment has the maximum number of editors")
	// ErrEvaluationBusy means the user already runs an evaluating request.
	ErrEvaluationBusy = errors.New("endpoints: another evaluation of this user is running")
)

// PlanInvalidError means submission or scheduling is refused because the plan has blocking issues.
type PlanInvalidError struct{ Issues []PlanIssue }

func (e *PlanInvalidError) Error() string { return "endpoints: the deployment plan is not valid" }

// TargetSet is a saved, bounded device query.
type TargetSet struct {
	ID          string
	Reference   string
	Name        string
	Description *string
	OwnerUserID string
	Definition  TargetDefinition
	AllDevices  bool
	// HighImpactReason is all_devices or nested_root_group when the saved definition is high impact (snapshot).
	HighImpactReason *string
	ArchivedAt       *time.Time
	ArchivedBy       *string
	CreatedBy        string
	UpdatedBy        string
	// IncludeDeviceCount and ExcludeDeviceCount are the sizes of the explicit lists; DeviceListsRedacted says the
	// lists were removed from Definition because the reader lacks endpoints.view.
	IncludeDeviceCount  int
	ExcludeDeviceCount  int
	DeviceListsRedacted bool
	Version             int
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// TargetSetInput is the input of CreateTargetSet and UpdateTargetSet. OwnerUserID empty means the caller.
type TargetSetInput struct {
	Name        string
	Description string
	OwnerUserID string
	Definition  TargetDefinition
}

// TargetSetFilter selects Target Sets.
type TargetSetFilter struct {
	IncludeArchived bool
	Page            Page
}

type TargetSetResult struct {
	Items      []TargetSet
	NextCursor string
}

// TargetExample is an example Device of an evaluation (only for readers with endpoints.view).
type TargetExample struct {
	DeviceID        string
	Name            string
	OSPlatform      string
	ComplianceState string
}

// TargetEvaluation is the bounded result of evaluating a Target Set now. Truncated: more than MaxTargetDevices
// matched or more than MaxTargetScan Devices exist (counts are then lower bounds). Incomplete: a directory lookup
// for nested groups was cut or the request's scan budget or deadline was reached, so Devices may be missing.
// ExamplesRedacted: the reader gets counts only (no endpoints.view, or read access only as a plan approver).
type TargetEvaluation struct {
	Matched          int
	Scanned          int
	Truncated        bool
	Incomplete       bool
	ByPlatform       map[string]int
	ByCompliance     map[string]int
	Examples         []TargetExample
	ExamplesRedacted bool
	EvaluatedAt      time.Time
	// deviceIDs are the matched Devices (internal: overlap checks).
	deviceIDs []string
}

// TargetExplanation says why a Device is in or out of a Target Set.
type TargetExplanation struct {
	TargetSetID string
	DeviceID    string
	DeviceName  string
	Matched     bool
	Clauses     []ClauseResult
	Incomplete  bool
	EvaluatedAt time.Time
}

// Deployment is a planned rollout of one Software Version with one intent (its Desired Software State) in rings.
type Deployment struct {
	ID                string
	Reference         string
	Name              string
	SoftwareVersionID string
	ProductID         string
	ProductName       string
	ProductVersion    string
	Intent            string
	Supersede         bool
	Status            string
	StatusReason      *string
	OwnerUserID       string
	CreatedBy         string
	Editors           []string
	HighImpact        bool
	ApprovalID        *string
	SubmittedBy       *string
	SubmittedAt       *time.Time
	PlanSHA256        *string
	ApprovedAt        *time.Time
	ScheduledBy       *string
	ScheduledAt       *time.Time
	CancelledBy       *string
	CancelledAt       *time.Time
	Version           int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// DeploymentRing is one stage of a Deployment with its gate configuration.
type DeploymentRing struct {
	ID                      string
	DeploymentID            string
	Position                int
	Name                    string
	TargetSetID             string
	TargetSetReference      string
	TargetSetName           string
	ApprovalRequired        bool
	SuccessThresholdPercent int
	MinFreshEvidencePercent *int
	SoakMinutes             int
	ChangeID                *string
	NoWindowRequired        bool
	MaxTargets              int
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

// RingInput is the input of AddRing and UpdateRing. MaxTargets nil means MaxTargetDevices.
type RingInput struct {
	Name                    string
	TargetSetID             string
	ApprovalRequired        bool
	SuccessThresholdPercent int
	MinFreshEvidencePercent *int
	SoakMinutes             int
	ChangeID                *string
	NoWindowRequired        bool
	MaxTargets              *int
}

// DeploymentInput is the input of CreateDeployment and UpdateDeployment. OwnerUserID empty means the caller
// (create) or unchanged (update).
type DeploymentInput struct {
	Name              string
	SoftwareVersionID string
	Intent            string
	Supersede         bool
	OwnerUserID       string
}

// DeploymentTransition is one append-only lifecycle row.
type DeploymentTransition struct {
	ID            string
	DeploymentID  string
	FromStatus    *string
	ToStatus      string
	Operation     string
	Reason        *string
	PlanSHA256    *string
	ActorUserID   *string
	ActorSystem   *string
	CorrelationID string
	CreatedAt     time.Time
}

// PlanIssue is one validation finding about a plan. RingID names the ring it is about; Count and DeploymentID
// carry the numbers and the other plan of an overlap.
type PlanIssue struct {
	Code         string
	Blocking     bool
	RingID       *string
	Count        *int
	DeploymentID *string
}

// RingTargets is the evaluated target count of one ring.
type RingTargets struct {
	RingID     string
	Matched    int
	Truncated  bool
	Incomplete bool
}

// PlanValidation is the result of validating a plan. Evaluated says the ring Target Sets were evaluated
// (Validate, submission, scheduling); a detail read only runs the structural checks and reports the high-impact
// snapshot. HighImpactReason is the first reason (uninstall, supersede, all_devices, nested_root_group,
// target_count). TotalTargets is the sum of the rings' evaluated target counts.
type PlanValidation struct {
	Valid            bool
	HighImpact       bool
	HighImpactReason string
	Evaluated        bool
	Incomplete       bool
	TotalTargets     int
	Issues           []PlanIssue
	Rings            []RingTargets
	ValidatedAt      time.Time
}

// ChangeWindow is what Deployment planning may know about a Change (changes/public): reference, status and window,
// plus requester and owner to apply the Changes read rule. Hidden is set in a detail read for a Change the reader
// may not read: then only ID is filled.
type ChangeWindow struct {
	ID          string
	Reference   string
	Status      string
	RequesterID string
	OwnerID     *string
	WindowStart *time.Time
	WindowEnd   *time.Time
	Hidden      bool
}

// DeploymentDetail is a Deployment with its rings, history, approvals and structural validation.
type DeploymentDetail struct {
	Deployment  Deployment
	Rings       []DeploymentRing
	Transitions []DeploymentTransition
	Approvals   []DeploymentApprovalInfo
	Changes     map[string]ChangeWindow
	Validation  PlanValidation
}

// DeploymentApprovalInfo is the state of a plan Approval.
type DeploymentApprovalInfo struct {
	ID              string
	Status          string
	ApproverUserID  *string
	ApproverTeamID  *string
	DecidedByUserID *string
	DecidedAt       *time.Time
}

// Approver names who approves a plan: exactly one of User and Team.
type Approver struct {
	UserID *string
	TeamID *string
}

// DeploymentFilter selects Deployments. Mine restricts to the caller's own (set by the service for callers
// without deployments.view).
type DeploymentFilter struct {
	Status    string
	ProductID string
	VersionID string
	MineOf    string
	Page      Page
}

type DeploymentResult struct {
	Items      []Deployment
	NextCursor string
}

// DeploymentApprovals is the port to the Approvals module (subject deployment).
type DeploymentApprovals interface {
	// RequestInTx requests the plan Approval in the caller's transaction; ErrNoEligibleApprover when the
	// approver is inactive or excluded.
	RequestInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, subjectID, label string, approver Approver, excluded []string) (string, error)
	CancelBySubjectInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, subjectID string) error
	ForSubject(ctx context.Context, subjectID string) ([]DeploymentApprovalInfo, error)
}

// DeploymentApprovers answers who may approve plans: the effective permissions of a User
// (platform/authorization/roles) and Team memberships (Organization work directory).
type DeploymentApprovers interface {
	Permissions(ctx context.Context, userID string) (map[string]struct{}, error)
	// TeamMemberIDs returns the current members of an active Team (none for an inactive or unknown Team).
	TeamMemberIDs(ctx context.Context, teamID string) ([]string, error)
	// TeamIDsOfUser returns the Teams the User currently belongs to.
	TeamIDsOfUser(ctx context.Context, userID string) ([]string, error)
}

// ChangeWindows is the port to the Changes module: id -> Change for the existing Changes among ids, with requester
// and owner so the caller applies the Changes read rule (changes/public.ReadScope with details).
type ChangeWindows interface {
	Lookup(ctx context.Context, ids []string) (map[string]ChangeWindow, error)
}

// AssetLocations is the port to the Assets module: asset id -> location id for the Assets among ids that have a
// location (at most targetBatch ids per call).
type AssetLocations interface {
	Locations(ctx context.Context, assetIDs []string) (map[string]string, error)
}

// DeploymentStore persists Target Sets and Deployments. Lock order: deployment -> target sets (FOR SHARE, id
// order) -> software package/version/product.
type DeploymentStore interface {
	InsertTargetSetTx(ctx context.Context, tx pgx.Tx, t TargetSet) (TargetSet, error)
	LockTargetSetTx(ctx context.Context, tx pgx.Tx, id string) (TargetSet, error)
	// ShareTargetSetsTx returns the Target Sets FOR SHARE in id order (missing ids are absent).
	ShareTargetSetsTx(ctx context.Context, tx pgx.Tx, ids []string) (map[string]TargetSet, error)
	UpdateTargetSetTx(ctx context.Context, tx pgx.Tx, t TargetSet) (TargetSet, error)
	// TargetSetInUseTx reports whether a Deployment in one of statuses has a ring on the Target Set.
	TargetSetInUseTx(ctx context.Context, tx pgx.Tx, id string, statuses []string) (bool, error)
	// DeploymentsUsingTargetSet returns up to limit ids of Deployments in one of statuses with a ring on the set.
	DeploymentsUsingTargetSet(ctx context.Context, id string, statuses []string, limit int) ([]string, error)
	GetTargetSet(ctx context.Context, id string) (TargetSet, error)
	ListTargetSets(ctx context.Context, f TargetSetFilter) (TargetSetResult, error)

	InsertDeploymentTx(ctx context.Context, tx pgx.Tx, d Deployment) (Deployment, error)
	LockDeploymentTx(ctx context.Context, tx pgx.Tx, id string) (Deployment, error)
	// DeploymentTx reads a Deployment in tx without locking it.
	DeploymentTx(ctx context.Context, tx pgx.Tx, id string) (Deployment, error)
	UpdateDeploymentTx(ctx context.Context, tx pgx.Tx, d Deployment) (Deployment, error)
	AppendDeploymentTransitionTx(ctx context.Context, tx pgx.Tx, t DeploymentTransition) error
	RingsTx(ctx context.Context, tx pgx.Tx, deploymentID string) ([]DeploymentRing, error)
	InsertRingTx(ctx context.Context, tx pgx.Tx, r DeploymentRing) (DeploymentRing, error)
	UpdateRingTx(ctx context.Context, tx pgx.Tx, r DeploymentRing) (DeploymentRing, error)
	DeleteRingTx(ctx context.Context, tx pgx.Tx, ringID string) error
	// CheckRingPositionsTx checks the deferred ring position uniqueness now (ErrVersionConflict on a collision).
	CheckRingPositionsTx(ctx context.Context, tx pgx.Tx) error
	// PackageGateTx reports for a version whether any package has a closed gate (open hash mismatch or publication
	// after revoke, revoked version, blocked product) and whether any package is published.
	PackageGateTx(ctx context.Context, tx pgx.Tx, versionID string) (closed, published bool, err error)
	GetDeployment(ctx context.Context, id string) (Deployment, error)
	Rings(ctx context.Context, deploymentID string) ([]DeploymentRing, error)
	DeploymentTransitions(ctx context.Context, deploymentID string) ([]DeploymentTransition, error)
	ListDeployments(ctx context.Context, f DeploymentFilter) (DeploymentResult, error)
	// ActiveDeploymentsOfProduct returns up to limit Deployments of the product's versions in one of statuses,
	// except excludeID.
	ActiveDeploymentsOfProduct(ctx context.Context, productID, excludeID string, statuses []string, limit int) ([]Deployment, error)
}
