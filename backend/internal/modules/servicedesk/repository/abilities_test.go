package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
)

// The abilities of the ticket detail must agree with what the operations really allow: a work grant in the
// Ticket's Queue (as of a team of infrastructure staff) comments and works the Ticket although the person holds no
// global ticket permission, a view grant reads only, the reporter comments publicly, a stranger sees nothing.
func TestTicketAbilitiesMatchTheOperationsTheBackendAllows(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	q := e.queue(application.QueuePublic)
	e.grant(q, user(e.u1, "work"), user(e.u2, "view"))
	tk := e.raise(e.employee(e.alice), q)

	cases := []struct {
		name string
		p    application.Principal
		want application.Abilities
	}{
		{"reporter", e.employee(e.alice), application.Abilities{Comment: true}},
		{"work grant", e.employee(e.u1), application.Abilities{Comment: true, InternalComment: true, Assign: true, SetPriority: true, Transition: true, MoveQueue: true, MarkDuplicate: true}},
		{"view grant", e.employee(e.u2), application.Abilities{}},
	}
	for _, c := range cases {
		d, err := e.svc.Get(ctx, c.p, tk.ID)
		if err != nil {
			t.Fatalf("%s: get: %v", c.name, err)
		}
		got := d.Abilities
		// The reporter may also cancel before work started, which is a lifecycle operation of the reporter.
		if c.name == "reporter" {
			c.want.Transition = len(d.Allowed) > 0
		}
		if got != c.want {
			t.Errorf("%s: abilities = %+v, want %+v", c.name, got, c.want)
		}
	}
	if _, err := e.svc.Get(ctx, e.employee(e.bob), tk.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("a stranger must not read the ticket: %v", err)
	}

	// Agreement: whatever the abilities promise, the operation accepts; whatever they deny, it refuses.
	for _, c := range cases {
		d, err := e.svc.Get(ctx, c.p, tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, errPublic := e.svc.AddComment(ctx, e.c(c.p.UserID), c.p, tk.ID, "public note", false)
		if (errPublic == nil) != d.Abilities.Comment {
			t.Errorf("%s: public comment ability %v but the operation returned %v", c.name, d.Abilities.Comment, errPublic)
		}
		_, errInternal := e.svc.AddComment(ctx, e.c(c.p.UserID), c.p, tk.ID, "internal note", true)
		if (errInternal == nil) != d.Abilities.InternalComment {
			t.Errorf("%s: internal comment ability %v but the operation returned %v", c.name, d.Abilities.InternalComment, errInternal)
		}
		_, errPrio := e.svc.SetPriority(ctx, e.c(c.p.UserID), c.p, tk.ID, nil, "high")
		if (errPrio == nil) != d.Abilities.SetPriority {
			t.Errorf("%s: priority ability %v but the operation returned %v", c.name, d.Abilities.SetPriority, errPrio)
		}
	}

	// A resolved or closed ticket takes no more comments or assignments, and the abilities say so.
	if _, err := e.svc.Transition(ctx, e.c(e.u1), e.employee(e.u1), tk.ID, nil, application.OpResolve, application.Params{Reason: "fixed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Transition(ctx, e.c(e.alice), e.employee(e.alice), tk.ID, nil, application.OpClose, application.Params{}); err != nil {
		t.Fatal(err)
	}
	d, err := e.svc.Get(ctx, e.employee(e.u1), tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Abilities.Comment || d.Abilities.InternalComment || d.Abilities.Assign || d.Abilities.MarkDuplicate {
		t.Errorf("a closed ticket offers no comment, assignment or duplicate: %+v", d.Abilities)
	}
}

// MoveQueue is offered exactly while the move operation accepts the status.
func TestMoveQueueAbilityFollowsTheMoveStatusGuard(t *testing.T) {
	staff := application.Principal{UserID: "u", View: true, Manage: true}
	for _, st := range application.Statuses {
		got := application.AbilitiesOf(application.Ticket{Status: st}, staff, true).MoveQueue
		want := st != application.StatusResolved && st != application.StatusClosed && st != application.StatusCancelled
		if got != want {
			t.Errorf("status %s: MoveQueue = %v, want %v", st, got, want)
		}
	}
}
