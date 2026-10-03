package main

import (
	"context"
	"errors"
	"testing"

	knowledgeapp "github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/application"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	tasksapp "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	tasksrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

func TestRunbookExecutionCreatesTasksAndCompletesWithThem(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	svc := wiring.Runbooks(w.pool)
	tasks := tasksapp.NewService(tasksrepository.New(w.pool), orgpublic.NewWorkDirectory(orgrepository.New(w.pool)), nil)
	t.Cleanup(func() {
		_, _ = w.pool.Exec(ctx, `DELETE FROM platform.tasks WHERE context_type = 'runbook_execution' AND context_id IN (SELECT id FROM knowledge.runbook_executions WHERE started_by = $1::uuid)`, w.assignee)
		_, _ = w.pool.Exec(ctx, `DELETE FROM knowledge.runbook_executions WHERE started_by = $1::uuid`, w.assignee)
		_, _ = w.pool.Exec(ctx, `DELETE FROM knowledge.runbooks WHERE created_by = $1::uuid`, w.assignee)
		_, _ = w.pool.Exec(ctx, `DELETE FROM servicedesk.tickets WHERE reporter_user_id = $1::uuid`, w.creator)
	})
	c := func(u string) knowledgeapp.Caller {
		return knowledgeapp.Caller{Actor: audit.UserActor(u), CorrelationID: w.corr}
	}
	author := knowledgeapp.RunbookPrincipal{UserID: w.assignee, Manage: true, Execute: true}
	reader := knowledgeapp.RunbookPrincipal{UserID: w.member, View: true}

	var inv *knowledgeapp.InvalidInputError
	if _, err := svc.Create(ctx, c(w.assignee), author, knowledgeapp.RunbookInput{Title: "Empty"}); !errors.As(err, &inv) {
		t.Errorf("a runbook needs steps: %v", err)
	}
	if _, err := svc.Create(ctx, c(w.assignee), author, knowledgeapp.RunbookInput{Title: "Bad team", Steps: []knowledgeapp.Step{{Title: "x", TeamID: "00000000-0000-7000-8000-000000000001"}}}); !errors.As(err, &inv) {
		t.Errorf("an unknown team: %v", err)
	}
	if _, err := svc.Create(ctx, c(w.member), reader, knowledgeapp.RunbookInput{Title: "x", Steps: []knowledgeapp.Step{{Title: "y"}}}); !errors.Is(err, knowledgeapp.ErrForbidden) {
		t.Errorf("create without knowledge.manage: %v", err)
	}
	rb, err := svc.Create(ctx, c(w.assignee), author, knowledgeapp.RunbookInput{Title: "Leaver offboarding", Description: "Standard steps", Steps: []knowledgeapp.Step{
		{Title: "Disable the account", TeamID: w.team}, {Title: "Collect the hardware"}, {Title: "Archive the mailbox", Description: "Keep for 90 days"},
	}})
	if err != nil || len(rb.Steps) != 3 || !rb.Active {
		t.Fatalf("create = %+v %v", rb, err)
	}
	if _, err := svc.Get(ctx, knowledgeapp.RunbookPrincipal{UserID: w.outsider}, rb.ID); !errors.Is(err, knowledgeapp.ErrNotFound) {
		t.Errorf("a stranger reads a runbook: %v", err)
	}
	if _, err := svc.Start(ctx, c(w.member), reader, rb.ID, ""); !errors.Is(err, knowledgeapp.ErrForbidden) {
		t.Errorf("start without runbooks.execute: %v", err)
	}

	// A ticket as the context must exist.
	sd := wiring.ServiceDesk(w.pool)
	tk, err := sd.Create(ctx, servicedeskapp.Caller{Actor: audit.UserActor(w.creator), CorrelationID: w.corr}, servicedeskapp.Principal{UserID: w.creator}, servicedeskapp.CreateInput{Title: "Leaver Anna"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(ctx, c(w.assignee), author, rb.ID, "00000000-0000-7000-8000-000000000001"); !errors.As(err, &inv) {
		t.Errorf("unknown ticket: %v", err)
	}
	ex, err := svc.Start(ctx, c(w.assignee), author, rb.ID, tk.ID)
	if err != nil || ex.Status != "running" || len(ex.TaskIDs) != 3 || ex.ContextID == nil {
		t.Fatalf("start = %+v %v", ex, err)
	}
	// The first step's task belongs to the team; the others stay unassigned.
	var team *string
	_ = w.pool.QueryRow(ctx, `SELECT assigned_team_id::text FROM platform.tasks WHERE id = $1::uuid`, ex.TaskIDs[0]).Scan(&team)
	if team == nil || *team != w.team {
		t.Errorf("first task team = %v", team)
	}
	// Finishing the tasks completes the execution after the last one, not before.
	finish := func(id string) {
		if _, err := tasks.Transition(ctx, tasksapp.Caller{Actor: audit.UserActor(w.assignee), CorrelationID: w.corr}, tasksapp.Principal{UserID: w.assignee, Manage: true}, id, nil, tasksapp.OpComplete, ""); err != nil {
			t.Fatal(err)
		}
	}
	finish(ex.TaskIDs[0])
	finish(ex.TaskIDs[1])
	w.dispatch()
	if got, _ := svc.GetExecution(ctx, author, ex.ID); got.Status != "running" {
		t.Fatalf("status after two of three tasks = %s", got.Status)
	}
	finish(ex.TaskIDs[2])
	w.dispatch()
	if got, _ := svc.GetExecution(ctx, author, ex.ID); got.Status != "completed" || got.FinishedAt == nil {
		t.Fatalf("status after all tasks = %+v", got)
	}
	if _, err := svc.Cancel(ctx, c(w.assignee), author, ex.ID, "oops"); err == nil {
		t.Error("a completed execution was cancelled")
	}

	// Cancelling a running execution cancels its open tasks.
	ex2, err := svc.Start(ctx, c(w.assignee), author, rb.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Cancel(ctx, c(w.assignee), author, ex2.ID, ""); !errors.As(err, &inv) {
		t.Errorf("cancel needs a reason: %v", err)
	}
	got, err := svc.Cancel(ctx, c(w.assignee), author, ex2.ID, "wrong person")
	if err != nil || got.Status != "cancelled" {
		t.Fatalf("cancel = %+v %v", got, err)
	}
	w.dispatch()
	var open int
	_ = w.pool.QueryRow(ctx, `SELECT count(*) FROM platform.tasks WHERE id = ANY($1::uuid[]) AND status NOT IN ('cancelled')`, ex2.TaskIDs).Scan(&open)
	if open != 0 {
		t.Errorf("%d tasks of a cancelled execution are still open", open)
	}
	// A deactivated runbook cannot start; running executions keep their snapshot.
	if _, err := svc.SetActive(ctx, c(w.assignee), author, rb.ID, nil, false); err != nil {
		t.Fatal(err)
	}
	var tr *knowledgeapp.InvalidTransitionError
	if _, err := svc.Start(ctx, c(w.assignee), author, rb.ID, ""); !errors.As(err, &tr) {
		t.Errorf("start of an inactive runbook: %v", err)
	}
	if w.pendingEvents() != 0 {
		t.Errorf("%d events left unprocessed", w.pendingEvents())
	}
}
