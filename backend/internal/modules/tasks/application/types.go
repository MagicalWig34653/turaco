// Package application holds the Task use cases, the lifecycle rules and the
// ports they depend on. Tasks are the shared work model of the platform
// (docs/domain/state-machines.md, "Task").
package application

import (
	"errors"
	"fmt"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Task statuses.
const (
	StatusOpen       = "open"
	StatusInProgress = "in_progress"
	StatusBlocked    = "blocked"
	StatusCompleted  = "completed"
	StatusCancelled  = "cancelled"
)

// Task priorities, lowest first.
const (
	PriorityLow    = "low"
	PriorityNormal = "normal"
	PriorityHigh   = "high"
	PriorityUrgent = "urgent"
)

// TaskPermissions are the permissions that let a User see at least some
// tasks; the transport routes and the notification recipients rely on the
// same list.
var TaskPermissions = []string{"tasks.view", "tasks.manage", "tasks.work"}

var (
	statuses   = []string{StatusOpen, StatusInProgress, StatusBlocked, StatusCompleted, StatusCancelled}
	priorities = []string{PriorityLow, PriorityNormal, PriorityHigh, PriorityUrgent}
)

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// Task is the persisted state of one task.
type Task struct {
	ID          string
	Title       string
	Description *string
	Status      string
	// StatusReason is set exactly while the task is blocked or cancelled.
	StatusReason      *string
	Priority          string
	AssignedUserID    *string
	AssignedTeamID    *string
	ContextType       *string
	ContextID         *string
	DueAt             *time.Time
	CompletedAt       *time.Time
	CreatedByUserID   *string
	CompletedByUserID *string
	// RecurrenceDefinitionID and ScheduledFor are set on tasks generated from a
	// Recurring Task Definition; the definition may have been deleted since.
	RecurrenceDefinitionID *string
	ScheduledFor           *time.Time
	// ResultNote is the closing comment left when the task was completed; nil otherwise.
	ResultNote *string
	// Version starts at 1 and increases with every change.
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Terminal reports whether the task is completed or cancelled.
func (t Task) Terminal() bool { return t.Status == StatusCompleted || t.Status == StatusCancelled }

// TaskView is a Task with the display names of its assignees.
type TaskView struct {
	Task
	AssignedUserName *string
	AssignedTeamName *string
}

// Principal is the caller's task-relevant authority, derived from its
// permissions by the transport layer.
type Principal struct {
	UserID string
	// ViewAll (tasks.view) sees every task.
	ViewAll bool
	// Manage (tasks.manage) creates, edits, assigns, cancels and reopens every
	// task and works on every task it can see; it implies seeing all tasks.
	Manage bool
	// Work (tasks.work) sees and works tasks assigned to the caller or to one
	// of the caller's Teams.
	Work bool
	// RecurrenceManage (tasks.recurrence.manage) manages Recurring Task Definitions.
	RecurrenceManage bool
	// BoardsManageTeam (tasks.boards.manage_team) creates and edits Team-owned Task Boards.
	BoardsManageTeam bool
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
		return errors.New("tasks: correlation id is required")
	}
	return nil
}

var (
	// ErrNotFound hides tasks the caller may not see as well as missing ones.
	ErrNotFound = errors.New("tasks: not found")
	// ErrForbidden means the caller sees the task but may not perform the operation.
	ErrForbidden = errors.New("tasks: forbidden")
	// ErrVersionConflict means the task changed since the caller read it.
	ErrVersionConflict = errors.New("tasks: version conflict")
	// ErrAssigneeInvalid means an assigned User or Team does not exist or is not active.
	ErrAssigneeInvalid = errors.New("tasks: assignee is not an active user or team")
	// ErrInvalidCursor is returned for a malformed pagination cursor.
	ErrInvalidCursor = errors.New("tasks: invalid cursor")
)

// InvalidTransitionError reports an operation the task's status does not allow.
type InvalidTransitionError struct {
	Operation string
	From      string
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("tasks: operation %s is not allowed in status %s", e.Operation, e.From)
}

// InvalidInputError carries a user-safe validation message.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return "tasks: invalid input: " + e.Message }

func invalid(format string, args ...any) error {
	return &InvalidInputError{Message: fmt.Sprintf(format, args...)}
}

const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// Page is cursor pagination in the shared task order (see ListQuery).
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
