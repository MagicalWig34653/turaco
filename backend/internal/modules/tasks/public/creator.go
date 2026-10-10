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
	// ResultNote is the closing note of a completed task; only set when it was marked for the requester.
	ResultNote *string
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
var contextIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Creator creates and reads tasks for other modules.
type Creator struct {
	store   application.Store
	dir     application.Directory
	allowed map[string]bool
}

func (c *Creator) owns(typ string) bool { return contextType.MatchString(typ) && c.allowed[typ] }

// ContextReader is the Tasks-owned read port for a bounded set of tasks attached
// to another module's record. The caller must authorize that record and any
// assignee details before returning the result to a user.
type ContextReader interface {
	ListByContext(ctx context.Context, contextType, contextID string, limit int) ([]application.Task, error)
	ListByContextTx(ctx context.Context, tx pgx.Tx, contextType, contextID string, limit int) ([]application.Task, error)
	SummaryByContexts(ctx context.Context, contextType string, contextIDs []string) (ContextSummary, error)
	SummaryByType(ctx context.Context, contextType string) (ContextSummary, error)
}

// SummaryByType is for the owner of a Task context type to summarize its work.
func (c *Creator) SummaryByType(ctx context.Context, typ string) (ContextSummary, error) {
	if !c.owns(typ) {
		return ContextSummary{}, &InvalidInputError{Message: "invalid task context"}
	}
	reader, ok := c.store.(ContextReader)
	if !ok {
		return ContextSummary{}, errors.New("tasks: context reader unavailable")
	}
	return reader.SummaryByType(ctx, typ)
}

// ContextSummary contains only state counts, so a module can aggregate its work
// without reading unrelated Tasks or exposing Task assignees.
type ContextSummary struct {
	Open      int `json:"open"`
	Done      int `json:"done"`
	Cancelled int `json:"cancelled"`
	Overdue   int `json:"overdue"`
}

func (c *Creator) SummaryByContexts(ctx context.Context, typ string, ids []string) (ContextSummary, error) {
	if !c.owns(typ) || len(ids) > 500 {
		return ContextSummary{}, &InvalidInputError{Message: "invalid task context"}
	}
	for _, id := range ids {
		if !contextIDPattern.MatchString(id) {
			return ContextSummary{}, &InvalidInputError{Message: "invalid task context id"}
		}
	}
	if len(ids) == 0 {
		return ContextSummary{}, nil
	}
	reader, ok := c.store.(ContextReader)
	if !ok {
		return ContextSummary{}, errors.New("tasks: context reader unavailable")
	}
	return reader.SummaryByContexts(ctx, typ, ids)
}

// ByContext reads live task state. A limit of 51 lets an owner enforce a cap
// of 50 without reading an unbounded context.
func (c *Creator) ByContext(ctx context.Context, contextType, contextID string, limit int) ([]Task, error) {
	return c.byContext(ctx, nil, contextType, contextID, limit)
}

// ByContextInTx reads a context inside its owner's transaction, so a locked
// parent row serializes creation and the cap check.
func (c *Creator) ByContextInTx(ctx context.Context, tx pgx.Tx, contextType, contextID string, limit int) ([]Task, error) {
	return c.byContext(ctx, tx, contextType, contextID, limit)
}

func (c *Creator) byContext(ctx context.Context, tx pgx.Tx, typ, contextID string, limit int) ([]Task, error) {
	if !c.owns(typ) || !contextIDPattern.MatchString(contextID) || limit < 1 || limit > 500 {
		return nil, &InvalidInputError{Message: "invalid task context or limit"}
	}
	reader, ok := c.store.(ContextReader)
	if !ok {
		return nil, errors.New("tasks: context reader unavailable")
	}
	var ts []application.Task
	var err error
	if tx == nil {
		ts, err = reader.ListByContext(ctx, typ, contextID, limit)
	} else {
		ts, err = reader.ListByContextTx(ctx, tx, typ, contextID, limit)
	}
	if err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(ts))
	for _, t := range ts {
		out = append(out, Task{ID: t.ID, Title: t.Title, Status: t.Status, Priority: t.Priority, DueAt: t.DueAt, AssignedUserID: t.AssignedUserID, AssignedTeamID: t.AssignedTeamID, ResultNote: requesterNote(t)})
	}
	return out, nil
}

func NewCreator(store application.Store, dir application.Directory, allowed ...string) *Creator {
	c := &Creator{store: store, dir: dir, allowed: map[string]bool{}}
	for _, typ := range allowed {
		if contextType.MatchString(typ) {
			c.allowed[typ] = true
		}
	}
	return c
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
	if !c.owns(in.ContextType) || !contextIDPattern.MatchString(in.ContextID) {
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
	if !c.owns(contextType) || !contextIDPattern.MatchString(contextID) {
		return 0, &InvalidInputError{Message: "invalid task context"}
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

// OverdueByContexts counts only open due Tasks in two owned context types.
func (c *Creator) OverdueByTwoTypes(ctx context.Context, typeA string, idsA []string, typeB string, idsB []string) (int, error) {
	if !c.owns(typeA) || !c.owns(typeB) || len(idsA) > 500 || len(idsB) > 500 {
		return 0, &InvalidInputError{Message: "invalid task context"}
	}
	for _, id := range append(append([]string{}, idsA...), idsB...) {
		if !contextIDPattern.MatchString(id) {
			return 0, &InvalidInputError{Message: "invalid task context id"}
		}
	}
	reader, ok := c.store.(interface {
		OverdueByTwoTypes(context.Context, string, []string, string, []string) (int, error)
	})
	if !ok {
		return 0, errors.New("tasks: overdue reader unavailable")
	}
	return reader.OverdueByTwoTypes(ctx, typeA, idsA, typeB, idsB)
}

// requesterNote is the result note only when the completer marked it for the requester.
func requesterNote(t application.Task) *string {
	if t.ResultNoteForRequester {
		return t.ResultNote
	}
	return nil
}
