package application

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Deployment execution (F9 G3, docs/product/f9-software-lifecycle-design.md#g3-implementation-notes,
// docs/domain/state-machines.md#deployment).

// Execution statuses of a Deployment (after the planning statuses of G2).
const (
	DeploymentResolvingTargets   = "resolving_targets"
	DeploymentReady              = "ready"
	DeploymentRunning            = "running"
	DeploymentPaused             = "paused"
	DeploymentCompleted          = "completed"
	DeploymentCompletedWithError = "completed_with_errors"
	DeploymentFailed             = "failed"
)

// Ring statuses.
const (
	RingPending           = "pending"
	RingActive            = "active"
	RingAwaitingPromotion = "awaiting_promotion"
	RingPromoted          = "promoted"
	RingHalted            = "halted"
)

// RingStatuses lists every ring status.
var RingStatuses = []string{RingPending, RingActive, RingAwaitingPromotion, RingPromoted, RingHalted}

// Deployment Target states.
const (
	TargetPending             = "pending"
	TargetAssignmentRequested = "assignment_requested"
	TargetAwaitingObservation = "awaiting_observation"
	TargetSuccessful          = "successful"
	TargetFailed              = "failed"
	TargetExpired             = "expired"
	TargetAlreadySatisfied    = "already_satisfied"
	TargetNotApplicable       = "not_applicable"
	TargetCancelled           = "cancelled"
)

// TargetStates lists every target state.
var TargetStates = []string{TargetPending, TargetAssignmentRequested, TargetAwaitingObservation, TargetSuccessful, TargetFailed,
	TargetExpired, TargetAlreadySatisfied, TargetNotApplicable, TargetCancelled}

// Attempt kinds and outcomes.
const (
	AttemptSet       = "set_assignment"
	AttemptClear     = "clear_assignment"
	OutcomeAccepted  = "accepted"
	OutcomeTransient = "transient_error"
	OutcomePermanent = "permanent_error"
)

// Execution events.
const (
	EventDeploymentStarted   = "DeploymentStarted"
	EventRingActivated       = "RingActivated"
	EventRingPromoted        = "RingPromoted"
	EventRingHalted          = "RingHalted"
	EventDeploymentCompleted = "DeploymentCompleted"
	EventDeploymentFailed    = "DeploymentFailed"
	// RingApprovalSubject is the Approvals subject type of a ring promotion Approval (subject id: the ring run).
	RingApprovalSubject = "deployment_ring"
)

// Job and actor of the execution engine.
const (
	DeploymentTickJobType    = "endpoints.deployment_tick"
	DeploymentTickJobTimeout = 5 * time.Minute
	DeploymentTickInterval   = time.Minute
	// DeploymentEngineActor is the system actor of every engine step.
	DeploymentEngineActor = "deployment-engine"
	// DeployWriteActor is the system actor of the capability audit record.
	DeployWriteActor = "deploy-write"
)

// Engine limits and defaults.
const (
	// DefaultObservationExpiry is how long a target waits for evidence after the assignment was read back.
	DefaultObservationExpiry = 72 * time.Hour
	// DefaultEvidenceFreshness is how recent the evidence behind a promotion gate must be.
	DefaultEvidenceFreshness = 24 * time.Hour
	// MaxTickDeployments bounds the Deployments one tick works on.
	MaxTickDeployments = 20
	// MaxAssignmentAttempts is how many writer attempts a ring gets before it halts (assignment_failed).
	MaxAssignmentAttempts = 5
	// MinAutoHaltSample is the number of decided targets after which the failure threshold may halt a ring (or all
	// of them when the ring has fewer).
	MinAutoHaltSample = 3
)

// Reasons (reason codes, never free text).
const (
	ReasonManualPause       = "manual_pause"
	ReasonManualHalt        = "manual_halt"
	ReasonFailureThreshold  = "failure_threshold"
	ReasonThresholdNotMet   = "threshold_not_met"
	ReasonAssignmentFailed  = "assignment_failed"
	ReasonVersionRevoked    = "version_revoked"
	ReasonProductBlocked    = "product_blocked"
	ReasonPackageGateClosed = "package_gate_closed"
	ReasonPackageNotPublish = "package_not_published"
	ReasonArtifactUnlinked  = "artifact_not_linked"
	ReasonHashMismatch      = "hash_mismatch"
	ReasonDeploymentCancel  = "deployment_cancelled"
	ReasonTooManyTargets    = "too_many_targets"
	ReasonEvalIncomplete    = "evaluation_incomplete"
	ReasonNoTargets         = "no_targets"
	ReasonDeviceGone        = "device_not_live"
	ReasonPlatformMismatch  = "platform_mismatch"
	ReasonProviderNA        = "provider_not_applicable"
	ReasonAlreadyPresent    = "version_present"
	ReasonNotInstalled      = "version_not_installed"
	ReasonObservedFailed    = "observed_failed"
	ReasonObservedApplied   = "observed_applied"
	ReasonExpired           = "observation_expired"
	ReasonNoAssignment      = "assignment_not_read_back"
)

// Gate codes of execution (409 endpoints.<code>).
const (
	CodeDeployWriteDisabled = "deploy_write_disabled"
	CodeWindowClosed        = "change_window_closed"
	CodeRingNotAwaiting     = "ring_not_awaiting_promotion"
	CodeSoakNotElapsed      = "soak_not_elapsed"
	CodeThresholdNotMet     = "threshold_not_met"
	CodeEvidenceNotFresh    = "evidence_not_fresh"
	CodePromotionApproval   = "promotion_approval_required"
	CodeRingHalted          = "ring_halted"
	CodeNoPreviousRing      = "no_next_ring"
	CodeRingNotHalted       = "ring_not_halted"
)

// Errors of execution.
var (
	// ErrSeparationOfPlanning means the starter took part in planning a high-impact plan.
	ErrSeparationOfPlanning = errors.New("endpoints: the person who planned a high-impact deployment cannot start it")
)

// DeploymentRingRun is the execution state of one ring.
type DeploymentRingRun struct {
	ID                           string
	DeploymentID                 string
	RingID                       string
	Position                     int
	Status                       string
	StatusReason                 *string
	GroupExternalID              string
	ActivatedAt                  *time.Time
	SettledAt                    *time.Time
	AwaitingSince                *time.Time
	PromotedAt                   *time.Time
	PromotedBy                   *string
	HaltedAt                     *time.Time
	AssignmentRequestedAt        *time.Time
	AssignmentClearedAt          *time.Time
	PromotionApprovalID          *string
	PromotionApprovalStatus      *string
	PromotionApprovalRequestedBy *string
	Version                      int
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
}

// DeploymentTarget is one Device of a ring.
type DeploymentTarget struct {
	ID                    string
	DeploymentID          string
	RingRunID             string
	DeviceID              string
	DeviceName            string
	State                 string
	StateReason           *string
	ResolvedAt            time.Time
	AssignmentRequestedAt *time.Time
	ReadBackAt            *time.Time
	ExpiresAt             *time.Time
	DecidedAt             *time.Time
	EvidenceObservedAt    *time.Time
	Version               int
	UpdatedAt             time.Time
}

// DeploymentAttempt is the immutable record of one provider write.
type DeploymentAttempt struct {
	ID           string
	DeploymentID string
	RingRunID    string
	Kind         string
	Attempt      int
	OperationID  string
	RequestedAt  time.Time
	OutcomeCode  string
	CreatedAt    time.Time
}

// RingTransition is one append-only ring lifecycle row.
type RingTransition struct {
	DeploymentID  string
	RingRunID     string
	From          *string
	To            string
	Operation     string
	Reason        *string
	ActorUserID   *string
	ActorSystem   *string
	CorrelationID string
}

// TargetInsert is one target created at resolution. State is pending, already_satisfied or not_applicable.
type TargetInsert struct {
	RingRunID string
	DeviceID  string
	State     string
	Reason    string
}

// DeviceFact is what resolution needs to know about a Device.
type DeviceFact struct {
	Live       bool
	Platform   string
	HasVersion bool
	External   string
	Provider   string
}

// PublishedPackage is the package the execution gates read: the published one of a version.
type PublishedPackage struct {
	PackageID          string
	ArtifactExternalID string
	ArtifactID         *string
	ArtifactPlatform   string
	HashMatches        bool
}

// TargetDecision is one decided target returned by the evidence step.
type TargetDecision struct {
	TargetID    string
	DeviceID    string
	State       string
	HasVersion  bool
	DeviceFresh bool
	ObservedAt  time.Time
}

// RingCounts are the target counts of a ring by state plus the evidence figures of the gates.
type RingCounts struct {
	ByState map[string]int
	// FreshSuccessful: successful targets whose evidence was synchronized within the freshness window.
	FreshSuccessful int
	// FreshObserved: targets that count (not already_satisfied, not_applicable, cancelled) with fresh evidence.
	FreshObserved int
}

// Total is the number of targets.
func (c RingCounts) Total() int {
	n := 0
	for _, v := range c.ByState {
		n += v
	}
	return n
}

// Denominator is the success-rate base: targets minus already_satisfied, not_applicable and cancelled.
func (c RingCounts) Denominator() int {
	return c.Total() - c.ByState[TargetAlreadySatisfied] - c.ByState[TargetNotApplicable] - c.ByState[TargetCancelled]
}

// Open is the number of targets without a final state.
func (c RingCounts) Open() int {
	return c.ByState[TargetPending] + c.ByState[TargetAssignmentRequested] + c.ByState[TargetAwaitingObservation]
}

// TargetFilter selects the targets of a ring.
type TargetFilter struct {
	State string
	Page  Page
}

// TargetResult is a page of targets; NamesRedacted says device names were left out (no endpoints.view).
type TargetResult struct {
	Items         []DeploymentTarget
	NextCursor    string
	NamesRedacted bool
}

// RingProgress is the progress of one ring.
type RingProgress struct {
	Run      DeploymentRingRun
	Ring     DeploymentRing
	Counts   RingCounts
	Rate     *float64
	SoakLeft time.Duration
	// NextGate names what the ring waits for: observations, soak, threshold, approval, promotion, window or none.
	NextGate string
}

// DeploymentProgress is the progress of a Deployment.
type DeploymentProgress struct {
	Deployment Deployment
	Rings      []RingProgress
}

// AttemptResult is a page of attempts.
type AttemptResult struct {
	Items      []DeploymentAttempt
	NextCursor string
}

// ExecutionStore persists the execution state. The lock order is deployment -> ring runs -> targets.
type ExecutionStore interface {
	InsertRingRunsTx(ctx context.Context, tx pgx.Tx, runs []DeploymentRingRun) ([]DeploymentRingRun, error)
	RingRunsTx(ctx context.Context, tx pgx.Tx, deploymentID string) ([]DeploymentRingRun, error)
	RingRuns(ctx context.Context, deploymentID string) ([]DeploymentRingRun, error)
	UpdateRingRunTx(ctx context.Context, tx pgx.Tx, run DeploymentRingRun) (DeploymentRingRun, error)
	AppendRingTransitionTx(ctx context.Context, tx pgx.Tx, t RingTransition) error
	// RingRunByIDTx reads a ring run without locking it (lock the Deployment first, then read it again).
	RingRunByIDTx(ctx context.Context, tx pgx.Tx, id string) (DeploymentRingRun, error)

	DeviceFactsTx(ctx context.Context, tx pgx.Tx, deviceIDs []string, productID, productVersion string) (map[string]DeviceFact, error)
	InsertTargetsTx(ctx context.Context, tx pgx.Tx, deploymentID string, targets []TargetInsert, now time.Time, correlationID string) error
	PublishedPackageTx(ctx context.Context, tx pgx.Tx, versionID string) (PublishedPackage, bool, error)
	// RingDeviceExternalIDsTx returns the provider ids of the ring targets that are not already satisfied, not applicable or cancelled.
	RingDeviceExternalIDsTx(ctx context.Context, tx pgx.Tx, ringRunID string) ([]string, error)
	RequestAssignmentsTx(ctx context.Context, tx pgx.Tx, ringRunID string, now time.Time, correlationID string) (int, error)
	// DetectReadBackTx moves assignment_requested targets whose assignment and group membership were read back by
	// the management synchronization to awaiting_observation (read_back_at = now, expires_at = now + expiry).
	DetectReadBackTx(ctx context.Context, tx pgx.Tx, ringRunID, artifactID, groupExternalID, intent string, now time.Time, expiry time.Duration, correlationID string) (int, error)
	// DecideTargetsTx decides awaiting_observation targets from Management Observations newer than their read-back.
	DecideTargetsTx(ctx context.Context, tx pgx.Tx, ringRunID, artifactID, productID, productVersion string, now time.Time, correlationID string) ([]TargetDecision, error)
	ExpireTargetsTx(ctx context.Context, tx pgx.Tx, ringRunID string, now time.Time, correlationID string) (int, error)
	CancelTargetsTx(ctx context.Context, tx pgx.Tx, deploymentID string, now time.Time, correlationID string) (int, error)
	RingCountsTx(ctx context.Context, tx pgx.Tx, ringRunID, artifactID string, freshSince time.Time) (RingCounts, error)
	RingCounts(ctx context.Context, ringRunID, artifactID string, freshSince time.Time) (RingCounts, error)
	ListTargets(ctx context.Context, ringRunID string, f TargetFilter) (TargetResult, error)

	AttemptStateTx(ctx context.Context, tx pgx.Tx, ringRunID, kind string) (n int, accepted bool, err error)
	InsertAttemptTx(ctx context.Context, tx pgx.Tx, a DeploymentAttempt, correlationID string) error
	ListAttempts(ctx context.Context, deploymentID string, page Page) (AttemptResult, error)
	// EngineWork returns the Deployments the engine works on: resolving_targets and running ones and cancelled ones
	// with ring assignments still to clear.
	EngineWork(ctx context.Context, limit int) ([]string, error)
}
