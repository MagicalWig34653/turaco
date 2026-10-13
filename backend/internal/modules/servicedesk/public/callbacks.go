package public

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	taskspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// CallbackTasks adapts the Tasks contract: a scheduled callback is a Task whose context is the Ticket.
type CallbackTasks struct{ c *taskspublic.Creator }

// NewCallbackTasks wraps a Tasks creator that owns the context type application.TaskContextType.
func NewCallbackTasks(c *taskspublic.Creator) *CallbackTasks { return &CallbackTasks{c: c} }

// CreateInTx creates the Task in the caller's transaction.
func (x *CallbackTasks) CreateInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID string, in application.CallbackTaskInput) (string, error) {
	due, assignee := in.DueAt, in.AssignedUserID
	id, err := x.c.CreateInTx(ctx, tx, taskspublic.Caller{Actor: actor, CorrelationID: correlationID}, taskspublic.CreateInput{
		Title: in.Title, DueAt: &due, AssignedUserID: &assignee, ContextType: application.TaskContextType, ContextID: in.TicketID, LocationID: in.LocationID,
	})
	var inv *taskspublic.InvalidInputError
	switch {
	case errors.Is(err, taskspublic.ErrAssigneeInvalid):
		return "", application.ErrAssigneeInvalid
	case errors.As(err, &inv):
		return "", &application.InvalidInputError{Message: inv.Message}
	}
	return id, err
}

func (x *CallbackTasks) ByContext(ctx context.Context, ticketID string, limit int) ([]application.CallbackTask, error) {
	tasks, err := x.c.ByContext(ctx, application.TaskContextType, ticketID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]application.CallbackTask, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, application.CallbackTask{ID: t.ID, Title: t.Title, Status: t.Status, DueAt: t.DueAt, AssignedUserID: t.AssignedUserID})
	}
	return out, nil
}
