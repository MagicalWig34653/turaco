package main

import (
	"context"
	"testing"

	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

func TestTicketNotificationsFollowWhoActed(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	svc := wiring.ServiceDesk(w.pool)
	t.Cleanup(func() {
		_, _ = w.pool.Exec(ctx, `DELETE FROM servicedesk.tickets WHERE reporter_user_id = $1::uuid`, w.creator)
	})
	c := func(u string) servicedeskapp.Caller {
		return servicedeskapp.Caller{Actor: audit.UserActor(u), CorrelationID: w.corr}
	}
	employee := servicedeskapp.Principal{UserID: w.creator}
	staff := servicedeskapp.Principal{UserID: w.assignee, Manage: true, View: true}

	tk, err := svc.Create(ctx, c(w.creator), employee, servicedeskapp.CreateInput{Title: "VPN drops"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Assign(ctx, c(w.member), servicedeskapp.Principal{UserID: w.member, Manage: true}, tk.ID, nil, &w.assignee, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddComment(ctx, c(w.assignee), staff, tk.ID, "Which network are you on?", false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddComment(ctx, c(w.assignee), staff, tk.ID, "Looks like the known client bug", true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddComment(ctx, c(w.creator), employee, tk.ID, "Home wifi", false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Transition(ctx, c(w.assignee), staff, tk.ID, nil, servicedeskapp.OpResolve, servicedeskapp.Params{Reason: "Updated the client"}); err != nil {
		t.Fatal(err)
	}
	w.dispatch()
	if w.notified(w.assignee, "ticket.assigned") != 1 {
		t.Error("the assignee must be told about the assignment")
	}
	if w.notified(w.creator, "ticket.comment") != 1 {
		t.Error("the reporter gets exactly the public staff reply, not the internal note")
	}
	if w.notified(w.assignee, "ticket.comment") != 1 {
		t.Error("the assignee gets the employee's reply")
	}
	if w.notified(w.creator, "ticket.resolved") != 1 {
		t.Error("the reporter must be told when the ticket is resolved")
	}
	if w.notified(w.assignee, "ticket.resolved") != 0 {
		t.Error("the resolver is not notified about their own action")
	}
	if w.pendingEvents() != 0 {
		t.Errorf("%d events left unprocessed", w.pendingEvents())
	}
}

func TestMajorIncidentUpdatesReachSubscribersOnly(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	svc := wiring.MajorIncidents(w.pool)
	t.Cleanup(func() {
		_, _ = w.pool.Exec(ctx, `DELETE FROM servicedesk.major_incidents WHERE declared_by = $1::uuid`, w.assignee)
	})
	c := func(u string) servicedeskapp.Caller {
		return servicedeskapp.Caller{Actor: audit.UserActor(u), CorrelationID: w.corr}
	}
	m, err := svc.Declare(ctx, c(w.assignee), true, "VPN outage", "The VPN gateway is unreachable.")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{w.creator, w.assignee} {
		if err := svc.Subscribe(ctx, c(u), u, m.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.PostUpdate(ctx, c(w.assignee), true, m.ID, "A fix is rolling out."); err != nil {
		t.Fatal(err)
	}
	w.dispatch()
	if w.notified(w.creator, "majorincident.update") != 1 {
		t.Error("the subscriber must hear about the update")
	}
	if w.notified(w.assignee, "majorincident.update") != 0 {
		t.Error("the author is not notified about their own update")
	}
	if w.notified(w.member, "majorincident.update") != 0 {
		t.Error("people who did not subscribe are not notified")
	}
}
