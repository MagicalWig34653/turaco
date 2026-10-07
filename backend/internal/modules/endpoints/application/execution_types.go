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
	// OutcomeInFlight is a write that was decided and not finished yet; a crash leaves it, the next tick turns it into
	// OutcomeInterrupted (the provider may or may not hold the write).
	OutcomeInFlight    = "in_flight"
	OutcomeInterrupted = "interrupted"
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
	// MaxClearAttempts is how many times clearing one ring assignment is tried before the Endpoint Finding
	// deployment_clear_failed is raised and the engine stops.
	MaxClearAttempts = 10
	// MaxRingRetries is how many explicit retries (ResumeRing with reason retry) a ring run gets after assignment_failed.
	MaxRingRetries = 3
	// MaxTargetGrowthPercent is how much the resolved target total may exceed the total evaluated at scheduling.
	MaxTargetGrowthPercent = 25
	// MaxProviderNotApplicablePercent is the share of provider not_applicable targets that halts a ring.
	MaxProviderNotApplicablePercent = 30
	// ResolvingStuckAfter is how long resolving_targets may last before progress reports it (worker stalled or the
	// write capability is not enabled on the worker).
	ResolvingStuckAfter = 15 * time.Minute
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
	ReasonBecameHighImpact  = "became_high_impact"
	ReasonTargetsGrew       = "targets_grew"
	ReasonNoEvidence        = "no_evidence"
	ReasonNARatio           = "not_applicable_ratio"
	ReasonReadBackMissing   = "read_back_missing"
	ReasonWindowClosed      = "window_closed"
	ReasonArtifactChanged   = "artifact_changed"
	ReasonProviderMismatch  = "provider_mismatch"
	// ReasonRetry is the ResumeRing reason that retries a ring halted with assignment_failed.
	ReasonRetry = "retry"
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
	CodeNoEvidence          = "no_evidence"
	CodeRetryRequired       = "retry_required"
	CodeRetryLimit          = "retry_limit_reached"
	CodeClearPending        = "clear_pending"
	CodeAssignmentCleared   = "assignment_cleared"
)

// Errors of execution.
var (
	// ErrSeparationOfPlanning means the starter took part in planning a high-impact plan.
	ErrSeparationOfPlanning = errors.New("endpoints: the person who planned a high-impact deployment cannot start it")
)

// DeploymentRingRun is the execution state of one ring.
type DeploymentRingRun struct {
	ID                    string
	DeploymentID          string
	RingID                string
	Position              int
	Status                string
	StatusReason          *string
	GroupExternalID       string
	ActivatedAt           *time.Time
	SettledAt             *time.Time
	AwaitingSince         *time.Time
	PromotedAt            *time.Time
	PromotedBy            *string
	HaltedAt              *time.Time
	AssignmentRequestedAt *time.Time
	AssignmentClearedAt   *time.Time
	// ClearRequestedAt queues the clearing of the ring assignment (cancel, kill switch).
	ClearRequestedAt *time.Time
	// ManagementArtifactID/ExternalID pin the artifact written at the first write.
	ManagementArtifactID         *string
	ManagementArtifactExternalID *string
	// AttemptBase is the number of set attempts before the last explicit retry; RetryCount the retries used.
	AttemptBase                  int
	RetryCount                   int
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
	FinishedAt   *time.Time
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
	// Fresh: the Device's inventory was synchronized within the evidence freshness window.
	Fresh    bool
	External string
	Provider string
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
	// ProviderNA: targets the provider reported not applicable (state_reason provider_not_applicable).
	ProviderNA int
}

// MinEvidence is the number of counted targets a ring needs before it may settle, promote or complete: all of them
// for a ring smaller than MinAutoHaltSample, MinAutoHaltSample otherwise, at least one.
func (c RingCounts) MinEvidence() int {
	return max(1, min(MinAutoHaltSample, c.Denominator()+c.ProviderNA))
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
	// ClearPending: the ring assignment is queued for clearing and not cleared yet (needs the write capability);
	// ClearFailed: clearing was given up after MaxClearAttempts (Endpoint Finding deployment_clear_failed).
	ClearPending bool
	ClearFailed  bool
	Run          DeploymentRingRun
	Ring         DeploymentRing
	Counts       RingCounts
	Rate         *float64
	SoakLeft     time.Duration
	// NextGate names what the ring waits for: observations, soak, threshold, approval, promotion, window or none.
	NextGate string
}

// DeploymentProgress is the progress of a Deployment.
type DeploymentProgress struct {
	// ClearPending: some ring assignment waits to be cleared. ResolvingStuck: resolving_targets lasts longer than
	// ResolvingStuckAfter (the worker is not running or its SOFTWARE_DEPLOY_WRITE differs from the API's).
	ClearPending   bool
	ResolvingStuck bool
	Deployment     Deployment
	Rings          []RingProgress
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

	DeviceFactsTx(ctx context.Context, tx pgx.Tx, deviceIDs []string, productID, productVersion string, freshSince time.Time) (map[string]DeviceFact, error)
	// RecheckSatisfiedTx moves pending targets whose fresh inventory shows the intent already satisfied to already_satisfied.
	RecheckSatisfiedTx(ctx context.Context, tx pgx.Tx, ringRunID, productID, productVersion string, uninstall bool, freshSince, now time.Time, correlationID string) (int, error)
	InsertTargetsTx(ctx context.Context, tx pgx.Tx, deploymentID string, targets []TargetInsert, now time.Time, correlationID string) error
	PublishedPackageTx(ctx context.Context, tx pgx.Tx, versionID string) (PublishedPackage, bool, error)
	// RingDeviceExternalIDsTx returns the provider ids of the ring targets of the provider that are not already satisfied,
	// not applicable or cancelled.
	RingDeviceExternalIDsTx(ctx context.Context, tx pgx.Tx, ringRunID, provider string) ([]string, error)
	// RingFirstDeviceTx returns a Device of the ring (the first target) to hang a ring finding on.
	RingFirstDeviceTx(ctx context.Context, tx pgx.Tx, ringRunID string) (string, error)
	RequestAssignmentsTx(ctx context.Context, tx pgx.Tx, ringRunID string, now time.Time, correlationID string) (int, error)
	// DetectReadBackTx moves assignment_requested targets whose assignment and group membership were read back by
	// the management synchronization to awaiting_observation (read_back_at = now, expires_at = now + expiry).
	DetectReadBackTx(ctx context.Context, tx pgx.Tx, ringRunID, artifactID, groupExternalID, intent string, now time.Time, expiry time.Duration, correlationID string) (int, error)
	// DecideTargetsTx decides awaiting_observation targets from Management Observations newer than their read-back.
	DecideTargetsTx(ctx context.Context, tx pgx.Tx, ringRunID, artifactID, productID, productVersion string, now time.Time, correlationID string) ([]TargetDecision, error)
	// ExpireTargetsTx expires awaiting_observation targets past expires_at and assignment_requested targets whose
	// assignment was not read back within expiry of assignment_requested_at (reason read_back_missing).
	ExpireTargetsTx(ctx context.Context, tx pgx.Tx, ringRunID string, now time.Time, expiry time.Duration, correlationID string) (int, error)
	CancelTargetsTx(ctx context.Context, tx pgx.Tx, deploymentID string, now time.Time, correlationID string) (int, error)
	RingCountsTx(ctx context.Context, tx pgx.Tx, ringRunID, artifactID string, freshSince time.Time) (RingCounts, error)
	RingCounts(ctx context.Context, ringRunID, artifactID string, freshSince time.Time) (RingCounts, error)
	ListTargets(ctx context.Context, ringRunID string, f TargetFilter) (TargetResult, error)

	AttemptStateTx(ctx context.Context, tx pgx.Tx, ringRunID, kind string) (n int, accepted bool, err error)
	// BeginAttemptTx records the decision to write (outcome in_flight) before the provider call.
	BeginAttemptTx(ctx context.Context, tx pgx.Tx, a DeploymentAttempt, correlationID string) error
	// FinishAttemptTx records the outcome of an in_flight attempt.
	FinishAttemptTx(ctx context.Context, tx pgx.Tx, ringRunID, kind string, attempt int, outcome string, now time.Time) error
	// InterruptAttemptsTx turns the in_flight attempts of a Deployment into interrupted and returns them.
	InterruptAttemptsTx(ctx context.Context, tx pgx.Tx, deploymentID string, now time.Time) ([]DeploymentAttempt, error)
	ListAttempts(ctx context.Context, deploymentID string, page Page) (AttemptResult, error)
	// EngineWork returns at most limit Deployments with due work (resolving_targets, running, or ring assignments
	// queued for clearing), the ones ticked longest ago first.
	EngineWork(ctx context.Context, limit int) ([]string, error)
	// TouchTicked records that the engine worked on the Deployment now (round robin; no version change).
	TouchTicked(ctx context.Context, id string) error
	// GateSweep returns the running and paused Deployments whose security gates look closed (set-based, uncapped);
	// executionGate decides.
	GateSweep(ctx context.Context) ([]string, error)
}
