package main

import (
	"context"
	"errors"
	"testing"
	"time"

	changesapp "github.com/MagicalWig34653/turaco/backend/internal/modules/changes/application"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	servicesapp "github.com/MagicalWig34653/turaco/backend/internal/modules/services/application"
	tasksapp "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	tasksrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

// A Change goes through the real Approvals, Tasks and Notifications modules:
// the assessment requests an approval for the chosen approver (never the
// requester or an editor), the decision is turned into the Change's status by
// the outbox consumer, the owners and support Teams of the affected Service are
// told when it is scheduled and the requester and owner when it is decided, and
// execution Tasks block completion until they are finished.
func TestChangeApprovalRoundTripAndExecution(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	chg := wiring.Changes(w.pool)
	svcs := wiring.Services(w.pool)

	sp := servicesapp.Principal{UserID: w.creator, Manage: true, View: true}
	service, err := svcs.Create(ctx, servicesapp.Caller{Actor: audit.UserActor(w.creator), CorrelationID: w.corr}, sp, servicesapp.Input{
		Name: w.corr + " service", OwnerUserID: w.assignee, OwnerTeamID: w.team, Criticality: servicesapp.CriticalityMedium})
	if err != nil {
		t.Fatal(err)
	}
	var changeIDs []string
	t.Cleanup(func() {
		for _, id := range changeIDs {
			_, _ = w.pool.Exec(ctx, `DELETE FROM approvals.approvals WHERE subject_id = $1::uuid`, id)
			_, _ = w.pool.Exec(ctx, `DELETE FROM platform.tasks WHERE context_type = 'change' AND context_id = $1::uuid`, id)
			_, _ = w.pool.Exec(ctx, `DELETE FROM platform.relationships WHERE source_id = $1::uuid`, id)
			_, _ = w.pool.Exec(ctx, `DELETE FROM changes.changes WHERE id = $1::uuid`, id)
		}
		_, _ = w.pool.Exec(ctx, `DELETE FROM platform.relationships WHERE target_id = $1::uuid`, service.ID)
		_, _ = w.pool.Exec(ctx, `DELETE FROM services.services WHERE id = $1::uuid`, service.ID)
	})

	requester := changesapp.Principal{UserID: w.creator, Manage: true, ServicesView: true}
	assessor := changesapp.Principal{UserID: w.outsider, Manage: true, ServicesView: true}
	owner := changesapp.Principal{UserID: w.assignee}
	cc := func(u string) changesapp.Caller {
		return changesapp.Caller{Actor: audit.UserActor(u), CorrelationID: w.corr}
	}
	status := func(id string) string {
		d, err := chg.Get(ctx, requester, id)
		if err != nil {
			t.Fatal(err)
		}
		return d.Change.Status
	}
	window := func() changesapp.Window {
		s, e := time.Now().Add(48*time.Hour), time.Now().Add(50*time.Hour)
		return changesapp.Window{Start: &s, End: &e}
	}
	newChange := func() changesapp.Change {
		c, err := chg.Create(ctx, cc(w.creator), requester, changesapp.NewChange{Title: "Firmware", Kind: changesapp.KindNormal, Risk: changesapp.RiskMedium,
			OwnerUserID: w.assignee, RollbackPlan: "flash previous image", Window: window()})
		if err != nil {
			t.Fatal(err)
		}
		changeIDs = append(changeIDs, c.ID)
		if _, _, err := chg.AddAffected(ctx, cc(w.creator), requester, c.ID, nil, "service", service.ID); err != nil {
			t.Fatal(err)
		}
		if c, err = chg.Submit(ctx, cc(w.creator), requester, c.ID, nil); err != nil {
			t.Fatal(err)
		}
		return c
	}
	assess := func(c changesapp.Change, approver string) {
		t.Helper()
		if _, err := chg.Assess(ctx, cc(w.outsider), assessor, c.ID, nil, changesapp.Assessment{Risk: "medium", Approver: changesapp.Approver{UserID: &approver}}); err != nil {
			t.Fatal(err)
		}
	}
	approvalOf := func(id string) string {
		var a string
		if err := w.pool.QueryRow(ctx, `SELECT id::text FROM approvals.approvals WHERE subject_type = 'change' AND subject_id = $1::uuid ORDER BY created_at DESC LIMIT 1`, id).Scan(&a); err != nil {
			t.Fatal(err)
		}
		return a
	}

	// The requester and the assessor can never be the approver.
	c := newChange()
	if _, err := chg.Assess(ctx, cc(w.outsider), assessor, c.ID, nil, changesapp.Assessment{Risk: "medium", Approver: changesapp.Approver{UserID: &w.creator}}); !errors.Is(err, changesapp.ErrNoEligibleApprover) {
		t.Errorf("requester as approver: %v", err)
	}
	if _, err := chg.Assess(ctx, cc(w.outsider), assessor, c.ID, nil, changesapp.Assessment{Risk: "medium", Approver: changesapp.Approver{UserID: &w.outsider}}); !errors.Is(err, changesapp.ErrNoEligibleApprover) {
		t.Errorf("assessor as approver: %v", err)
	}
	if status(c.ID) != "assessment" {
		t.Fatal("a refused approval request must leave the change in assessment")
	}
	assess(c, w.member)
	if status(c.ID) != "pending_approval" {
		t.Fatalf("status = %s", status(c.ID))
	}
	w.dispatch()
	if w.notified(w.member, "approval.requested") != 1 {
		t.Error("the approver must be notified of the request")
	}
	if err := decideApproval(w, w.creator, approvalOf(c.ID), "approve"); err == nil {
		t.Error("the requester decided their own change")
	}
	if err := decideApproval(w, w.member, approvalOf(c.ID), "approve"); err != nil {
		t.Fatal(err)
	}
	w.dispatch()
	if status(c.ID) != "approved" {
		t.Fatalf("status after approval = %s", status(c.ID))
	}
	if w.notified(w.assignee, "change.state") != 1 {
		t.Errorf("the owner must be told about the approval, got %d", w.notified(w.assignee, "change.state"))
	}
	if w.notified(w.creator, "change.state") != 1 {
		t.Errorf("the requester is the approval's subject, not its actor: got %d", w.notified(w.creator, "change.state"))
	}

	// Scheduling tells the Service's owner and support Team (not the actor, not inactive members).
	if _, err := chg.Schedule(ctx, cc(w.creator), requester, c.ID, nil, changesapp.ScheduleInput{}); err != nil {
		t.Fatal(err)
	}
	w.dispatch()
	for user, want := range map[string]int{w.assignee: 1, w.member: 1, w.creator: 0, w.inactive: 0, w.outsider: 0} {
		if got := w.notified(user, "change.scheduled"); got != want {
			t.Errorf("change.scheduled notifications for user = %d, want %d", got, want)
		}
	}

	// Execution: Tasks block completion until finished.
	if _, err := chg.Start(ctx, cc(w.assignee), owner, c.ID, nil); err != nil {
		t.Fatal(err)
	}
	taskID, err := chg.AddTask(ctx, cc(w.assignee), owner, c.ID, changesapp.NewTask{Title: "Flash firmware"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chg.Complete(ctx, cc(w.assignee), owner, c.ID, nil, ""); !errors.Is(err, changesapp.ErrOpenTasks) {
		t.Fatalf("complete with an open task: %v", err)
	}
	var ctxType string
	if err := w.pool.QueryRow(ctx, `SELECT context_type FROM platform.tasks WHERE id = $1::uuid`, taskID).Scan(&ctxType); err != nil || ctxType != "change" {
		t.Errorf("task context = %q %v", ctxType, err)
	}
	ts := tasksapp.NewService(tasksrepository.New(w.pool), orgpublic.NewWorkDirectory(orgrepository.New(w.pool)), nil)
	if _, err := ts.Transition(ctx, w.caller(w.creator), tasksapp.Principal{UserID: w.creator, Manage: true}, taskID, nil, tasksapp.OpComplete, ""); err != nil {
		t.Fatal(err)
	}
	if done, err := chg.Complete(ctx, cc(w.assignee), owner, c.ID, nil, ""); err != nil || done.Status != "completed" {
		t.Fatalf("complete = %+v %v", done, err)
	}

	// A rejected approval is terminal and tells the requester and owner.
	r := newChange()
	assess(r, w.member)
	if err := decideApproval(w, w.member, approvalOf(r.ID), "reject"); err != nil {
		t.Fatal(err)
	}
	w.dispatch()
	got, err := chg.Get(ctx, requester, r.ID)
	if err != nil || got.Change.Status != "rejected" || got.Change.StatusReason == nil || *got.Change.StatusReason != changesapp.ReasonApprovalRejected {
		t.Fatalf("rejected change = %+v %v", got.Change, err)
	}
	if len(got.Approvals) != 1 || got.Approvals[0].Status != "rejected" {
		t.Errorf("approvals = %+v", got.Approvals)
	}
	if w.notified(w.assignee, "change.state") != 2 {
		t.Errorf("owner state notifications = %d, want 2", w.notified(w.assignee, "change.state"))
	}

	// Cancelling a change with a pending approval cancels the approval, so it can no longer be decided.
	p := newChange()
	assess(p, w.member)
	if _, err := chg.Cancel(ctx, cc(w.creator), requester, p.ID, nil, "superseded"); err != nil {
		t.Fatal(err)
	}
	if err := decideApproval(w, w.member, approvalOf(p.ID), "approve"); err == nil {
		t.Error("a cancelled change's approval could still be decided")
	}
	w.dispatch()
	if status(p.ID) != "cancelled" {
		t.Errorf("status = %s", status(p.ID))
	}
	if w.pendingEvents() != 0 {
		var types []string
		rows, _ := w.pool.Query(ctx, `SELECT event_type || ':' || status FROM platform.outbox_events WHERE correlation_id = $1 AND status <> 'processed'`, w.corr)
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			types = append(types, s)
		}
		rows.Close()
		t.Errorf("%d events left unprocessed: %v", w.pendingEvents(), types)
	}
}
