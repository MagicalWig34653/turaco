package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/timetracking/application"
)

func (e *env) reporter(p application.Principal) application.Principal {
	p.Report = true
	return p
}

func (e *env) log(p application.Principal, ticket string, minutes int) {
	e.t.Helper()
	if _, err := e.svc.Create(context.Background(), e.caller(p), p, application.NewEntry{SubjectType: "ticket", SubjectID: ticket, WorkDate: e.day(0), DurationMinutes: minutes}); err != nil {
		e.t.Fatal(err)
	}
}

func TestReportSuppressesSmallGroupsAndNeverGroupsByPerson(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.log(e.alice, e.ticket, 60)
	lead := e.reporter(e.lead)
	from, to := e.day(-3), e.day(1)

	sum, err := e.svc.Report(ctx, lead, from, to, "queue")
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Groups) != 1 || !sum.Groups[0].Suppressed || sum.Groups[0].Minutes != 0 {
		t.Fatalf("one contributor must be suppressed: %+v", sum.Groups)
	}
	// Per-Ticket groups are operational documentation and exempt.
	sum, err = e.svc.Report(ctx, lead, from, to, "ticket")
	if err != nil || len(sum.Groups) != 1 || sum.Groups[0].Suppressed || sum.Groups[0].Minutes != 60 || sum.Groups[0].Label != "T-1" {
		t.Fatalf("ticket grouping: %+v %v", sum.Groups, err)
	}
	// Enough contributors lift the suppression for queue and month groups.
	e.log(e.bob, e.ticket, 30)
	other := application.Principal{UserID: newID(), Log: true}
	e.tickets.grant(other.UserID, e.ticket, application.TicketAccess{Reference: "T-1", QueueID: e.queue, CanView: true, CanWork: true})
	e.log(other, e.ticket, 30)
	sum, err = e.svc.Report(ctx, lead, from, to, "queue")
	if err != nil || len(sum.Groups) != 1 || sum.Groups[0].Suppressed || sum.Groups[0].Minutes != 120 || sum.Groups[0].Contributors != 3 {
		t.Fatalf("three contributors: %+v %v", sum.Groups, err)
	}
	for _, g := range []string{"user", "person", "assignee", ""} {
		var inv *application.InvalidInputError
		if _, err := e.svc.Report(ctx, lead, from, to, g); !errors.As(err, &inv) {
			t.Errorf("groupBy %q accepted: %v", g, err)
		}
	}
}

func TestReportScopeAndPermission(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.log(e.alice, e.ticket, 45)
	from, to := e.day(-3), e.day(1)
	// Without the report permission nothing is returned.
	if _, err := e.svc.Report(ctx, e.lead, from, to, "ticket"); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("no permission: %v", err)
	}
	// A reporter who views no Queue gets an empty report, and a Ticket outside the viewed Queues is never named.
	stranger := e.reporter(application.Principal{UserID: newID()})
	sum, err := e.svc.Report(ctx, stranger, from, to, "ticket")
	if err != nil || len(sum.Groups) != 0 {
		t.Fatalf("stranger: %+v %v", sum.Groups, err)
	}
	// A reporter viewing another Queue sees nothing of this one.
	e.tickets.grant(stranger.UserID, newID(), application.TicketAccess{Reference: "T-2", QueueID: newID(), CanView: true})
	sum, err = e.svc.Report(ctx, stranger, from, to, "queue")
	if err != nil || len(sum.Groups) != 0 {
		t.Fatalf("other queue: %+v %v", sum.Groups, err)
	}
	// Deleted entries are excluded and periods are bounded.
	var inv *application.InvalidInputError
	if _, err := e.svc.Report(ctx, e.reporter(e.lead), "2024-01-01", "2026-01-01", "queue"); !errors.As(err, &inv) {
		t.Fatalf("long period: %v", err)
	}
}

func TestReportCoversWholeMonthsSoPeriodsCannotBeDifferenced(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.log(e.alice, e.ticket, 60)
	lead := e.reporter(e.lead)
	day := e.day(0)
	a, err := e.svc.Report(ctx, lead, day, day, "queue")
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.svc.Report(ctx, lead, day[:8]+"01", day, "queue")
	if err != nil {
		t.Fatal(err)
	}
	if a.From != day[:8]+"01" || a.From != b.From || a.To != b.To {
		t.Fatalf("periods are not normalized to whole months: %+v %+v", a, b)
	}
	if len(a.Groups) != 1 || !a.Groups[0].Suppressed || len(b.Groups) != 1 || !b.Groups[0].Suppressed {
		t.Fatalf("single-contributor month must stay suppressed: %+v %+v", a.Groups, b.Groups)
	}
}
