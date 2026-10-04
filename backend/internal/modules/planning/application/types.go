// Package application holds the Planning use cases: the Initiative lifecycle
// (idea to completed), its approval through Approvals, its Milestones, the
// Changes, Tasks, Procurement Requests and Services it includes as platform
// Relationships ("initiative INCLUDES ...") and the maintenance calendar read
// model over Changes (docs/product/f7-infrastructure-change-design.md, slice 4).
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

// Initiative statuses (docs/domain/state-machines.md#initiative).
const (
	StatusIdea      = "idea"
	StatusPlanning  = "planning"
	StatusProposed  = "proposed"
	StatusApproved  = "approved"
	StatusActive    = "active"
	StatusOnHold    = "on_hold"
	StatusCompleted = "completed"
	StatusCancelled = "cancelled"
)

// Statuses lists every status.
var Statuses = []string{StatusIdea, StatusPlanning, StatusProposed, StatusApproved, StatusActive, StatusOnHold, StatusCompleted, StatusCancelled}

// editableStatuses are the statuses in which details, Milestones and included
// items can change: not while a proposal waits for its decision (the approver
// decides what was proposed) and never after the Initiative ended.
var editableStatuses = []string{StatusIdea, StatusPlanning, StatusApproved, StatusActive, StatusOnHold}

var (
	// HoldReasons are the reason codes Hold accepts.
	HoldReasons = []string{"blocked_dependency", "resource_shortage", "budget", "reprioritized", "other"}
	// CancelReasons are the reason codes Cancel accepts.
	CancelReasons = []string{"no_longer_needed", "superseded", "budget", "reprioritized", "error_correction", "other"}
	// MilestoneRemoveReasons are the reason codes RemoveMilestone accepts.
	MilestoneRemoveReasons = []string{"no_longer_needed", "merged", "error_correction", "other"}
)

// Reason codes written by the module itself.
const (
	// ReasonApprovalRejected is stored on an Initiative whose proposal was rejected (it returns to planning).
	ReasonApprovalRejected = "approval_rejected"
	reasonRemoved          = "removed"
)

// Permissions of the Planning module (platform/permissions registry).
const (
	PermView   = "planning.view"
	PermManage = "planning.manage"
)

// Relationship names used with platform/relationships.
const (
	NodeInitiative         = "initiative"
	NodeChange             = "change"
	NodeTask               = "task"
	NodeProcurementRequest = "procurement_request"
	NodeService            = "service"
	NodeLocation           = "location"

	RelIncludes = "INCLUDES"

	// RelationshipOwner owns every triple of this module in the relationship registry.
	RelationshipOwner = "planning"
)

// Triples are the relationships this module registers: an Initiative includes
// Changes, Tasks, Procurement Requests and Services (no copies of them).
var Triples = []relationships.Triple{
	{SourceType: NodeInitiative, Type: RelIncludes, TargetType: NodeChange, Owner: RelationshipOwner},
	{SourceType: NodeInitiative, Type: RelIncludes, TargetType: NodeTask, Owner: RelationshipOwner},
	{SourceType: NodeInitiative, Type: RelIncludes, TargetType: NodeProcurementRequest, Owner: RelationshipOwner},
	{SourceType: NodeInitiative, Type: RelIncludes, TargetType: NodeService, Owner: RelationshipOwner},
}

// ItemTypes are the record types an Initiative may include.
var ItemTypes = []string{NodeChange, NodeTask, NodeProcurementRequest, NodeService}

const (
	// SubjectType is the Approval subject type of an Initiative.
	SubjectType = "initiative"
	// EventStatusChanged is published on every status transition of an Initiative.
	EventStatusChanged = "InitiativeStatusChanged"
	// NotificationCategory tells the owner that their Initiative changed status.
	NotificationCategory = "initiative.state"

	DefaultLimit = 50
	MaxLimit     = 200
	// MaxItems bounds the records one Initiative includes.
	MaxItems = 200
	// DefaultItemLimit and MaxItemLimit page the included records.
	DefaultItemLimit = 100
	MaxItemLimit     = 200
	// MaxMilestones bounds the live Milestones of one Initiative.
	MaxMilestones = 50
	// MaxCalendarRange is the longest maintenance calendar range (92 days).
	MaxCalendarRange = 92 * 24 * time.Hour
	// MaxCalendarEntries bounds one maintenance calendar read.
	MaxCalendarEntries = 500
	// MaxDueMilestones bounds one due-milestones read.
	MaxDueMilestones = 200
	maxTitle         = 150
	maxGoal          = 4000
	maxQuery         = 100
	maxPosition      = 10000
)

// Initiative is a medium/long-term modernization effort.
type Initiative struct {
	ID           string
	Reference    string
	Title        string
	Goal         *string
	OwnerID      string
	Status       string
	StatusReason *string
	TargetDate   *time.Time
	ApprovalID   *string
	ProposedBy   *string
	// Editors are the Users who edited the Initiative or its included items; with
	// the proposer and the owner they can never approve it.
	Editors     []string
	CreatedBy   string
	ApprovedAt  *time.Time
	ActivatedAt *time.Time
	ClosedAt    *time.Time
	Version     int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Transition is one entry of an Initiative's append-only state history.
type Transition struct {
	ID            string
	InitiativeID  string
	FromStatus    *string
	ToStatus      string
	Operation     string
	Reason        *string
	ActorUserID   *string
	ActorSystem   *string
	CorrelationID string
	CreatedAt     time.Time
}

// Milestone is a dated entry of an Initiative.
type Milestone struct {
	ID           string
	InitiativeID string
	Title        string
	DueDate      time.Time
	Position     int
	DoneAt       *time.Time
	DoneBy       *string
	RemovedAt    *time.Time
	RemoveReason *string
	RemovedBy    *string
	CreatedBy    *string
	Version      int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Principal is the caller's authority. View/Manage are planning.view and
// planning.manage. ChangesView (changes.view|manage|execute), TasksView
// (tasks.view|manage), ProcurementView (procurement.view|manage), ServicesView
// (services.view|manage) and InfraView (infrastructure.view|manage) decide
// which included and affected records the caller may see.
type Principal struct {
	UserID          string
	View            bool
	Manage          bool
	ChangesView     bool
	TasksView       bool
	ProcurementView bool
	ServicesView    bool
	InfraView       bool
}

// readsAll reports that the caller may read every Initiative.
func (p Principal) readsAll() bool { return p.View || p.Manage }

// hides reports that the caller may not see records of the type.
func (p Principal) hides(nodeType string) bool {
	switch nodeType {
	case NodeChange:
		return !p.ChangesView
	case NodeTask:
		return !p.TasksView
	case NodeProcurementRequest:
		return !p.ProcurementView
	case NodeService:
		return !p.ServicesView
	case NodeLocation:
		return !p.InfraView
	}
	return true
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
		return errors.New("planning: correlation id is required")
	}
	return nil
}

var (
	ErrNotFound           = errors.New("planning: not found")
	ErrForbidden          = errors.New("planning: forbidden")
	ErrInvalidCursor      = errors.New("planning: invalid cursor")
	ErrVersionConflict    = errors.New("planning: version conflict")
	ErrReferenceInvalid   = errors.New("planning: referenced record does not exist or is not usable")
	ErrNoEligibleApprover = errors.New("planning: no eligible approver")
	ErrTooMany            = errors.New("planning: limit reached")
)

// InvalidTransitionError reports an operation the status does not allow.
type InvalidTransitionError struct {
	Operation string
	From      string
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("planning: operation %s is not allowed in status %s", e.Operation, e.From)
}

// InvalidInputError carries a user-safe validation message.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return "planning: invalid input: " + e.Message }

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

// Filter selects Initiatives. OnlyOwnerID restricts the list to the
// Initiatives that User owns (set by the service for callers without
// planning.view|manage).
type Filter struct {
	Status      string
	OwnerID     string
	Query       string
	TargetFrom  *time.Time
	TargetTo    *time.Time
	OnlyOwnerID string
	Page        Page
}

// Store persists Initiatives, their transitions and Milestones. Tx methods run inside InTx.
type Store interface {
	InTx(ctx context.Context, fn func(tx pgx.Tx) error) error
	// Q reads relationships outside a transaction.
	Q() relationships.Querier

	InsertTx(ctx context.Context, tx pgx.Tx, in Initiative) (Initiative, error)
	LockTx(ctx context.Context, tx pgx.Tx, id string) (Initiative, error)
	// UpdateTx writes every mutable column and bumps the version.
	UpdateTx(ctx context.Context, tx pgx.Tx, in Initiative) (Initiative, error)
	Get(ctx context.Context, id string) (Initiative, error)
	ByIDs(ctx context.Context, ids []string) ([]Initiative, error)
	List(ctx context.Context, f Filter) (Result[Initiative], error)

	InsertTransitionTx(ctx context.Context, tx pgx.Tx, t Transition) error
	Transitions(ctx context.Context, initiativeID string, page Page) (Result[Transition], error)

	InsertMilestoneTx(ctx context.Context, tx pgx.Tx, m Milestone) (Milestone, error)
	LockMilestoneTx(ctx context.Context, tx pgx.Tx, initiativeID, id string) (Milestone, error)
	// UpdateMilestoneTx writes every mutable column and bumps the version.
	UpdateMilestoneTx(ctx context.Context, tx pgx.Tx, m Milestone) (Milestone, error)
	// LiveMilestonesTx counts the Milestones that are not removed and returns the highest position.
	LiveMilestonesTx(ctx context.Context, tx pgx.Tx, initiativeID string) (count, maxPosition int, err error)
	// Milestones lists the Milestones that are not removed, by position, due date and id.
	Milestones(ctx context.Context, initiativeID string) ([]Milestone, error)
	// DueMilestones lists open Milestones due in [from, to] of Initiatives in the
	// statuses, by due date and id, at most limit; ownerID restricts them to
	// Initiatives that User owns (empty: all).
	DueMilestones(ctx context.Context, from, to time.Time, statuses []string, ownerID string, limit int) ([]Milestone, error)
}

// Directory is what Planning asks the Organization module.
type Directory interface {
	ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error)
	ActiveTeams(ctx context.Context, ids []string) (map[string]bool, error)
	UserNames(ctx context.Context, ids []string) (map[string]string, error)
	LocationNames(ctx context.Context, ids []string) (map[string]string, error)
}

// ChangeInfo is what Planning knows about a Change.
type ChangeInfo struct {
	ID          string
	Reference   string
	Title       string
	Kind        string
	Risk        string
	Status      string
	RequesterID string
	OwnerID     *string
	WindowStart *time.Time
	WindowEnd   *time.Time
}

// Terminal reports a Change that can no longer be carried out.
func (c ChangeInfo) Terminal() bool {
	return c.Status == "closed" || c.Status == "cancelled" || c.Status == "rejected"
}

// Node is a record a Change affects.
type Node struct {
	Type string
	ID   string
}

// ChangeCalendarEntry is a Change in the maintenance calendar with what it affects.
type ChangeCalendarEntry struct {
	Change   ChangeInfo
	Affected []Node
}

// ErrInvalidRange is the Changes contract's refusal of a calendar range.
var ErrInvalidRange = errors.New("planning: invalid calendar range")

// Changes is the Changes contract (changes/public).
type Changes interface {
	Lookup(ctx context.Context, ids []string) (map[string]ChangeInfo, error)
	// Calendar lists the approved, scheduled and in-progress Changes whose window
	// overlaps [from, to); truncated reports more than limit.
	Calendar(ctx context.Context, from, to time.Time, limit int) (entries []ChangeCalendarEntry, truncated bool, err error)
}

// TaskInfo is what Planning knows about a Task.
type TaskInfo struct {
	ID     string
	Title  string
	Status string
	DueAt  *time.Time
}

// Tasks is the Tasks contract (tasks/public).
type Tasks interface {
	Tasks(ctx context.Context, ids []string) ([]TaskInfo, error)
}

// RequestInfo is what Planning knows about a Procurement Request.
type RequestInfo struct {
	ID        string
	Reference string
	Quantity  int
	Status    string
}

// Procurement is the Procurement contract (procurement/public).
type Procurement interface {
	Requests(ctx context.Context, ids []string) (map[string]RequestInfo, error)
}

// ServiceInfo is what Planning knows about a Service.
type ServiceInfo struct {
	ID          string
	Reference   string
	Name        string
	Status      string
	Criticality string
}

// Services is the Services contract (services/public).
type Services interface {
	Lookup(ctx context.Context, ids []string) (map[string]ServiceInfo, error)
}

// ApprovalRequest describes the approval step requested for an Initiative.
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
	// CanView reports whether the User is or was an approver of the Initiative.
	CanView(ctx context.Context, subjectID, userID string) (bool, error)
}

// Notifier creates notifications inside the caller's transaction.
type Notifier interface {
	Create(ctx context.Context, tx pgx.Tx, in notifications.Intent) (bool, error)
}
