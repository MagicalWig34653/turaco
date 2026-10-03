package repository_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

type dir struct{ active map[string]bool }

func (d dir) pick(ids []string) map[string]bool {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = d.active[id]
	}
	return out
}
func (d dir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	return d.pick(ids), nil
}
func (d dir) ActiveTeams(_ context.Context, ids []string) (map[string]bool, error) {
	return d.pick(ids), nil
}
func (d dir) UserNames(_ context.Context, ids []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (d dir) TeamNames(_ context.Context, ids []string) (map[string]string, error) {
	return map[string]string{}, nil
}

// device knows which asset is held by whom.
type device map[string]string

func (d device) Snapshot(_ context.Context, assetID, holder string) (map[string]any, error) {
	h, ok := d[assetID]
	if !ok || (holder != "" && h != holder) {
		return nil, application.ErrNotFound
	}
	return map[string]any{"reference": "AST-1", "product": "Notebook"}, nil
}

type env struct {
	t                                       *testing.T
	pool                                    *pgxpool.Pool
	svc                                     *application.Service
	corr                                    string
	alice, bob, agent, viewer, team, myBook string
	otherBook                               string
	user, staff, view                       func() application.Principal
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	e := &env{t: t, pool: pool, corr: "tickets-" + hex.EncodeToString(b)}
	for _, dst := range []*string{&e.alice, &e.bob, &e.agent, &e.viewer, &e.team, &e.myBook, &e.otherBook} {
		if err := pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	d := dir{active: map[string]bool{e.alice: true, e.bob: true, e.agent: true, e.viewer: true, e.team: true}}
	e.svc = application.NewService(repository.New(pool), d, device{e.myBook: e.alice, e.otherBook: e.bob})
	e.user = func() application.Principal { return application.Principal{UserID: e.alice} }
	e.staff = func() application.Principal { return application.Principal{UserID: e.agent, Manage: true, View: true} }
	e.view = func() application.Principal { return application.Principal{UserID: e.viewer, View: true} }
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, e.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, e.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM servicedesk.tickets WHERE reporter_user_id = ANY($1::uuid[])`, []string{e.alice, e.bob})
	})
	return e
}

func (e *env) c(user string) application.Caller {
	return application.Caller{Actor: audit.UserActor(user), CorrelationID: e.corr}
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) raise() application.Ticket {
	e.t.Helper()
	tk, err := e.svc.Create(context.Background(), e.c(e.alice), e.user(), application.CreateInput{Title: "Printer jams", Description: "Every page"})
	if err != nil {
		e.t.Fatal(err)
	}
	return tk
}

func (e *env) op(tk application.Ticket, p application.Principal, op, reason string) (application.Ticket, error) {
	return e.svc.Transition(context.Background(), e.c(p.UserID), p, tk.ID, nil, op, application.Params{Reason: reason})
}

func TestEmployeeRaisesAndFollowsATicket(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tk := e.raise()
	if tk.Status != "new" || tk.Priority != "normal" || tk.Reference == "" || tk.AffectedUserID != e.alice || tk.ReporterID != e.alice {
		t.Fatalf("ticket = %+v", tk)
	}
	if e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'TicketCreated'`, e.corr) != 1 {
		t.Error("TicketCreated must be published")
	}
	// An employee cannot set routing fields or report for someone else.
	for name, in := range map[string]application.CreateInput{
		"priority": {Title: "x", Priority: "urgent"},
		"queue":    {Title: "x", QueueTeamID: &e.team},
		"affected": {Title: "x", AffectedUserID: &e.bob},
	} {
		if _, err := e.svc.Create(ctx, e.c(e.alice), e.user(), in); !errors.Is(err, application.ErrForbidden) {
			t.Errorf("%s by an employee: %v", name, err)
		}
	}
	var inv *application.InvalidInputError
	if _, err := e.svc.Create(ctx, e.c(e.alice), e.user(), application.CreateInput{Title: "  "}); !errors.As(err, &inv) {
		t.Errorf("empty title: %v", err)
	}
	// Device: only one the user holds.
	own, err := e.svc.Create(ctx, e.c(e.alice), e.user(), application.CreateInput{Title: "Laptop slow", AssetID: &e.myBook})
	if err != nil || own.DeviceSnapshot["product"] != "Notebook" {
		t.Fatalf("own device = %+v %v", own, err)
	}
	if _, err := e.svc.Create(ctx, e.c(e.alice), e.user(), application.CreateInput{Title: "x", AssetID: &e.otherBook}); !errors.Is(err, application.ErrDeviceInvalid) {
		t.Errorf("someone else's device: %v", err)
	}
	// Staff may report for someone and set routing.
	byStaff, err := e.svc.Create(ctx, e.c(e.agent), e.staff(), application.CreateInput{Title: "Phone call", AffectedUserID: &e.bob, Priority: "high", QueueTeamID: &e.team, AssetID: &e.otherBook})
	if err != nil || byStaff.ReporterID != e.agent || byStaff.AffectedUserID != e.bob || byStaff.Priority != "high" {
		t.Fatalf("by staff = %+v %v", byStaff, err)
	}
	// Visibility: owner and staff see it, strangers get 404; the queue is hidden from the employee.
	if d, err := e.svc.Get(ctx, e.user(), tk.ID); err != nil || len(d.Allowed) == 0 {
		t.Errorf("owner get = %+v %v", d, err)
	}
	if _, err := e.svc.Get(ctx, application.Principal{UserID: e.bob}, tk.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("stranger: %v", err)
	}
	if _, err := e.svc.Get(ctx, application.Principal{UserID: e.bob}, "nope"); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("malformed id: %v", err)
	}
	bobView, err := e.svc.Get(ctx, application.Principal{UserID: e.bob}, byStaff.ID)
	if err != nil || bobView.Ticket.QueueTeamID != nil {
		t.Errorf("affected user's view = %+v %v", bobView.Ticket, err)
	}
	mine, err := e.svc.List(ctx, e.user(), false, application.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mine.Items {
		if m.ReporterID != e.alice && m.AffectedUserID != e.alice {
			t.Errorf("my list leaked %s", m.Reference)
		}
	}
	if _, err := e.svc.List(ctx, e.user(), true, application.Filter{}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("all tickets without tickets.view: %v", err)
	}
	if all, err := e.svc.List(ctx, e.view(), true, application.Filter{Status: "new"}); err != nil || len(all.Items) < 2 {
		t.Errorf("all = %+v %v", all, err)
	}
}

func TestWorkflowAndCommentVisibility(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tk := e.raise()
	var tr *application.InvalidTransitionError
	// Employees cannot work the ticket.
	if _, err := e.op(tk, e.user(), "resolve", "done"); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("resolve by employee: %v", err)
	}
	asg, err := e.svc.Assign(ctx, e.c(e.agent), e.staff(), tk.ID, nil, &e.agent, &e.team)
	if err != nil || asg.Status != "open" || asg.AssigneeID == nil {
		t.Fatalf("assign = %+v %v", asg, err)
	}
	if e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'TicketAssigned'`, e.corr) != 1 {
		t.Error("TicketAssigned must be published")
	}
	if _, err := e.svc.Assign(ctx, e.c(e.agent), e.staff(), tk.ID, nil, &e.alice, nil); err != nil {
		t.Errorf("assigning to another active user: %v", err)
	}
	if _, err := e.svc.Assign(ctx, e.c(e.agent), e.view(), tk.ID, nil, &e.agent, nil); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("assign with tickets.view only: %v", err)
	}
	cur, _ := e.op(tk, e.staff(), "start", "")
	if cur.Status != "in_progress" {
		t.Fatalf("status = %s", cur.Status)
	}
	if _, err := e.op(cur, e.staff(), "wait", "weather"); !errors.As(err, new(*application.InvalidInputError)) {
		t.Errorf("bad waiting reason: %v", err)
	}
	cur, err = e.op(cur, e.staff(), "wait", "customer")
	if err != nil || cur.Status != "waiting" || cur.WaitingReason == nil {
		t.Fatalf("wait = %+v %v", cur, err)
	}
	// Comments: internal ones stay hidden from the employee; a public reply resumes a customer wait.
	if _, err := e.svc.AddComment(ctx, e.c(e.agent), e.staff(), tk.ID, "Please check the cable", false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AddComment(ctx, e.c(e.agent), e.staff(), tk.ID, "Probably the toner again", true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AddComment(ctx, e.c(e.alice), e.user(), tk.ID, "secret", true); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("internal comment by employee: %v", err)
	}
	if _, err := e.svc.AddComment(ctx, e.c(e.viewer), e.view(), tk.ID, "hi", false); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("comment with tickets.view only: %v", err)
	}
	if _, err := e.svc.AddComment(ctx, e.c(e.bob), application.Principal{UserID: e.bob}, tk.ID, "hi", false); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("stranger comment: %v", err)
	}
	if _, err := e.svc.AddComment(ctx, e.c(e.alice), e.user(), tk.ID, "Cable is fine", false); err != nil {
		t.Fatal(err)
	}
	empl, _ := e.svc.Get(ctx, e.user(), tk.ID)
	if len(empl.Comments) != 2 || empl.Ticket.Status != "in_progress" {
		t.Errorf("employee sees %d comments, status %s; want 2 public and a resumed ticket", len(empl.Comments), empl.Ticket.Status)
	}
	for _, c := range empl.Comments {
		if c.Internal {
			t.Error("an internal comment reached the employee")
		}
	}
	staffView, _ := e.svc.Get(ctx, e.view(), tk.ID)
	if len(staffView.Comments) != 3 {
		t.Errorf("staff sees %d comments, want 3", len(staffView.Comments))
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND metadata::text LIKE '%Probably the toner%'`, e.corr) != 0 {
		t.Error("comment text must not be copied into audit")
	}

	latest, _ := e.svc.Get(ctx, e.staff(), tk.ID)
	res, err := e.op(latest.Ticket, e.staff(), "resolve", "Replaced the toner")
	if err != nil || res.Status != "resolved" || res.ResolvedAt == nil || res.Resolution == nil {
		t.Fatalf("resolve = %+v %v", res, err)
	}
	if e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'TicketResolved'`, e.corr) != 1 {
		t.Error("TicketResolved must be published")
	}
	if _, err := e.op(res, e.staff(), "start", ""); !errors.As(err, &tr) {
		t.Errorf("start on a resolved ticket: %v", err)
	}
	// The employee reopens with a reason or confirms by closing.
	if _, err := e.op(res, e.user(), "reopen", ""); !errors.As(err, new(*application.InvalidInputError)) {
		t.Errorf("reopen needs a reason: %v", err)
	}
	re, err := e.op(res, e.user(), "reopen", "Still jams")
	if err != nil || re.Status != "open" || re.ResolvedAt != nil {
		t.Fatalf("reopen = %+v %v", re, err)
	}
	res, _ = e.op(re, e.staff(), "resolve", "Cleaned the rollers")
	closed, err := e.op(res, e.user(), "close", "")
	if err != nil || closed.Status != "closed" || closed.ClosedAt == nil {
		t.Fatalf("close = %+v %v", closed, err)
	}
	if _, err := e.svc.AddComment(ctx, e.c(e.alice), e.user(), tk.ID, "late", false); !errors.As(err, &tr) {
		t.Errorf("comment on a closed ticket: %v", err)
	}
}

func TestCancelRulesAndConcurrency(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tk := e.raise()
	if _, err := e.svc.Transition(ctx, e.c(e.alice), e.user(), tk.ID, nil, "cancel", application.Params{Reason: "solved itself"}); err != nil {
		t.Errorf("owner cancels a new ticket: %v", err)
	}
	t2 := e.raise()
	if _, err := e.op(t2, e.staff(), "start", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.op(t2, e.user(), "cancel", "never mind"); err == nil {
		t.Error("the owner cancelled a ticket that is being worked")
	}
	if _, err := e.svc.Transition(ctx, e.c(e.agent), e.staff(), t2.ID, new(int), "resolve", application.Params{Reason: "x"}); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale version: %v", err)
	}
	if _, err := e.svc.SetPriority(ctx, e.c(e.agent), e.staff(), t2.ID, nil, "urgent"); err != nil {
		t.Error(err)
	}
	if _, err := e.svc.SetPriority(ctx, e.c(e.agent), e.staff(), t2.ID, nil, "meh"); err == nil {
		t.Error("unknown priority accepted")
	}
	for name, sql := range map[string]string{
		"unknown status": `UPDATE servicedesk.tickets SET status = 'lost' WHERE id = $1::uuid`,
		"waiting no why": `UPDATE servicedesk.tickets SET status = 'waiting' WHERE id = $1::uuid`,
		"blank title":    `UPDATE servicedesk.tickets SET title = ' ' WHERE id = $1::uuid`,
		"bad priority":   `UPDATE servicedesk.tickets SET priority = 'x' WHERE id = $1::uuid`,
	} {
		if _, err := e.pool.Exec(ctx, sql, t2.ID); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestStaffCannotReadForeignDevicesCommentCapAndRedaction(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// Staff raising a ticket for alice may only attach alice's device, not bob's.
	if _, err := e.svc.Create(ctx, e.c(e.agent), e.staff(), application.CreateInput{Title: "x", AffectedUserID: &e.alice, AssetID: &e.otherBook}); !errors.Is(err, application.ErrDeviceInvalid) {
		t.Errorf("staff attaching someone else's device: %v", err)
	}
	if _, err := e.svc.Create(ctx, e.c(e.agent), e.staff(), application.CreateInput{Title: "x", AffectedUserID: &e.alice, AssetID: &e.myBook}); err != nil {
		t.Errorf("staff attaching the affected user's device: %v", err)
	}
	tk := e.raise()
	if _, err := e.svc.Assign(ctx, e.c(e.agent), e.staff(), tk.ID, nil, nil, &e.team); err != nil {
		t.Fatal(err)
	}
	// The queue stays hidden in every response an employee gets.
	closed, err := e.op(tk, e.user(), "cancel", "not needed")
	if err != nil || closed.QueueTeamID != nil {
		t.Errorf("cancel response leaks the queue: %+v %v", closed, err)
	}
	probe, err := e.svc.List(ctx, e.user(), false, application.Filter{QueueID: e.team})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range probe.Items {
		if x.QueueTeamID != nil {
			t.Error("list leaks the queue")
		}
	}
	// A conversation is capped.
	t2 := e.raise()
	for i := 0; i < application.MaxComments; i++ {
		if _, err := e.svc.AddComment(ctx, e.c(e.alice), e.user(), t2.ID, "again", false); err != nil {
			t.Fatalf("comment %d: %v", i, err)
		}
	}
	if _, err := e.svc.AddComment(ctx, e.c(e.alice), e.user(), t2.ID, "one more", false); !errors.Is(err, application.ErrCommentLimit) {
		t.Errorf("comment above the cap: %v", err)
	}
	if _, err := e.svc.AddComment(ctx, e.c(e.agent), e.staff(), t2.ID, "staff can no longer answer either", false); !errors.Is(err, application.ErrCommentLimit) {
		t.Errorf("the cap holds for staff too: %v", err)
	}
}
