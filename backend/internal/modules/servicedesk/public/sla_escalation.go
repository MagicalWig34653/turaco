package public

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	taskspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// EscalationTasks adapts the Tasks contract: the optional SLA breach Task is an ordinary Task of the Queue's
// default Team whose context is the Ticket (context type "ticket", like a scheduled callback).
type EscalationTasks struct{ c *taskspublic.Creator }

// NewEscalationTasks wraps a Tasks creator that owns the context type application.TaskContextType.
func NewEscalationTasks(c *taskspublic.Creator) *EscalationTasks { return &EscalationTasks{c: c} }

var _ application.EscalationTasks = (*EscalationTasks)(nil)

// CreateInTx creates the Task in the caller's transaction.
func (x *EscalationTasks) CreateInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID string, in application.EscalationTaskInput) (string, error) {
	due, team := in.DueAt, in.AssignedTeamID
	id, err := x.c.CreateInTx(ctx, tx, taskspublic.Caller{Actor: actor, CorrelationID: correlationID}, taskspublic.CreateInput{
		Title: in.Title, DueAt: &due, AssignedTeamID: &team, ContextType: application.TaskContextType, ContextID: in.TicketID, LocationID: in.LocationID,
	})
	if errors.Is(err, taskspublic.ErrAssigneeInvalid) {
		return "", application.ErrAssigneeInvalid
	}
	return id, err
}
