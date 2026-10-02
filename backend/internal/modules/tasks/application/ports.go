package application

import (
	"context"
	"time"
)

// Event is a domain event recorded in the outbox with a task change.
type Event struct {
	Type    string
	Payload map[string]any
}

// Change is the outcome of a decision made inside the store's transaction:
// the new state plus its audit action and events. NoChange leaves the task
// untouched (no version bump, audit or events).
type Change struct {
	Next     Task
	NoChange bool
	Action   string
	Metadata map[string]any
	Events   []Event
}

// NewTask is the input of Store.Insert. The task starts open at version 1.
type NewTask struct {
	Title          string
	Description    *string
	Priority       string
	DueAt          *time.Time
	AssignedUserID *string
	AssignedTeamID *string
	CreatedBy      *string
}

// ListQuery selects tasks in the shared task order: due date (tasks without
// one last), priority (urgent first), then id. All fields are optional.
type ListQuery struct {
	Statuses       []string
	Priority       string
	AssignedUserID string
	AssignedTeamID string
	// Overdue selects non-terminal tasks whose due date has passed.
	Overdue bool
	// TitlePrefix is a case-insensitive literal prefix.
	TitlePrefix string
	// Mine restricts the result to tasks assigned to UserID or to one of TeamIDs.
	Mine *Mine
	Page Page
}

// Mine is the "assigned to me or my Teams" restriction.
type Mine struct {
	UserID  string
	TeamIDs []string
}

// Store is the persistence port of tasks.
type Store interface {
	Insert(ctx context.Context, c Caller, n NewTask) (Task, error)
	Get(ctx context.Context, id string) (Task, error)
	// Change locks the task row (FOR UPDATE), calls decide with its current
	// state and, unless decide fails or returns NoChange, stores Next with
	// version+1 and records the audit event and outbox events in the same
	// transaction. ErrNotFound for an unknown or malformed id.
	Change(ctx context.Context, c Caller, id string, decide func(current Task) (Change, error)) (Task, error)
	List(ctx context.Context, q ListQuery) (Result[Task], error)
}

// Directory answers the Organization questions tasks need (implemented by
// organization/public.WorkDirectory).
type Directory interface {
	ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error)
	UserNames(ctx context.Context, ids []string) (map[string]string, error)
	ActiveTeams(ctx context.Context, ids []string) (map[string]bool, error)
	TeamNames(ctx context.Context, ids []string) (map[string]string, error)
	CurrentTeamIDs(ctx context.Context, userID string) ([]string, error)
	CurrentMemberIDs(ctx context.Context, teamID string) ([]string, error)
}
