package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
)

func TestMajorIncidentLifecycleSubscriptionsAndLinking(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	svc := application.NewMajorService(repository.New(e.pool)).WithTicketAccess(e.ticketAccess())
	t.Cleanup(func() {
		_, _ = e.pool.Exec(ctx, `DELETE FROM servicedesk.tickets WHERE reporter_user_id = ANY($1::uuid[])`, []string{e.alice, e.bob})
		_, _ = e.pool.Exec(ctx, `DELETE FROM servicedesk.major_incidents WHERE declared_by = $1::uuid`, e.agent)
	})
	var inv *application.InvalidInputError
	var tr *application.InvalidTransitionError
	if _, err := svc.Declare(ctx, e.c(e.agent), false, "Mail down", "We are looking into it"); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("declare without permission: %v", err)
	}
	if _, err := svc.Declare(ctx, e.c(e.agent), true, "Mail down", " "); !errors.As(err, &inv) {
		t.Errorf("declare without a message: %v", err)
	}
	m, err := svc.Declare(ctx, e.c(e.agent), true, "Mail down", "Nobody can send or receive mail.")
	if err != nil || m.Status != "identified" || m.Reference == "" || !m.Active() {
		t.Fatalf("declare = %+v %v", m, err)
	}
	// Everybody reads it; operations are offered to managers only.
	d, err := svc.Get(ctx, e.bob, false, m.ID)
	if err != nil || len(d.Updates) != 1 || len(d.Operations) != 0 || d.Incident.Subscribed {
		t.Fatalf("employee view = %+v %v", d, err)
	}
	if d, _ := svc.Get(ctx, e.agent, true, m.ID); len(d.Operations) == 0 {
		t.Error("managers get the allowed operations")
	}
	if err := svc.Subscribe(ctx, e.c(e.bob), e.bob, m.ID, true); err != nil {
		t.Fatal(err)
	}
	if d, _ := svc.Get(ctx, e.bob, false, m.ID); !d.Incident.Subscribed {
		t.Error("the subscription must show")
	}
	// A reported ticket is linked and its reporter follows along.
	tk := e.raise()
	if err := svc.LinkTicket(ctx, e.c(e.agent), false, m.ID, tk.ID); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("link without permission: %v", err)
	}
	if err := svc.LinkTicket(ctx, e.c(e.agent), true, m.ID, tk.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.LinkTicket(ctx, e.c(e.agent), true, m.ID, "00000000-0000-7000-8000-000000000001"); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("link an unknown ticket: %v", err)
	}
	if d, _ := svc.Get(ctx, e.alice, false, m.ID); !d.Incident.Subscribed || d.Incident.Tickets != 1 {
		t.Errorf("reporter after linking: %+v", d.Incident)
	}
	// The detail lists the linked tickets the reader may view: the reporter sees theirs, an unrelated employee none
	// (and the count follows the list: no hidden counts).
	if d, err := svc.Get(ctx, e.alice, false, m.ID); err != nil || len(d.Tickets) != 1 || d.Tickets[0].ID != tk.ID {
		t.Errorf("reporter's linked tickets = %+v %v", d.Tickets, err)
	}
	if d, err := svc.Get(ctx, e.bob, false, m.ID); err != nil || len(d.Tickets) != 0 || d.Incident.Tickets != 0 {
		t.Errorf("unrelated employee: tickets %+v total %d %v", d.Tickets, d.Incident.Tickets, err)
	}
	if d, err := svc.Get(ctx, e.agent, true, m.ID); err != nil || len(d.Tickets) != 1 || d.Incident.Tickets != 1 {
		t.Errorf("agent's linked tickets = %+v %v", d.Tickets, err)
	}
	// The list shapes the count by the same rule.
	for who, want := range map[string]int{e.alice: 1, e.bob: 0, e.agent: 1} {
		res, err := svc.List(ctx, who, true, application.Page{})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range res.Items {
			if it.ID == m.ID && it.Tickets != want {
				t.Errorf("list count for %s = %d, want %d", who, it.Tickets, want)
			}
		}
	}
	// Unlinking: permission, unknown ticket, then the ticket leaves the incident (idempotent).
	if err := svc.UnlinkTicket(ctx, e.c(e.agent), false, m.ID, tk.ID); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("unlink without permission: %v", err)
	}
	if err := svc.UnlinkTicket(ctx, e.c(e.agent), true, m.ID, "00000000-0000-7000-8000-000000000001"); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unlink an unknown ticket: %v", err)
	}
	if err := svc.UnlinkTicket(ctx, e.c(e.agent), true, m.ID, tk.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.UnlinkTicket(ctx, e.c(e.agent), true, m.ID, tk.ID); err != nil {
		t.Errorf("second unlink must be a no-op: %v", err)
	}
	if d, _ := svc.Get(ctx, e.agent, true, m.ID); len(d.Tickets) != 0 || d.Incident.Tickets != 0 {
		t.Errorf("after unlink: %+v", d)
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'servicedesk.major_incident.ticket_unlinked'`, e.corr) != 1 {
		t.Error("exactly one unlink audit event")
	}
	if err := svc.LinkTicket(ctx, e.c(e.agent), true, m.ID, tk.ID); err != nil {
		t.Fatal(err)
	}
	linked, err := e.svc.Get(ctx, e.user(), tk.ID)
	if err != nil || linked.Ticket.MajorIncidentID == nil || *linked.Ticket.MajorIncidentID != m.ID {
		t.Errorf("ticket link = %+v %v", linked.Ticket, err)
	}

	// Lifecycle: forward only, resolve needs a message, updates replace the public summary.
	if _, err := svc.Transition(ctx, e.c(e.agent), true, m.ID, nil, "close", ""); !errors.As(err, &tr) {
		t.Errorf("close before resolve: %v", err)
	}
	if _, err := svc.Transition(ctx, e.c(e.agent), true, m.ID, nil, "investigate", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Transition(ctx, e.c(e.agent), true, m.ID, nil, "investigate", ""); !errors.As(err, &tr) {
		t.Errorf("investigate twice: %v", err)
	}
	if _, err := svc.PostUpdate(ctx, e.c(e.agent), true, m.ID, "The mail server disk is full."); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Transition(ctx, e.c(e.agent), true, m.ID, nil, "resolve", ""); !errors.As(err, &inv) {
		t.Errorf("resolve without message: %v", err)
	}
	res, err := svc.Transition(ctx, e.c(e.agent), true, m.ID, nil, "resolve", "Disk space freed, mail flows again.")
	if err != nil || res.Status != "resolved" || res.ResolvedAt == nil || res.Active() {
		t.Fatalf("resolve = %+v %v", res, err)
	}
	if err := svc.Subscribe(ctx, e.c(e.carol()), e.carol(), m.ID, true); !errors.As(err, &tr) {
		t.Errorf("subscribing to a resolved incident: %v", err)
	}
	if err := svc.LinkTicket(ctx, e.c(e.agent), true, m.ID, tk.ID); !errors.As(err, &tr) {
		t.Errorf("linking to a resolved incident: %v", err)
	}
	if _, err := svc.Transition(ctx, e.c(e.agent), true, m.ID, nil, "close", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PostUpdate(ctx, e.c(e.agent), true, m.ID, "late"); !errors.As(err, &tr) {
		t.Errorf("update on a closed incident: %v", err)
	}
	active, err := svc.List(ctx, e.bob, true, application.Page{})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range active.Items {
		if x.ID == m.ID {
			t.Error("a closed incident is not active")
		}
	}
	if all, _ := svc.List(ctx, e.bob, false, application.Page{}); len(all.Items) == 0 {
		t.Error("all incidents include closed ones")
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND metadata::text LIKE '%Disk space freed%'`, e.corr) != 0 {
		t.Error("public messages are not copied into audit")
	}
	if e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'MajorIncidentUpdated'`, e.corr) < 4 {
		t.Error("each progress step must publish MajorIncidentUpdated")
	}
}

func (e *env) carol() string { return e.viewer }

func TestLinkingIsOnceAndNeverMovesATicket(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	svc := application.NewMajorService(repository.New(e.pool)).WithTicketAccess(e.ticketAccess())
	t.Cleanup(func() {
		_, _ = e.pool.Exec(ctx, `DELETE FROM servicedesk.tickets WHERE reporter_user_id = ANY($1::uuid[])`, []string{e.alice, e.bob})
		_, _ = e.pool.Exec(ctx, `DELETE FROM servicedesk.major_incidents WHERE declared_by = $1::uuid`, e.agent)
	})
	one, _ := svc.Declare(ctx, e.c(e.agent), true, "One", "first")
	two, _ := svc.Declare(ctx, e.c(e.agent), true, "Two", "second")
	tk := e.raise()
	if err := svc.LinkTicket(ctx, e.c(e.agent), true, one.ID, tk.ID); err != nil {
		t.Fatal(err)
	}
	before, _ := e.svc.Get(ctx, e.user(), tk.ID)
	if err := svc.LinkTicket(ctx, e.c(e.agent), true, one.ID, tk.ID); err != nil {
		t.Errorf("linking again to the same incident is a no-op: %v", err)
	}
	after, _ := e.svc.Get(ctx, e.user(), tk.ID)
	if after.Ticket.Version != before.Ticket.Version {
		t.Error("a repeated link must not change the ticket")
	}
	if err := svc.LinkTicket(ctx, e.c(e.agent), true, two.ID, tk.ID); err == nil {
		t.Error("a ticket was moved to another incident")
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'servicedesk.major_incident.ticket_linked'`, e.corr) != 1 {
		t.Error("only the first link is audited")
	}
}
