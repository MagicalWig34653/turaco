package repository_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	servicedeskpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/views"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

const slaStaff = "00000000-0000-7000-8000-000000000002"

// slaListFixture is a Queue with one ticket per SLA state. The policy has a 100 minute continuous resolution target
// with the warning at 80 percent.
type slaListFixture struct {
	*sweepFixture
	onTrack, atRisk, breached, met, exempt string
}

func newSLAListFixture(t *testing.T) *slaListFixture {
	t.Helper()
	f := &slaListFixture{sweepFixture: newSweepFixture(t, false, nil)}
	now := time.Now().UTC().Truncate(time.Microsecond)
	f.onTrack = f.ticket(now.Add(-10 * time.Minute))
	f.atRisk = f.ticket(now.Add(-85 * time.Minute))
	f.breached = f.ticket(now.Add(-120 * time.Minute)) // overdue; the sweep has not recorded it yet
	f.met = f.ticket(now.Add(-60 * time.Minute))
	f.apply(f.met, "resolve", "", now.Add(-30*time.Minute))
	if _, err := f.pool.Exec(context.Background(), `UPDATE servicedesk.tickets SET status='resolved', resolved_at=$2 WHERE id=$1::uuid`, f.met, now.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	f.exempt = f.ticket(now.Add(-20 * time.Minute))
	f.apply(f.exempt, "exempt", "test_ticket", now.Add(-15*time.Minute))
	return f
}

func (f *slaListFixture) svc(on bool) *application.Service {
	return wiring.ServiceDesk(f.pool).WithSLA(func(context.Context) (bool, error) { return on, nil })
}

func staffPrincipal() application.Principal {
	return application.Principal{UserID: slaStaff, View: true, Manage: true}
}

func (f *slaListFixture) run(svc *application.Service, p application.Principal, conds ...query.Node) (query.Page[application.Ticket], error) {
	conds = append([]query.Node{query.Cond("queue", query.OpEquals, f.queueID)}, conds...)
	return svc.QueryScoped(context.Background(), p, query.Request{Limit: 100, Filter: qFilter(conds...)}, application.ScopeAll, nil, "")
}

func TestSLAStateFilterAndRowSummary(t *testing.T) {
	f := newSLAListFixture(t)
	svc := f.svc(true)
	want := map[string]string{f.onTrack: "on_track", f.atRisk: "at_risk", f.breached: "breached", f.met: "met", f.exempt: "none"}
	page, err := f.run(svc, staffPrincipal())
	if err != nil || len(page.Items) != 5 {
		t.Fatalf("list: %d rows, %v", len(page.Items), err)
	}
	for _, tk := range page.Items {
		if tk.SLA == nil || tk.SLA.State != want[tk.ID] {
			t.Errorf("ticket %s: summary %+v, want state %s", tk.ID, tk.SLA, want[tk.ID])
		}
		if tk.SLA != nil && (tk.SLA.State == "met" || tk.SLA.State == "none") && tk.SLA.ResolutionDueAt != nil {
			t.Errorf("ticket %s: due time %v in state %s", tk.ID, tk.SLA.ResolutionDueAt, tk.SLA.State)
		}
	}
	for state, id := range map[string]string{"on_track": f.onTrack, "at_risk": f.atRisk, "breached": f.breached, "met": f.met, "none": f.exempt} {
		page, err := f.run(svc, staffPrincipal(), query.Cond("sla_state", query.OpEquals, state))
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != id {
			t.Errorf("sla_state = %s: %v %v", state, qIDs(page), err)
		}
	}
	page, err = f.run(svc, staffPrincipal(), query.Cond("sla_state", query.OpIn, []string{"at_risk", "breached"}))
	if err != nil || len(page.Items) != 2 {
		t.Errorf("at risk or breached: %v %v", qIDs(page), err)
	}
	// The boolean and the outcome fields agree with the state.
	for name, cond := range map[string]query.Node{
		"sla_breached true":  query.Cond("sla_breached", query.OpIsTrue, nil),
		"outcome met":        query.Cond("sla_resolution_outcome", query.OpEquals, "met"),
		"response outcome":   query.Cond("sla_response_outcome", query.OpEquals, "not_measured"),
		"overdue by due_at":  query.Cond("sla_resolution_due_at", query.OpBefore, time.Now().UTC().Format(time.RFC3339)),
		"due later than now": query.Cond("sla_resolution_due_at", query.OpAfter, time.Now().UTC().Format(time.RFC3339)),
	} {
		page, err := f.run(svc, staffPrincipal(), cond)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := qIDs(page)
		switch name {
		case "sla_breached true":
			if len(got) != 1 || !got[f.breached] {
				t.Errorf("%s: %v", name, got)
			}
		case "outcome met":
			if len(got) != 1 || !got[f.met] {
				t.Errorf("%s: %v", name, got)
			}
		case "overdue by due_at":
			if len(got) != 1 || !got[f.breached] {
				t.Errorf("%s: %v", name, got)
			}
		case "due later than now":
			if len(got) != 2 || !got[f.onTrack] || !got[f.atRisk] {
				t.Errorf("%s: %v", name, got)
			}
		}
	}
	// Sorting by the due time puts the most urgent running clock first and tickets without a clock last.
	page, err = svc.QueryScoped(context.Background(), staffPrincipal(), query.Request{Limit: 100,
		Filter: &query.Filter{V: 1, Root: &query.Node{Type: "group", Logic: "and", Children: []query.Node{query.Cond("queue", query.OpEquals, f.queueID)}},
			Sort: []query.SortSpec{{Field: "sla_resolution_due_at", Dir: "asc"}}}}, application.ScopeAll, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, tk := range page.Items {
		order = append(order, tk.ID)
	}
	if !slices.Equal(order[:3], []string{f.breached, f.atRisk, f.onTrack}) {
		t.Errorf("due order: %v", order)
	}
}

func TestSLAFieldsAreStaffOnlyAndAbsentWhileTheSwitchIsOff(t *testing.T) {
	f := newSLAListFixture(t)
	hasSLA := func(info query.Info) bool {
		for _, fld := range info.Fields {
			if len(fld.Key) > 4 && fld.Key[:4] == "sla_" {
				return true
			}
		}
		return false
	}
	on, off := f.svc(true), f.svc(false)
	info, err := on.QueryFields(context.Background(), staffPrincipal())
	if err != nil || !hasSLA(info) {
		t.Fatalf("staff with the switch on: %v %v", hasSLA(info), err)
	}
	if info, err = off.QueryFields(context.Background(), staffPrincipal()); err != nil || hasSLA(info) {
		t.Fatalf("staff with the switch off: %v %v", hasSLA(info), err)
	}
	if info, err = on.QueryFields(context.Background(), application.Principal{UserID: sweepUser}); err != nil || hasSLA(info) {
		t.Fatalf("employee with the switch on: %v %v", hasSLA(info), err)
	}
	// A filter on an SLA field is refused while the switch is off, and the rows carry no summary.
	if _, err := f.run(off, staffPrincipal(), query.Cond("sla_state", query.OpEquals, "at_risk")); err == nil {
		t.Error("sla_state filter accepted while the switch is off")
	}
	page, err := f.run(off, staffPrincipal())
	if err != nil || len(page.Items) != 5 {
		t.Fatalf("list with the switch off: %d %v", len(page.Items), err)
	}
	for _, tk := range page.Items {
		if tk.SLA != nil {
			t.Fatal("summary returned while the switch is off")
		}
	}
	// The reporter of the tickets (sweepUser) may be given queue view access; they still see no SLA on their own tickets.
	owner := application.Principal{UserID: sweepUser, View: true}
	page, err = f.run(on, owner)
	if err != nil || len(page.Items) != 5 {
		t.Fatalf("owner list: %d %v", len(page.Items), err)
	}
	for _, tk := range page.Items {
		if tk.SLA != nil {
			t.Fatal("summary returned to the reporter of the ticket")
		}
	}
	// A summary is also on the detail read for staff (AI tool), but not for the reporter.
	d, err := on.Get(context.Background(), staffPrincipal(), f.atRisk)
	if err != nil || d.Ticket.SLA == nil || d.Ticket.SLA.State != "at_risk" || d.Ticket.SLA.ResolutionDueAt == nil {
		t.Fatalf("detail summary: %+v %v", d.Ticket.SLA, err)
	}
	if d, err = on.Get(context.Background(), owner, f.atRisk); err != nil || d.Ticket.SLA != nil {
		t.Fatalf("detail summary for the reporter: %+v %v", d.Ticket.SLA, err)
	}
}

func TestOverviewCountsAtRiskAndBreachedPerQueueWithoutAssigneeFigures(t *testing.T) {
	f := newSLAListFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE servicedesk.tickets SET assignee_user_id=$1 WHERE id=$2::uuid`, sweepUser, f.atRisk); err != nil {
		t.Fatal(err)
	}
	find := func(o application.Overview) application.OverviewQueue {
		for _, q := range o.Queues {
			if q.Queue.ID == f.queueID {
				return q
			}
		}
		t.Fatal("queue missing from the overview")
		return application.OverviewQueue{}
	}
	o, err := f.svc(true).TicketOverview(ctx, staffPrincipal())
	if err != nil {
		t.Fatal(err)
	}
	q := find(o)
	if !o.SLA || q.Open != 4 || q.AtRisk != 1 || q.Breached != 1 || o.Totals.AtRisk < 1 || o.Totals.Breached < 1 {
		t.Fatalf("counts with SLA: %+v totals %+v", q.OverviewQueueCounts, o.Totals)
	}
	// Next to breach: the at-risk and the on-track ticket (due ahead), the overdue one is a breach, not "next".
	// The shared list is capped (other tests leave tickets with nearer due times in the database), so the order is
	// asserted on the store scoped to this fixture's Queue.
	scoped, err := f.repo.TicketOverview(ctx, application.OverviewQuery{QueueIDs: []string{f.queueID}, AssigneeLimit: 5, OldestLimit: 10, SLA: true})
	if err != nil {
		t.Fatal(err)
	}
	var next []string
	for _, tk := range scoped.NextToBreach {
		next = append(next, tk.ID)
		if tk.SLADueAt == nil || !tk.SLADueAt.After(time.Now().Add(-time.Second)) {
			t.Errorf("next to breach row without a future due time: %+v", tk)
		}
	}
	if !slices.Equal(next, []string{f.atRisk, f.onTrack}) {
		t.Errorf("next to breach: %v", next)
	}
	if o, err = f.svc(false).TicketOverview(ctx, staffPrincipal()); err != nil {
		t.Fatal(err)
	}
	q = find(o)
	if o.SLA || q.AtRisk != 0 || q.Breached != 0 || len(o.NextToBreach) != 0 {
		t.Fatalf("counts without SLA: %+v %v", q.OverviewQueueCounts, o.NextToBreach)
	}
}

func TestSLAReportCountsFinishedClocksForViewableQueuesOnly(t *testing.T) {
	f := newSLAListFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	// A second ticket finished late: breached, resolved after 120 minutes of business (continuous) time.
	late := f.ticket(now.Add(-150 * time.Minute))
	f.apply(late, "resolve", "", now.Add(-30*time.Minute))
	day := func(d time.Time) time.Time { return d.Truncate(24 * time.Hour) }
	from, to := day(now).AddDate(0, 0, -1), day(now).AddDate(0, 0, 1)
	rep, err := f.svc(true).SLAReport(ctx, staffPrincipal(), from, to, "queue", f.queueID)
	if err != nil || len(rep.Groups) != 1 {
		t.Fatalf("report: %+v %v", rep, err)
	}
	g := rep.Groups[0]
	if g.Queue == nil || g.Queue.ID != f.queueID || len(g.Kinds) != 1 || g.Kinds[0].Kind != "resolution" {
		t.Fatalf("group: %+v", g)
	}
	k := g.Kinds[0]
	if k.Measured != 2 || k.Met != 1 || k.Breached != 1 || k.Exempted != 1 || k.MetRate == nil || *k.MetRate != 0.5 || k.MedianSeconds == nil || k.P90Seconds == nil {
		t.Fatalf("figures: %+v", k)
	}
	if *k.MedianSeconds <= 0 || *k.P90Seconds < *k.MedianSeconds {
		t.Fatalf("percentiles: %d %d", *k.MedianSeconds, *k.P90Seconds)
	}
	for _, by := range []string{"priority", "month"} {
		rep, err := f.svc(true).SLAReport(ctx, staffPrincipal(), from, to, by, f.queueID)
		if err != nil || len(rep.Groups) != 1 || rep.Groups[0].Kinds[0].Measured != 2 {
			t.Errorf("groupBy %s: %+v %v", by, rep.Groups, err)
		}
	}
	// Outside the period nothing is reported.
	rep, err = f.svc(true).SLAReport(ctx, staffPrincipal(), from.AddDate(0, 0, -10), from.AddDate(0, 0, -5), "queue", f.queueID)
	if err != nil || len(rep.Groups) != 0 {
		t.Errorf("earlier period: %+v %v", rep.Groups, err)
	}
	// A Queue the caller does not view yields nothing; people without Queue access and the switch are refused.
	other := application.Principal{UserID: slaStaff}
	if _, err := f.svc(true).SLAReport(ctx, other, from, to, "queue", ""); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("caller without queue access: %v", err)
	}
	if _, err := f.svc(false).SLAReport(ctx, staffPrincipal(), from, to, "queue", ""); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("switch off: %v", err)
	}
	// Input limits: no per-person grouping, at most 366 days.
	for name, args := range map[string][3]any{
		"assignee": {from, to, "assignee"},
		"user":     {from, to, "user"},
		"too long": {from, from.AddDate(0, 0, 366), "queue"},
		"reversed": {to, from, "queue"},
	} {
		_, err := f.svc(true).SLAReport(ctx, staffPrincipal(), args[0].(time.Time), args[1].(time.Time), args[2].(string), "")
		var inv *application.InvalidInputError
		if !errors.As(err, &inv) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := f.svc(true).SLAReport(ctx, staffPrincipal(), from, from.AddDate(0, 0, 365), "queue", ""); err != nil {
		t.Errorf("366 days: %v", err)
	}
}

func TestAtRiskSystemViewExistsOnlyWhileTheSwitchIsOn(t *testing.T) {
	f := newSLAListFixture(t)
	caller := views.Caller{UserID: slaStaff, Permissions: map[string]struct{}{"tickets.view": {}, "tickets.manage": {}}}
	keys := func(on bool) []string {
		vs, err := servicedeskpublic.NewSystemViews(f.svc(on)).SystemViews(context.Background(), caller)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, v := range vs {
			out = append(out, v.Key)
		}
		return out
	}
	if !slices.Contains(keys(true), "system:tickets:at-risk") || slices.Contains(keys(false), "system:tickets:at-risk") {
		t.Fatal("the At risk view must exist exactly while the switch is on")
	}
}
