// Package public is the Tasks module's contract for other modules: create
// Tasks that belong to a record of the caller (a typed context), cancel them
// together and read their state, without touching Tasks storage.
package public

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Caller identifies who creates or changes tasks and the request/operation
// they belong to; a system actor creates tasks without a creating User.
type Caller struct {
	Actor         audit.Actor
	CorrelationID string
}

// CreateInput describes a task for a context record.
type CreateInput struct {
	Title       string
	Description string
	Priority    string // empty means normal
	DueAt       *time.Time
	// AssignedUserID and AssignedTeamID must be active; at most both.
	AssignedUserID *string
	AssignedTeamID *string
	// ContextType names the kind of record ("service_request"), ContextID is its id.
	ContextType string
	ContextID   string
}

// Task is the read view other modules get.
type Task struct {
	ID             string
	Title          string
	Status         string
	Priority       string
	DueAt          *time.Time
	AssignedUserID *string
	AssignedTeamID *string
}

// Status values of a task.
const (
	StatusOpen       = application.StatusOpen
	StatusInProgress = application.StatusInProgress
	StatusBlocked    = application.StatusBlocked
	StatusCompleted  = application.StatusCompleted
	StatusCancelled  = application.StatusCancelled
)

// ErrAssigneeInvalid means an assigned User or Team is not active.
var ErrAssigneeInvalid = application.ErrAssigneeInvalid

// InvalidInputError is returned for unusable task input.
type InvalidInputError = application.InvalidInputError

var contextType = regexp.MustCompile(`^[a-z][a-z_]{1,39}$`)

// Creator creates and reads tasks for other modules.
type Creator struct {
	store application.Store
	dir   application.Directory
}

func NewCreator(store application.Store, dir application.Directory) *Creator {
	return &Creator{store: store, dir: dir}
}

// CreateInTx creates an open task in the caller's transaction (the caller
// commits, so the task exists exactly when the caller's change does) and
// returns its id. Assignees are checked for activity before the transaction
// is used.
func (c *Creator) CreateInTx(ctx context.Context, tx pgx.Tx, caller Caller, in CreateInput) (string, error) {
	if err := caller.Actor.Validate(); err != nil {
		return "", err
	}
	if caller.CorrelationID == "" {
		return "", errors.New("tasks: correlation id is required")
	}
	if !contextType.MatchString(in.ContextType) || len(in.ContextID) != 36 {
		return "", &InvalidInputError{Message: "a task context needs a type and an id"}
	}
	n, err := application.NormalizeNewTask(application.TaskDraft{
		Title: in.Title, Description: in.Description, Priority: in.Priority, DueAt: in.DueAt,
		AssignedUserID: in.AssignedUserID, AssignedTeamID: in.AssignedTeamID,
	})
	if err != nil {
		return "", err
	}
	if err := application.CheckAssignees(ctx, c.dir, in.AssignedUserID, in.AssignedTeamID); err != nil {
		return "", err
	}
	if caller.Actor.UserID != "" {
		by := caller.Actor.UserID
		n.CreatedBy = &by
	}
	n.ContextType, n.ContextID = &in.ContextType, &in.ContextID
	t, err := c.store.InsertTx(ctx, tx, application.Caller{Actor: caller.Actor, CorrelationID: caller.CorrelationID}, n)
	if err != nil {
		return "", err
	}
	return t.ID, nil
}

// CancelByContextInTx cancels every unfinished task of a context record in
// the caller's transaction and returns how many were cancelled.
func (c *Creator) CancelByContextInTx(ctx context.Context, tx pgx.Tx, caller Caller, contextType, contextID, reason string) (int, error) {
	if err := caller.Actor.Validate(); err != nil {
		return 0, err
	}
	return c.store.CancelByContextTx(ctx, tx, application.Caller{Actor: caller.Actor, CorrelationID: caller.CorrelationID}, contextType, contextID, reason)
}

// Tasks returns the tasks with the given ids; unknown ids are absent.
func (c *Creator) Tasks(ctx context.Context, ids []string) ([]Task, error) {
	ts, err := c.store.ListByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(ts))
	for _, t := range ts {
		out = append(out, Task{ID: t.ID, Title: t.Title, Status: t.Status, Priority: t.Priority, DueAt: t.DueAt, AssignedUserID: t.AssignedUserID, AssignedTeamID: t.AssignedTeamID})
	}
	return out, nil
}

// StatusesInTx returns id -> status for the given tasks inside the caller's transaction.
func (c *Creator) StatusesInTx(ctx context.Context, tx pgx.Tx, ids []string) (map[string]string, error) {
	return c.store.StatusesTx(ctx, tx, ids)
}
