// Package application holds the Changes use cases: the Change lifecycle
// (draft to closed), its approval through Approvals, execution Tasks, the
// affected resources as platform Relationships ("change AFFECTS ...") and the
// communication to the owners of affected Services
// (docs/product/f7-infrastructure-change-design.md, slice 3).
package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

// Change statuses (docs/domain/state-machines.md).
const (
	StatusDraft           = "draft"
	StatusAssessment      = "assessment"
	StatusPendingApproval = "pending_approval"
	StatusApproved        = "approved"
	StatusScheduled       = "scheduled"
	StatusInProgress      = "in_progress"
	StatusCompleted       = "completed"
	StatusReview          = "review"
	StatusClosed          = "closed"
	StatusRejected        = "rejected"
	StatusFailed          = "failed"
	StatusCancelled       = "cancelled"
)

// Statuses lists every status.
var Statuses = []string{StatusDraft, StatusAssessment, StatusPendingApproval, StatusApproved, StatusScheduled, StatusInProgress,
	StatusCompleted, StatusReview, StatusClosed, StatusRejected, StatusFailed, StatusCancelled}

// Kinds and risk levels.
const (
	KindStandard  = "standard"
	KindNormal    = "normal"
	KindEmergency = "emergency"

	RiskLow    = "low"
	RiskMedium = "medium"
	RiskHigh   = "high"
)

var (
	Kinds = []string{KindStandard, KindNormal, KindEmergency}
	Risks = []string{RiskLow, RiskMedium, RiskHigh}
	// CancelReasons are the reason codes Cancel accepts.
	CancelReasons = []string{"no_longer_needed", "superseded", "rescheduled", "risk_too_high", "error_correction", "other"}
	// FailReasons are the reason codes Fail accepts.
	FailReasons = []string{"execution_error", "verification_failed", "window_exceeded", "dependency_unavailable", "other"}
)

// Reason codes written by the module itself.
const (
	// ReasonApprovalRejected is stored on a Change whose approval was rejected.
	ReasonApprovalRejected = "approval_rejected"
	// ReasonTasksWaived is the only force reason Complete accepts; it completes a
	// Change although execution Tasks are still open (they are cancelled).
	ReasonTasksWaived = "tasks_waived"
	// reasonEmergency marks the emergency approval path.
	reasonEmergency = "emergency"
	reasonRemoved   = "removed"
	// ReasonChangeClosed is the reason the open execution Tasks of a closed
	// Change are cancelled with, and the end reason of its AFFECTS links.
	ReasonChangeClosed = "change_closed"
	// End reasons of the AFFECTS links of a cancelled or rejected Change.
	ReasonChangeCancelled = "change_cancelled"
	ReasonChangeRejected  = "change_rejected"
)

// linkEndReason is the end reason of the AFFECTS links of a Change in a
// terminal status; empty while the Change is still open (its links are current).
func linkEndReason(status string) string {
	switch status {
	case StatusClosed:
		return ReasonChangeClosed
	case StatusCancelled:
		return ReasonChangeCancelled
	case StatusRejected:
		return ReasonChangeRejected
	}
	return ""
}

// Permissions of the Changes module (platform/permissions registry).
const (
	PermView    = "changes.view"
	PermManage  = "changes.manage"
	PermExecute = "changes.execute"
)

// Relationship names used with platform/relationships.
const (
	NodeChange   = "change"
	NodeService  = "service"
	NodeVM       = "vm"
	NodeAsset    = "asset"
	NodeLocation = "location"

	RelAffects = "AFFECTS"

	// RelationshipOwner owns every triple of this module in the relationship registry.
	RelationshipOwner = "changes"
)

// Triples are the relationships this module registers: a Change affects a
// Service, Virtual Machine, Asset or Location (Site).
var Triples = []relationships.Triple{
	{SourceType: NodeChange, Type: RelAffects, TargetType: NodeService, Owner: RelationshipOwner},
	{SourceType: NodeChange, Type: RelAffects, TargetType: NodeVM, Owner: RelationshipOwner},
	{SourceType: NodeChange, Type: RelAffects, TargetType: NodeAsset, Owner: RelationshipOwner},
	{SourceType: NodeChange, Type: RelAffects, TargetType: NodeLocation, Owner: RelationshipOwner},
}

// AffectedTargets are the record types a Change may affect.
var AffectedTargets = []string{NodeService, NodeVM, NodeAsset, NodeLocation}

const (
	// SubjectType is the Approval subject type of a Change.
	SubjectType = "change"
	// TaskContextType is the Task context type of a Change's execution Tasks.
	TaskContextType = "change"

	DefaultLimit = 50
	MaxLimit     = 200
	// MaxAffected bounds the resources one Change may affect (and the impact walks it starts).
	MaxAffected = 25
	// MaxTasks bounds the execution Tasks of one Change.
	MaxTasks = 50
	// MaxWindow is the longest maintenance window.
	MaxWindow = 30 * 24 * time.Hour
	// EmergencyPastGrace is how far in the past an emergency change's window may start when it is scheduled.
	EmergencyPastGrace = time.Hour
	// ImpactNodeCap bounds the records one Change impact response lists in all.
	ImpactNodeCap = 1000
	maxTitle      = 150
	maxDesc       = 4000
	maxRollback   = 4000
	maxJustify    = 1000
	maxOutcome    = 1000
	maxTaskTitle  = 200
)

// Change is a planned alteration of infrastructure or services.
type Change struct {
	ID                     string
	Reference              string
	Title                  string
	Description            *string
	Kind                   string
	Risk                   string
	Status                 string
	StatusReason           *string
	RequesterID            string
	OwnerID                *string
	RollbackPlan           *string
	EmergencyJustification *string
	WindowStart            *time.Time
	WindowEnd              *time.Time
	OutcomeNote            *string
	RollbackDone           *bool
	ApprovalID             *string
	// ApprovedWindowStart/End are the maintenance window the approver approved;
	// Schedule keeps a non-emergency Change within it.
	ApprovedWindowStart *time.Time
	ApprovedWindowEnd   *time.Time
	// EmergencyApprovedBy approved an emergency change on its justification; they never review or close it.
	EmergencyApprovedBy *string
	// Editors are the Users who edited, submitted or assessed the Change; with the requester they can never approve it.
	Editors     []string
	RemindedFor *time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time
	ClosedAt    *time.Time
	Version     int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ReviewRequired reports that the Change cannot be closed without a review:
// emergency changes always get a post-hoc review.
func (c Change) ReviewRequired() bool { return c.Kind == KindEmergency }

// Transition is one entry of a Change's append-only state history.
type Transition struct {
	ID            string
	ChangeID      string
	FromStatus    *string
	ToStatus      string
	Operation     string
	Reason        *string
	ActorUserID   *string
	ActorSystem   *string
	CorrelationID string
	CreatedAt     time.Time
}

// Window is a maintenance window. Both ends nil clears the window.
type Window struct {
	Start *time.Time
	End   *time.Time
}

// Principal is the caller's authority. View/Manage/Execute are changes.view,
// changes.manage and changes.execute; ServicesView, InfraView and AssetsView say
// which affected resources the caller may see by name (services.view|manage,
// infrastructure.view|manage, assets.view|manage).
type Principal struct {
	UserID       string
	View         bool
	Manage       bool
	Execute      bool
	ServicesView bool
	InfraView    bool
	AssetsView   bool
}

// readsAll reports that the caller may read every Change.
func (p Principal) readsAll() bool { return p.View || p.Manage || p.Execute }

// hides reports that the caller may not see records of the type.
func (p Principal) hides(nodeType string) bool {
	switch nodeType {
	case NodeService:
		return !p.ServicesView
	case NodeVM, NodeLocation:
		return !p.InfraView
	case NodeAsset:
		return !p.AssetsView
	}
	return true
}

// canExecute reports that the caller may run the Change: the holder of
// changes.execute or the Change's owner (the assignee).
func (p Principal) canExecute(c Change) bool {
	return p.Execute || p.UserID != "" && c.OwnerID != nil && *c.OwnerID == p.UserID
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
		return errors.New("changes: correlation id is required")
	}
	return nil
}

var (
	ErrNotFound           = errors.New("changes: not found")
	ErrForbidden          = errors.New("changes: forbidden")
	ErrInvalidCursor      = errors.New("changes: invalid cursor")
	ErrVersionConflict    = errors.New("changes: version conflict")
	ErrReferenceInvalid   = errors.New("changes: referenced record does not exist or is not usable")
	ErrNoEligibleApprover = errors.New("changes: no eligible approver")
	ErrReviewRequired     = errors.New("changes: an emergency change needs a review before it can be closed")
	ErrOpenTasks          = errors.New("changes: execution tasks are still open")
	ErrTooMany            = errors.New("changes: limit reached")
	ErrImpactBusy         = errors.New("changes: an impact traversal is already running for this user")
	// ErrSeparationOfDuties refuses a step the caller may not take on this
	// Change because of an earlier role: the requester and editors never assess
	// it, the emergency approver never reviews or closes it.
	ErrSeparationOfDuties = errors.New("changes: separation of duties forbids this step for the caller")
	// ErrWindowNotApproved refuses a schedule outside the approved maintenance window.
	ErrWindowNotApproved = errors.New("changes: the maintenance window lies outside the approved window")
)

// InvalidTransitionError reports an operation the status does not allow.
type InvalidTransitionError struct {
	Operation string
	From      string
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("changes: operation %s is not allowed in status %s", e.Operation, e.From)
}

// InvalidInputError carries a user-safe validation message.
type InvalidInputError struct {
	Message string
	// Issues names the fields that block the operation (all of them at once), so a client can point at each one.
	Issues []FieldIssue
}

// FieldIssue is one field-level problem: Field is the API name of the field or collection (windowStart,
// rollbackPlan, affectedResources), Code a stable machine code (required).
type FieldIssue struct {
	Field string
	Code  string
}

func (e *InvalidInputError) Error() string { return "changes: invalid input: " + e.Message }

func invalid(format string, args ...any) error {
	return &InvalidInputError{Message: fmt.Sprintf(format, args...)}
}

// Page is keyset pagination over the UUIDv7 primary key.
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
type Result[T any] struct {
	Items      []T
	NextCursor string
}

// Filter selects Changes. AffectedType and AffectedID go together. OnlyUserID
// restricts the list to the Changes that User requested or owns (set by the
// service for callers without general read access).
type Filter struct {
	Status       string
	Risk         string
	Kind         string
	OwnerID      string
	RequesterID  string
	AffectedType string
	AffectedID   string
	// WindowFrom and WindowTo select Changes whose window overlaps [from, to].
	WindowFrom *time.Time
	WindowTo   *time.Time
	OnlyUserID string
	// AffectedChangeIDs are the Changes linked to the affected resource (set by
	// the service from the relationships; used when AffectedType is set).
	AffectedChangeIDs []string
	Page              Page
}

// Store persists Changes. Tx methods run inside InTx.
type Store interface {
	InTx(ctx context.Context, fn func(tx pgx.Tx) error) error
	// Q reads relationships outside a transaction.
	Q() relationships.Querier

	InsertTx(ctx context.Context, tx pgx.Tx, c Change) (Change, error)
	LockTx(ctx context.Context, tx pgx.Tx, id string) (Change, error)
	// UpdateTx writes every mutable column and bumps the version.
	UpdateTx(ctx context.Context, tx pgx.Tx, c Change) (Change, error)
	GetTx(ctx context.Context, tx pgx.Tx, id string) (Change, error)
	Get(ctx context.Context, id string) (Change, error)
	List(ctx context.Context, f Filter) (Result[Change], error)

	InsertTransitionTx(ctx context.Context, tx pgx.Tx, t Transition) error
	Transitions(ctx context.Context, changeID string, page Page) (Result[Transition], error)

	AddTaskTx(ctx context.Context, tx pgx.Tx, changeID, taskID string, createdBy *string) error
	TaskIDsTx(ctx context.Context, tx pgx.Tx, changeID string) ([]string, error)
	TaskIDs(ctx context.Context, changeID string) ([]string, error)

	// ByIDs returns the Changes among ids (unknown ids are absent).
	ByIDs(ctx context.Context, ids []string) ([]Change, error)
	// InWindow lists the Changes in one of the statuses whose maintenance window
	// overlaps [from, to), ordered by window start and id, at most limit.
	InWindow(ctx context.Context, statuses []string, from, to time.Time, limit int) ([]Change, error)

	// DueReminderIDs lists scheduled Changes whose window starts within (from, until]
	// and that were not reminded for that window yet, in id order after afterID.
	DueReminderIDs(ctx context.Context, from, until time.Time, afterID string, limit int) ([]string, error)
	// MarkRemindedTx records the window start a reminder was sent for (no version change).
	MarkRemindedTx(ctx context.Context, tx pgx.Tx, id string, windowStart time.Time) error
}

// Directory is what Changes asks the Organization module.
type Directory interface {
	ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error)
	ActiveTeams(ctx context.Context, ids []string) (map[string]bool, error)
	ActiveLocations(ctx context.Context, ids []string) (map[string]bool, error)
	LocationNames(ctx context.Context, ids []string) (map[string]string, error)
	UserNames(ctx context.Context, ids []string) (map[string]string, error)
	CurrentMemberIDs(ctx context.Context, teamID string) ([]string, error)
}

// ServiceInfo is what Changes needs to know about a Service.
type ServiceInfo struct {
	ID            string
	Reference     string
	Name          string
	Status        string
	OwnerUserID   *string
	OwnerTeamID   *string
	SupportTeamID *string
}

// ImpactNode is a record reached by an impact walk, as the Services impact view
// shows it (hidden records carry placeholder ids).
type ImpactNode struct {
	Type        string
	ID          string
	Name        *string
	Reference   *string
	Status      *string
	Criticality *string
	Missing     bool
	Hidden      bool
	Depth       int
	Confidence  string
}

// Impact is the downstream walk from one affected resource.
type Impact struct {
	Type         string
	ID           string
	Name         *string
	Reference    *string
	Nodes        []ImpactNode
	Truncated    bool
	DepthLimited bool
	NodeLimited  bool
}

// Services is what Changes asks the Services module (public contract).
type Services interface {
	Lookup(ctx context.Context, ids []string) (map[string]ServiceInfo, error)
	// Impact walks downstream from a record the caller may see; ErrNotFound for
	// an unknown record. ServicesView/InfraView/AssetsView decide the redaction.
	Impact(ctx context.Context, p Principal, nodeType, id string, depth int) (Impact, error)
}

// VMInfo is what Changes needs to know about a Virtual Machine.
type VMInfo struct {
	ID    string
	Name  string
	State string
}

// Infrastructure is what Changes asks the Infrastructure module (public contract).
type Infrastructure interface {
	VMs(ctx context.Context, ids []string) (map[string]VMInfo, error)
}

// AssetInfo is what Changes needs to know about an Asset.
type AssetInfo struct {
	ID        string
	Reference string
	Status    string
}

// Usable reports whether an Asset may be affected: disposed, lost and retired assets are gone.
func (a AssetInfo) Usable() bool {
	switch a.Status {
	case "disposed", "lost", "retired":
		return false
	}
	return true
}

// Assets is what Changes asks the Assets module (public contract).
type Assets interface {
	Assets(ctx context.Context, ids []string) (map[string]AssetInfo, error)
}

// ApprovalRequest describes the approval step requested for a Change.
type ApprovalRequest struct {
	SubjectID       string
	Label           string
	ApproverUserID  *string
	ApproverTeamID  *string
	ExcludedUserIDs []string
}

// ApprovalInfo is the state of one approval step.
type ApprovalInfo struct {
	ID              string
	StepIndex       int
	Status          string
	ApproverUserID  *string
	ApproverTeamID  *string
	DecidedByUserID *string
	DecidedAt       *time.Time
}

// Approvals is the Approvals contract (approvals/public adapter).
type Approvals interface {
	// RequestInTx creates a pending approval and returns its id; ErrNoEligibleApprover when nobody could decide it.
	RequestInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID string, r ApprovalRequest) (string, error)
	CancelBySubjectInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, subjectID string) error
	ForSubject(ctx context.Context, subjectID string) ([]ApprovalInfo, error)
	// CanView reports whether the User is or was an approver of the Change.
	CanView(ctx context.Context, subjectID, userID string) (bool, error)
}

// TaskInput describes an execution Task.
type TaskInput struct {
	ChangeID       string
	Title          string
	Description    string
	DueAt          *time.Time
	AssignedUserID *string
	AssignedTeamID *string
}

// TaskInfo is the read view of an execution Task.
type TaskInfo struct {
	ID     string
	Title  string
	Status string
	DueAt  *time.Time
}

// Task statuses that count as finished.
func taskFinished(status string) bool { return status == "completed" || status == "cancelled" }

// Tasks is the Tasks contract (tasks/public adapter). Tasks of a Change carry
// the context type "change".
type Tasks interface {
	CreateInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID string, in TaskInput) (string, error)
	CancelByContextInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, changeID, reason string) (int, error)
	StatusesInTx(ctx context.Context, tx pgx.Tx, ids []string) (map[string]string, error)
	Tasks(ctx context.Context, ids []string) ([]TaskInfo, error)
}

// PermissionResolver returns a User's effective permissions
// (platform/authorization/roles.Evaluator).
type PermissionResolver interface {
	Permissions(ctx context.Context, userID string) (map[string]struct{}, error)
}

// ApprovalViewer reports whether a User is or was an approver of a Change.
type ApprovalViewer interface {
	CanView(ctx context.Context, subjectID, userID string) (bool, error)
}

// Notifier creates notifications inside the caller's transaction.
type Notifier interface {
	Create(ctx context.Context, tx pgx.Tx, in notifications.Intent) (bool, error)
}
