package wiring

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	knowledgeapp "github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/application"
	knowledgerepository "github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/repository"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	servicedeskrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
	taskspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// runbookContextType is the typed task context of runbook executions.
const runbookContextType = "runbook_execution"

type runbookTasks struct{ c *taskspublic.Creator }

func (t runbookTasks) CreateInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID string, in knowledgeapp.TaskInput) (string, error) {
	create := taskspublic.CreateInput{Title: in.Title, Description: in.Description, ContextType: runbookContextType, ContextID: in.ContextID}
	if in.TeamID != "" {
		create.AssignedTeamID = &in.TeamID
	}
	return t.c.CreateInTx(ctx, tx, taskspublic.Caller{Actor: actor, CorrelationID: correlationID}, create)
}

func (t runbookTasks) CancelByContextInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, contextID, reason string) (int, error) {
	return t.c.CancelByContextInTx(ctx, tx, taskspublic.Caller{Actor: actor, CorrelationID: correlationID}, runbookContextType, contextID, reason)
}

func (t runbookTasks) StatusesInTx(ctx context.Context, tx pgx.Tx, ids []string) (map[string]string, error) {
	return t.c.StatusesInTx(ctx, tx, ids)
}

type ticketContexts struct {
	s *servicedeskrepository.Repository
}

func (c ticketContexts) TicketExists(ctx context.Context, id string) (bool, error) {
	_, err := c.s.Get(ctx, id)
	if errors.Is(err, servicedeskapp.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Runbooks builds the Runbook service over the other modules' public contracts.
func Runbooks(pool *pgxpool.Pool) *knowledgeapp.RunbookService {
	dir := orgpublic.NewWorkDirectory(orgrepository.New(pool))
	return knowledgeapp.NewRunbookService(knowledgerepository.New(pool), runbookTasks{taskspublicCreator(pool, dir, runbookContextType)},
		ticketContexts{servicedeskrepository.New(pool)}, dir)
}
