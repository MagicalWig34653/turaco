package repository_test

import (
	"context"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
)

func TestTicketFactsCarryAssignee(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tk := e.raise()
	repo := repository.New(e.pool)
	facts, ok, err := repo.TicketFacts(ctx, tk.ID)
	if err != nil || !ok || facts.AssigneeUserID != "" || facts.ReporterUserID != e.alice {
		t.Fatalf("unassigned: %+v %v %v", facts, ok, err)
	}
	if _, err := e.svc.Assign(ctx, e.c(e.agent), e.staff(), tk.ID, nil, &e.agent, nil); err != nil {
		t.Fatal(err)
	}
	if facts, _, _ = repo.TicketFacts(ctx, tk.ID); facts.AssigneeUserID != e.agent {
		t.Fatalf("assignee: %+v", facts)
	}
}
