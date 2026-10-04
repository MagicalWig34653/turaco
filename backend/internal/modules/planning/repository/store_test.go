package repository_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/planning/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/planning/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

// ---- fakes of the other modules' contracts ----

type fakeDir struct {
	inactive         map[string]bool
	locs             map[string]string
	maxLocationBatch int
	afterActiveUsers func()
}

func (d *fakeDir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = !d.inactive[id]
	}
	if d.afterActiveUsers != nil {
		d.afterActiveUsers()
	}
	return out, nil
}
func (d *fakeDir) ActiveTeams(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
func (d *fakeDir) UserNames(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		out[id] = "user " + id[:4]
	}
	return out, nil
}
func (d *fakeDir) LocationNames(_ context.Context, ids []string) (map[string]string, error) {
	d.maxLocationBatch = max(d.maxLocationBatch, len(ids))
	if len(ids) > 500 {
		return nil, errors.New("too many locations")
	}
	out := map[string]string{}
	for _, id := range ids {
		if n, ok := d.locs[id]; ok {
			out[id] = n
		}
	}
	return out, nil
}

type fakeChanges struct {
	byID      map[string]application.ChangeInfo
	calendar  []application.ChangeCalendarEntry
	truncated bool
	from, to  time.Time
}

func (f *fakeChanges) Lookup(_ context.Context, ids []string) (map[string]application.ChangeInfo, error) {
	out := map[string]application.ChangeInfo{}
	for _, id := range ids {
		if v, ok := f.byID[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}
func (f *fakeChanges) Calendar(_ context.Context, from, to time.Time, _ int) ([]application.ChangeCalendarEntry, bool, error) {
	f.from, f.to = from, to
	return f.calendar, f.truncated, nil
}

type fakeTasks struct {
	byID map[string]application.TaskInfo
}

func (f *fakeTasks) Tasks(_ context.Context, ids []string) ([]application.TaskInfo, error) {
	var out []application.TaskInfo
	for _, id := range ids {
		if v, ok := f.byID[id]; ok {
			out = append(out, v)
		}
	}
	return out, nil
}

type fakeProcurement struct {
	byID map[string]application.RequestInfo
}

func (f *fakeProcurement) Requests(_ context.Context, ids []string) (map[string]application.RequestInfo, error) {
	out := map[string]application.RequestInfo{}
	for _, id := range ids {
		if v, ok := f.byID[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

type fakeServices struct {
	byID        map[string]application.ServiceInfo
	maxBatch    int
	afterLookup func()
}

func (f *fakeServices) Lookup(_ context.Context, ids []string) (map[string]application.ServiceInfo, error) {
	f.maxBatch = max(f.maxBatch, len(ids))
	if len(ids) > 500 {
		return nil, errors.New("too many services")
	}
	out := map[string]application.ServiceInfo{}
	for _, id := range ids {
		if v, ok := f.byID[id]; ok {
			out[id] = v
		}
	}
	if f.afterLookup != nil {
		f.afterLookup()
	}
	return out, nil
}

// fakeApprovals refuses excluded approvers like the Approvals module does.
type fakeApprovals struct {
	mu        sync.Mutex
	requested []application.ApprovalRequest
	cancelled []string
	viewers   map[string]bool // subjectID+":"+user
}

func (a *fakeApprovals) RequestInTx(ctx context.Context, tx pgx.Tx, _ audit.Actor, _ string, r application.ApprovalRequest) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.ApproverUserID != nil && slices.Contains(r.ExcludedUserIDs, *r.ApproverUserID) {
		return "", application.ErrNoEligibleApprover
	}
	a.requested = append(a.requested, r)
	var id string
	if err := tx.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id); err != nil {
		return "", err
	}
	return id, nil
}
func (a *fakeApprovals) CancelBySubjectInTx(_ context.Context, _ pgx.Tx, _ audit.Actor, _, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cancelled = append(a.cancelled, id)
	return nil
}
func (a *fakeApprovals) ForSubject(context.Context, string) ([]application.ApprovalInfo, error) {
	return nil, nil
}
func (a *fakeApprovals) CanView(_ context.Context, subjectID, userID string) (bool, error) {
	return a.viewers[subjectID+":"+userID], nil
}

type fakeNotifier struct {
	mu     sync.Mutex
	seen   map[string]bool
	intent []notifications.Intent
}

func (n *fakeNotifier) Create(_ context.Context, _ pgx.Tx, in notifications.Intent) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	key := in.RecipientUserID + "|" + in.DedupeKey
	if n.seen[key] {
		return false, nil
	}
	n.seen[key] = true
	n.intent = append(n.intent, in)
	return true, nil
}

// ---- environment ----

type env struct {
	t     *testing.T
	pool  *pgxpool.Pool
	svc   *application.Service
	notes *application.Notifications
	store *repository.Repository
	graph *relationships.Graph

	dir      *fakeDir
	changes  *fakeChanges
	tasks    *fakeTasks
	proc     *fakeProcurement
	services *fakeServices
	appr     *fakeApprovals
	notifier *fakeNotifier

	corr  string
	ids   []string
	clock time.Time

	mgr, mgr2, owner, viewer, approver, outsider string
	manage, manage2, view, ownerP, outsiderP     application.Principal
}

func ptr[T any](v T) *T { return &v }

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	e := &env{t: t, pool: pool, corr: "planning-" + hex.EncodeToString(b), clock: time.Now().UTC().Truncate(time.Second),
		dir:      &fakeDir{inactive: map[string]bool{}, locs: map[string]string{}},
		changes:  &fakeChanges{byID: map[string]application.ChangeInfo{}},
		tasks:    &fakeTasks{byID: map[string]application.TaskInfo{}},
		proc:     &fakeProcurement{byID: map[string]application.RequestInfo{}},
		services: &fakeServices{byID: map[string]application.ServiceInfo{}},
		appr:     &fakeApprovals{viewers: map[string]bool{}},
		notifier: &fakeNotifier{seen: map[string]bool{}},
	}
	for _, dst := range []*string{&e.mgr, &e.mgr2, &e.owner, &e.viewer, &e.approver, &e.outsider} {
		*dst = e.uuid()
	}
	reg := relationships.NewRegistry()
	reg.Register(application.Triples...)
	e.graph = relationships.New(reg)
	e.store = repository.New(pool)
	e.svc = application.NewService(e.store, e.graph, e.dir, e.changes, e.tasks, e.proc, e.services, e.appr).WithClock(func() time.Time { return e.clock })
	e.notes = application.NewNotifications(e.store, e.dir, e.notifier)
	all := application.Principal{ChangesView: true, TasksView: true, ProcurementView: true, ServicesView: true, InfraView: true}
	e.manage, e.manage2, e.view = all, all, all
	e.manage.UserID, e.manage.Manage = e.mgr, true
	e.manage2.UserID, e.manage2.Manage = e.mgr2, true
	e.view.UserID, e.view.View = e.viewer, true
	e.ownerP = application.Principal{UserID: e.owner}
	e.outsiderP = application.Principal{UserID: e.outsider}
	t.Cleanup(func() {
		ctx := context.Background()
		mine := `SELECT id FROM planning.initiatives WHERE created_by = ANY($1::uuid[])`
		_, _ = pool.Exec(ctx, `DELETE FROM platform.relationships WHERE source_type = 'initiative' AND source_id IN (`+mine+`)`, e.ids)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id LIKE $1`, e.corr+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id LIKE $1`, e.corr+"%")
		conn, err := pool.Acquire(ctx)
		if err != nil {
			return
		}
		defer conn.Release()
		// Transitions are append-only: skip the triggers on this connection only.
		_, _ = conn.Exec(ctx, `SET session_replication_role = replica`)
		_, _ = conn.Exec(ctx, `DELETE FROM planning.initiative_transitions WHERE initiative_id IN (`+mine+`)`, e.ids)
		_, _ = conn.Exec(ctx, `DELETE FROM planning.milestones WHERE initiative_id IN (`+mine+`)`, e.ids)
		_, _ = conn.Exec(ctx, `DELETE FROM planning.initiatives WHERE created_by = ANY($1::uuid[])`, e.ids)
		_, _ = conn.Exec(ctx, `RESET session_replication_role`)
	})
	return e
}

func (e *env) uuid() string {
	var id string
	if err := e.pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	e.ids = append(e.ids, id)
	return id
}

func (e *env) ctx() context.Context { return context.Background() }

func (e *env) caller(user string) application.Caller {
	return application.Caller{Actor: audit.UserActor(user), CorrelationID: e.corr}
}

func (e *env) create() application.Initiative {
	e.t.Helper()
	i, err := e.svc.Create(e.ctx(), e.caller(e.mgr), e.manage, application.NewInitiative{Title: "Replace core switches", Goal: "Redundant core", OwnerUserID: e.owner})
	if err != nil {
		e.t.Fatalf("create: %v", err)
	}
	return i
}

// v returns the current version of an Initiative.
func (e *env) v(id string) *int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(e.ctx(), `SELECT version FROM planning.initiatives WHERE id = $1::uuid`, id).Scan(&n); err != nil {
		return ptr(1)
	}
	return &n
}

func (e *env) get(id string) application.Initiative {
	e.t.Helper()
	i, err := e.store.Get(e.ctx(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return i
}

func (e *env) decideWith(subjectID, subjectType, decision, approvalID string) error {
	payload, _ := json.Marshal(map[string]any{"approvalId": approvalID, "subjectType": subjectType, "subjectId": subjectID, "decision": decision})
	return pgx.BeginFunc(e.ctx(), e.pool, func(tx pgx.Tx) error {
		return e.svc.OnApprovalDecided(e.ctx(), tx, events.OutboxEvent{ID: "ev", EventType: "ApprovalDecided", CorrelationID: e.corr, Payload: payload})
	})
}

func (e *env) decide(id, decision string) {
	e.t.Helper()
	cur := e.get(id)
	if cur.ApprovalID == nil {
		e.t.Fatal("no approval")
	}
	if err := e.decideWith(id, "initiative", decision, *cur.ApprovalID); err != nil {
		e.t.Fatalf("decide: %v", err)
	}
}

// drive creates an Initiative and moves it to the status through the explicit operations.
func (e *env) drive(status string) application.Initiative {
	e.t.Helper()
	ctx, mc := e.ctx(), e.caller(e.mgr)
	step := func(i application.Initiative, err error) application.Initiative {
		e.t.Helper()
		if err != nil {
			e.t.Fatalf("drive to %s: %v", status, err)
		}
		return i
	}
	i := e.create()
	switch status {
	case "idea":
		return i
	case "cancelled":
		return step(e.svc.Cancel(ctx, mc, e.manage, i.ID, e.v(i.ID), "superseded"))
	}
	i = step(e.svc.StartPlanning(ctx, mc, e.manage, i.ID, e.v(i.ID)))
	if status == "planning" {
		return i
	}
	i = step(e.svc.Propose(ctx, e.caller(e.mgr2), e.manage2, i.ID, e.v(i.ID), application.Approver{UserID: &e.approver}))
	if status == "proposed" {
		return i
	}
	e.decide(i.ID, "approve")
	i = e.get(i.ID)
	if status == "approved" {
		return i
	}
	i = step(e.svc.Activate(ctx, mc, e.manage, i.ID, e.v(i.ID)))
	switch status {
	case "active":
		return i
	case "on_hold":
		return step(e.svc.Hold(ctx, mc, e.manage, i.ID, e.v(i.ID), "budget"))
	case "completed":
		return step(e.svc.Complete(ctx, mc, e.manage, i.ID, e.v(i.ID)))
	}
	e.t.Fatalf("unknown status %s", status)
	return i
}

func (e *env) activate(i application.Initiative) application.Initiative {
	e.t.Helper()
	if _, err := e.svc.Propose(e.ctx(), e.caller(e.mgr2), e.manage2, i.ID, e.v(i.ID), application.Approver{UserID: &e.approver}); err != nil {
		e.t.Fatal(err)
	}
	e.decide(i.ID, "approve")
	out, err := e.svc.Activate(e.ctx(), e.caller(e.mgr), e.manage, i.ID, e.v(i.ID))
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(e.ctx(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) audits(action, id string) int {
	return e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = $2 AND target_id = $3`, e.corr, "planning.initiative."+action, id)
}

func isTransition(err error) bool {
	var tr *application.InvalidTransitionError
	return errors.As(err, &tr)
}

func isInvalid(err error) bool {
	var inv *application.InvalidInputError
	return errors.As(err, &inv)
}

// ---- lifecycle ----

func TestLifecycleTransitions(t *testing.T) {
	e := newEnv(t)
	ctx, mc := e.ctx(), e.caller(e.mgr)
	type opFn func(id string, v *int) (application.Initiative, error)
	ops := map[string]opFn{
		"start_planning": func(id string, v *int) (application.Initiative, error) {
			return e.svc.StartPlanning(ctx, mc, e.manage, id, v)
		},
		"propose": func(id string, v *int) (application.Initiative, error) {
			return e.svc.Propose(ctx, e.caller(e.mgr2), e.manage2, id, v, application.Approver{UserID: &e.approver})
		},
		"activate": func(id string, v *int) (application.Initiative, error) {
			return e.svc.Activate(ctx, mc, e.manage, id, v)
		},
		"hold": func(id string, v *int) (application.Initiative, error) {
			return e.svc.Hold(ctx, mc, e.manage, id, v, "budget")
		},
		"resume": func(id string, v *int) (application.Initiative, error) { return e.svc.Resume(ctx, mc, e.manage, id, v) },
		"complete": func(id string, v *int) (application.Initiative, error) {
			return e.svc.Complete(ctx, mc, e.manage, id, v)
		},
		"cancel": func(id string, v *int) (application.Initiative, error) {
			return e.svc.Cancel(ctx, mc, e.manage, id, v, "other")
		},
	}
	allowed := map[string]map[string]string{ // from -> op -> to
		"idea":      {"start_planning": "planning", "cancel": "cancelled"},
		"planning":  {"propose": "proposed", "cancel": "cancelled"},
		"proposed":  {"cancel": "cancelled"},
		"approved":  {"activate": "active", "cancel": "cancelled"},
		"active":    {"hold": "on_hold", "complete": "completed", "cancel": "cancelled"},
		"on_hold":   {"resume": "active", "cancel": "cancelled"},
		"completed": {},
		"cancelled": {},
	}
	for from, permitted := range allowed {
		for op, fn := range ops {
			t.Run(from+"/"+op, func(t *testing.T) {
				i := e.drive(from)
				out, err := fn(i.ID, e.v(i.ID))
				to, ok := permitted[op]
				if !ok {
					if !isTransition(err) {
						t.Fatalf("%s from %s: want invalid transition, got %v", op, from, err)
					}
					if e.get(i.ID).Status != from {
						t.Fatal("a refused operation changed the status")
					}
					return
				}
				if err != nil || out.Status != to {
					t.Fatalf("%s from %s = %s, %v; want %s", op, from, out.Status, err, to)
				}
				if e.count(`SELECT count(*) FROM planning.initiative_transitions WHERE initiative_id = $1::uuid AND from_status = $2 AND to_status = $3`, i.ID, from, to) != 1 {
					t.Error("transition not recorded")
				}
				if e.count(`SELECT count(*) FROM platform.outbox_events WHERE event_type = 'InitiativeStatusChanged' AND payload->>'initiativeId' = $1 AND payload->>'status' = $2 AND payload->>'previousStatus' = $3`, i.ID, to, from) != 1 {
					t.Error("InitiativeStatusChanged not published")
				}
			})
		}
	}
}

func TestLifecycleDetails(t *testing.T) {
	e := newEnv(t)
	ctx, mc := e.ctx(), e.caller(e.mgr)
	i := e.create()
	if i.Status != "idea" || !strings.HasPrefix(i.Reference, "INI-") || i.Version != 1 || i.OwnerID != e.owner {
		t.Fatalf("created = %+v", i)
	}
	if e.audits("created", i.ID) != 1 {
		t.Error("create not audited")
	}
	// expectedVersion is required and checked.
	if _, err := e.svc.StartPlanning(ctx, mc, e.manage, i.ID, nil); !isInvalid(err) {
		t.Errorf("missing version: %v", err)
	}
	if _, err := e.svc.StartPlanning(ctx, mc, e.manage, i.ID, ptr(99)); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale version: %v", err)
	}
	// Reasons are codes from the lists.
	a := e.drive("active")
	if _, err := e.svc.Hold(ctx, mc, e.manage, a.ID, e.v(a.ID), "free text"); !isInvalid(err) {
		t.Errorf("unknown hold reason: %v", err)
	}
	held, err := e.svc.Hold(ctx, mc, e.manage, a.ID, e.v(a.ID), "resource_shortage")
	if err != nil || held.StatusReason == nil || *held.StatusReason != "resource_shortage" {
		t.Fatalf("hold = %+v %v", held, err)
	}
	resumed, err := e.svc.Resume(ctx, mc, e.manage, a.ID, e.v(a.ID))
	if err != nil || resumed.StatusReason != nil || resumed.ActivatedAt == nil {
		t.Fatalf("resume = %+v %v", resumed, err)
	}
	done, err := e.svc.Complete(ctx, mc, e.manage, a.ID, e.v(a.ID))
	if err != nil || done.ClosedAt == nil || done.ApprovedAt == nil {
		t.Fatalf("complete = %+v %v", done, err)
	}
	// Details: editable in idea, refused while proposed and after the end.
	title, goal := "Core network refresh", ""
	upd, err := e.svc.UpdateDetails(ctx, mc, e.manage, i.ID, e.v(i.ID), application.Details{Title: &title, Goal: &goal, TargetDate: ptr(time.Date(2027, 3, 31, 15, 0, 0, 0, time.UTC))})
	if err != nil || upd.Title != title || upd.Goal != nil || upd.TargetDate == nil || upd.TargetDate.Format("2006-01-02") != "2027-03-31" || !slices.Contains(upd.Editors, e.mgr) {
		t.Fatalf("update = %+v %v", upd, err)
	}
	cleared, err := e.svc.UpdateDetails(ctx, mc, e.manage, i.ID, e.v(i.ID), application.Details{ClearTargetDate: true})
	if err != nil || cleared.TargetDate != nil {
		t.Fatalf("clear target = %+v %v", cleared, err)
	}
	p := e.drive("proposed")
	if _, err := e.svc.UpdateDetails(ctx, mc, e.manage, p.ID, e.v(p.ID), application.Details{Title: &title}); !isTransition(err) {
		t.Errorf("update while proposed: %v", err)
	}
	if _, err := e.svc.UpdateDetails(ctx, mc, e.manage, done.ID, e.v(done.ID), application.Details{Title: &title}); !isTransition(err) {
		t.Errorf("update after completion: %v", err)
	}
	bad := "x‮y"
	if _, err := e.svc.UpdateDetails(ctx, mc, e.manage, i.ID, e.v(i.ID), application.Details{Title: &bad}); !isInvalid(err) {
		t.Errorf("unsafe title: %v", err)
	}
	inactive := e.uuid()
	e.dir.inactive[inactive] = true
	if _, err := e.svc.UpdateDetails(ctx, mc, e.manage, i.ID, e.v(i.ID), application.Details{OwnerUserID: &inactive}); !errors.Is(err, application.ErrReferenceInvalid) {
		t.Errorf("inactive owner: %v", err)
	}
	// Cancelling a proposal cancels its approval.
	if _, err := e.svc.Cancel(ctx, mc, e.manage, p.ID, e.v(p.ID), "budget"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(e.appr.cancelled, p.ID) {
		t.Error("the pending approval was not cancelled")
	}
	if e.audits("cancelled", p.ID) != 1 {
		t.Error("cancel not audited")
	}
}

// ---- approval ----

func TestApprovalRoundTripAndSeparationOfDuties(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	i := e.drive("planning")
	// Owner, creator, editors and the proposer can never approve.
	for _, u := range []string{e.owner, e.mgr, e.mgr2} {
		u := u
		if _, err := e.svc.Propose(ctx, e.caller(e.mgr2), e.manage2, i.ID, e.v(i.ID), application.Approver{UserID: &u}); !errors.Is(err, application.ErrNoEligibleApprover) {
			t.Errorf("approver %s: %v", u, err)
		}
	}
	if _, err := e.svc.Propose(ctx, e.caller(e.mgr2), e.manage2, i.ID, e.v(i.ID), application.Approver{}); !isInvalid(err) {
		t.Errorf("no approver: %v", err)
	}
	if e.get(i.ID).Status != "planning" {
		t.Fatal("a refused proposal must leave the initiative in planning")
	}
	p, err := e.svc.Propose(ctx, e.caller(e.mgr2), e.manage2, i.ID, e.v(i.ID), application.Approver{UserID: &e.approver})
	if err != nil || p.Status != "proposed" || p.ApprovalID == nil || p.ProposedBy == nil || *p.ProposedBy != e.mgr2 {
		t.Fatalf("propose = %+v %v", p, err)
	}
	last := e.appr.requested[len(e.appr.requested)-1]
	for _, u := range []string{e.owner, e.mgr, e.mgr2} {
		if !slices.Contains(last.ExcludedUserIDs, u) {
			t.Errorf("excluded %v misses %s", last.ExcludedUserIDs, u)
		}
	}
	// Events of other subjects and other approvals.
	if err := e.decideWith(i.ID, "change", "approve", *p.ApprovalID); err != nil || e.get(i.ID).Status != "proposed" {
		t.Errorf("other subject type changed the initiative: %v", err)
	}
	err = e.decideWith(i.ID, "initiative", "approve", e.uuid())
	if !events.IsPermanent(err) {
		t.Errorf("foreign approval id: %v", err)
	}
	if err := e.decideWith(i.ID, "initiative", "approve", ""); !events.IsPermanent(err) {
		t.Errorf("missing approval id: %v", err)
	}
	// Rejection returns it to planning; it can be proposed again.
	e.decide(i.ID, "reject")
	r := e.get(i.ID)
	if r.Status != "planning" || r.StatusReason == nil || *r.StatusReason != application.ReasonApprovalRejected || r.ApprovedAt != nil {
		t.Fatalf("rejected = %+v", r)
	}
	if e.count(`SELECT count(*) FROM planning.initiative_transitions WHERE initiative_id = $1::uuid AND operation = 'approval_rejected' AND actor_system IS NOT NULL`, i.ID) != 1 {
		t.Error("rejection transition missing or not by the system actor")
	}
	// A replayed decision is stale and changes nothing.
	if err := e.decideWith(i.ID, "initiative", "approve", *p.ApprovalID); err != nil || e.get(i.ID).Status != "planning" {
		t.Errorf("replayed decision: %v %s", err, e.get(i.ID).Status)
	}
	p2, err := e.svc.Propose(ctx, e.caller(e.mgr2), e.manage2, i.ID, e.v(i.ID), application.Approver{UserID: &e.approver})
	if err != nil || *p2.ApprovalID == *p.ApprovalID {
		t.Fatalf("re-propose = %+v %v", p2, err)
	}
	e.decide(i.ID, "approve")
	a := e.get(i.ID)
	if a.Status != "approved" || a.ApprovedAt == nil || a.StatusReason != nil {
		t.Fatalf("approved = %+v", a)
	}
	if e.audits("approved", i.ID) != 1 || e.audits("approval_rejected", i.ID) != 1 {
		t.Error("decisions not audited")
	}
	// Team approvers read only while the Initiative awaits the decision; afterwards it is hidden again.
	e.appr.viewers[i.ID+":"+e.approver] = true
	if _, err := e.svc.Get(ctx, application.Principal{UserID: e.approver}, i.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("approver read after the decision: %v", err)
	}
}

func TestApprovedPlanRequiresReplanForMaterialEdits(t *testing.T) {
	e := newEnv(t)
	ctx, mc := e.ctx(), e.caller(e.mgr)
	title, goal, owner := "Revised title", "Revised goal", e.viewer
	serviceID := e.uuid()
	e.services.byID[serviceID] = application.ServiceInfo{ID: serviceID, Reference: "SVC-9", Name: "Mail", Status: "operational"}
	for _, status := range []string{"approved", "active", "on_hold"} {
		t.Run(status, func(t *testing.T) {
			i := e.drive(status)
			oldApproval := *i.ApprovalID
			for field, details := range map[string]application.Details{
				"title": {Title: &title}, "goal": {Goal: &goal}, "owner": {OwnerUserID: &owner},
			} {
				if _, err := e.svc.UpdateDetails(ctx, mc, e.manage, i.ID, e.v(i.ID), details); !isTransition(err) {
					t.Errorf("%s edit: %v", field, err)
				}
			}
			if _, err := e.svc.AddMilestone(ctx, mc, e.manage, i.ID, e.v(i.ID), application.MilestoneInput{Title: "New scope", DueDate: e.clock.AddDate(0, 0, 7)}); !isTransition(err) {
				t.Errorf("milestone add: %v", err)
			}
			if _, _, err := e.svc.AddItem(ctx, mc, e.manage, i.ID, e.v(i.ID), "service", serviceID); !isTransition(err) {
				t.Errorf("item add: %v", err)
			}
			if _, err := e.svc.Replan(ctx, mc, e.manage, i.ID, nil, "scope_change"); !isInvalid(err) {
				t.Errorf("missing replan version: %v", err)
			}
			if _, err := e.svc.Replan(ctx, mc, e.manage, i.ID, e.v(i.ID), "free text"); !isInvalid(err) {
				t.Errorf("invalid replan reason: %v", err)
			}
			r, err := e.svc.Replan(ctx, mc, e.manage, i.ID, e.v(i.ID), "scope_change")
			if err != nil || r.Status != "planning" || r.ApprovalID != nil || r.ProposedBy != nil || r.ApprovedAt != nil || r.ActivatedAt != nil {
				t.Fatalf("replan = %+v %v", r, err)
			}
			if e.audits("replanned", i.ID) != 1 || e.count(`SELECT count(*) FROM planning.initiative_transitions WHERE initiative_id = $1::uuid AND from_status = $2 AND to_status = 'planning' AND operation = 'replanned' AND reason = 'scope_change'`, i.ID, status) != 1 {
				t.Error("replan not audited and recorded")
			}
			if _, err := e.svc.UpdateDetails(ctx, mc, e.manage, i.ID, e.v(i.ID), application.Details{Title: &title}); err != nil {
				t.Fatalf("edit after replan: %v", err)
			}
			p, err := e.svc.Propose(ctx, e.caller(e.mgr2), e.manage2, i.ID, e.v(i.ID), application.Approver{UserID: &e.approver})
			if err != nil || p.ApprovalID == nil || *p.ApprovalID == oldApproval {
				t.Fatalf("new proposal = %+v %v", p, err)
			}
		})
	}
}

// ---- milestones ----

func TestMilestones(t *testing.T) {
	e := newEnv(t)
	ctx, mc := e.ctx(), e.caller(e.mgr)
	i := e.drive("planning")
	due := e.clock.AddDate(0, 1, 0)
	if _, err := e.svc.AddMilestone(ctx, mc, e.manage, i.ID, nil, application.MilestoneInput{Title: "Design", DueDate: due}); !isInvalid(err) {
		t.Errorf("missing version: %v", err)
	}
	if _, err := e.svc.AddMilestone(ctx, mc, e.ownerP, i.ID, e.v(i.ID), application.MilestoneInput{Title: "Design", DueDate: due}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("owner without manage: %v", err)
	}
	m1, err := e.svc.AddMilestone(ctx, mc, e.manage, i.ID, e.v(i.ID), application.MilestoneInput{Title: "Design", DueDate: due})
	if err != nil || m1.Position != 0 || m1.Version != 1 {
		t.Fatalf("add = %+v %v", m1, err)
	}
	m2, err := e.svc.AddMilestone(ctx, mc, e.manage, i.ID, e.v(i.ID), application.MilestoneInput{Title: "Rollout", DueDate: e.clock.AddDate(0, 0, -1)})
	if err != nil || m2.Position != 1 {
		t.Fatalf("add second = %+v %v", m2, err)
	}
	if _, err := e.svc.AddMilestone(ctx, mc, e.manage, i.ID, e.v(i.ID), application.MilestoneInput{Title: "x", DueDate: due, Position: ptr(-1)}); !isInvalid(err) {
		t.Errorf("negative position: %v", err)
	}
	title := "Detailed design"
	u, err := e.svc.UpdateMilestone(ctx, mc, e.manage, i.ID, m1.ID, ptr(1), application.MilestoneChange{Title: &title, Position: ptr(5)})
	if err != nil || u.Title != title || u.Position != 5 || u.Version != 2 {
		t.Fatalf("update = %+v %v", u, err)
	}
	if _, err := e.svc.UpdateMilestone(ctx, mc, e.manage, i.ID, m1.ID, ptr(1), application.MilestoneChange{Title: &title}); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale milestone version: %v", err)
	}
	// A milestone of another Initiative is not found through this one (IDOR).
	other := e.drive("idea")
	if _, err := e.svc.CompleteMilestone(ctx, mc, e.manage, other.ID, m1.ID, ptr(2)); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("foreign milestone: %v", err)
	}
	c, err := e.svc.CompleteMilestone(ctx, mc, e.manage, i.ID, m1.ID, ptr(2))
	if err != nil || c.DoneAt == nil || c.DoneBy == nil || *c.DoneBy != e.mgr {
		t.Fatalf("complete = %+v %v", c, err)
	}
	if _, err := e.svc.CompleteMilestone(ctx, mc, e.manage, i.ID, m1.ID, ptr(3)); !isTransition(err) {
		t.Errorf("complete twice: %v", err)
	}
	d, err := e.svc.Get(ctx, e.view, i.ID)
	if err != nil || d.Progress.MilestonesDone != 1 || d.Progress.MilestonesTotal != 2 || d.Progress.MilestonesOverdue != 1 {
		t.Fatalf("progress = %+v %v", d.Progress, err)
	}
	if d.Milestones[0].ID != m2.ID {
		t.Error("milestones must be ordered by position")
	}
	r, err := e.svc.ReopenMilestone(ctx, mc, e.manage, i.ID, m1.ID, ptr(3))
	if err != nil || r.DoneAt != nil {
		t.Fatalf("reopen = %+v %v", r, err)
	}
	if _, err := e.svc.RemoveMilestone(ctx, mc, e.manage, i.ID, m2.ID, ptr(1), "because"); !isInvalid(err) {
		t.Errorf("unknown remove reason: %v", err)
	}
	if _, err := e.svc.RemoveMilestone(ctx, mc, e.manage, i.ID, m2.ID, ptr(1), "merged"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CompleteMilestone(ctx, mc, e.manage, i.ID, m2.ID, ptr(2)); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("removed milestone: %v", err)
	}
	if ms, _ := e.store.Milestones(ctx, i.ID); len(ms) != 1 {
		t.Errorf("live milestones = %d", len(ms))
	}
	if e.count(`SELECT count(*) FROM planning.milestones WHERE id = $1::uuid AND remove_reason = 'merged'`, m2.ID) != 1 {
		t.Error("removed milestone row must stay with its reason")
	}
	for _, a := range []string{"milestone_added", "milestone_updated", "milestone_completed", "milestone_reopened", "milestone_removed"} {
		if e.audits(a, i.ID) == 0 {
			t.Errorf("%s not audited", a)
		}
	}
	// Not editable while proposed; cap of live milestones.
	p := e.drive("proposed")
	if _, err := e.svc.AddMilestone(ctx, mc, e.manage, p.ID, e.v(p.ID), application.MilestoneInput{Title: "x", DueDate: due}); !isTransition(err) {
		t.Errorf("add while proposed: %v", err)
	}
	capped := e.drive("idea")
	for n := 0; n < application.MaxMilestones; n++ {
		if _, err := e.svc.AddMilestone(ctx, mc, e.manage, capped.ID, e.v(capped.ID), application.MilestoneInput{Title: "m", DueDate: due}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.AddMilestone(ctx, mc, e.manage, capped.ID, e.v(capped.ID), application.MilestoneInput{Title: "m", DueDate: due}); !errors.Is(err, application.ErrTooMany) {
		t.Errorf("milestone cap: %v", err)
	}
	// Due milestones (public contract) see only approved, active and on-hold Initiatives.
	act := e.drive("planning")
	if _, err := e.svc.AddMilestone(ctx, mc, e.manage, act.ID, e.v(act.ID), application.MilestoneInput{Title: "Go live", DueDate: e.clock.AddDate(0, 0, 3)}); err != nil {
		t.Fatal(err)
	}
	act = e.activate(act)
	list, err := e.svc.DueMilestones(ctx, e.clock, e.clock.AddDate(0, 0, 7), e.owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].InitiativeID != act.ID {
		t.Errorf("due milestones = %+v", list)
	}
	if _, err := e.svc.DueMilestones(ctx, e.clock, e.clock.AddDate(0, 0, 93), ""); !isInvalid(err) {
		t.Errorf("due range cap: %v", err)
	}
}

func TestMilestoneMutationsRecordEditorWithoutInitiativeVersionBump(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	for _, op := range []string{"add", "update", "complete", "reopen", "remove"} {
		t.Run(op, func(t *testing.T) {
			i := e.drive("planning")
			var m application.Milestone
			var err error
			if op != "add" {
				m, err = e.svc.AddMilestone(ctx, e.caller(e.mgr), e.manage, i.ID, e.v(i.ID), application.MilestoneInput{Title: "Scope", DueDate: e.clock.AddDate(0, 0, 7)})
				if err != nil {
					t.Fatal(err)
				}
			}
			if op == "reopen" {
				m, err = e.svc.CompleteMilestone(ctx, e.caller(e.mgr), e.manage, i.ID, m.ID, ptr(m.Version))
				if err != nil {
					t.Fatal(err)
				}
			}
			version := *e.v(i.ID)
			actor := e.caller(e.mgr2)
			switch op {
			case "add":
				_, err = e.svc.AddMilestone(ctx, actor, e.manage2, i.ID, &version, application.MilestoneInput{Title: "Scope", DueDate: e.clock.AddDate(0, 0, 7)})
			case "update":
				title := "Expanded scope"
				_, err = e.svc.UpdateMilestone(ctx, actor, e.manage2, i.ID, m.ID, ptr(m.Version), application.MilestoneChange{Title: &title})
			case "complete":
				_, err = e.svc.CompleteMilestone(ctx, actor, e.manage2, i.ID, m.ID, ptr(m.Version))
			case "reopen":
				_, err = e.svc.ReopenMilestone(ctx, actor, e.manage2, i.ID, m.ID, ptr(m.Version))
			case "remove":
				_, err = e.svc.RemoveMilestone(ctx, actor, e.manage2, i.ID, m.ID, ptr(m.Version), "merged")
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := *e.v(i.ID); got != version || !slices.Contains(e.get(i.ID).Editors, e.mgr2) {
				t.Fatalf("version/editor after %s: %d, %+v", op, got, e.get(i.ID).Editors)
			}
			if _, err := e.svc.Propose(ctx, e.caller(e.mgr), e.manage, i.ID, e.v(i.ID), application.Approver{UserID: &e.mgr2}); !errors.Is(err, application.ErrNoEligibleApprover) {
				t.Errorf("editor could approve after %s: %v", op, err)
			}
		})
	}
}

// ---- included records ----

func TestItemsValidationRedactionAndCaps(t *testing.T) {
	e := newEnv(t)
	ctx, mc := e.ctx(), e.caller(e.mgr)
	i := e.drive("planning")
	chg, cancelled, task, req, svc, retired := e.uuid(), e.uuid(), e.uuid(), e.uuid(), e.uuid(), e.uuid()
	e.changes.byID[chg] = application.ChangeInfo{ID: chg, Reference: "CHG-1", Title: "Firmware", Status: "scheduled"}
	e.changes.byID[cancelled] = application.ChangeInfo{ID: cancelled, Reference: "CHG-2", Title: "Old", Status: "cancelled"}
	e.tasks.byID[task] = application.TaskInfo{ID: task, Title: "Order cables", Status: "open"}
	e.proc.byID[req] = application.RequestInfo{ID: req, Reference: "PR-1", Quantity: 4, Status: "open"}
	e.services.byID[svc] = application.ServiceInfo{ID: svc, Reference: "SVC-1", Name: "Mail", Status: "operational"}
	e.services.byID[retired] = application.ServiceInfo{ID: retired, Reference: "SVC-2", Name: "Fax", Status: "retired"}

	for _, tc := range []struct {
		typ, id string
		want    error
	}{
		{"change", cancelled, application.ErrReferenceInvalid},
		{"service", retired, application.ErrReferenceInvalid},
		{"change", e.uuid(), application.ErrReferenceInvalid},
		{"asset", svc, nil},
	} {
		_, _, err := e.svc.AddItem(ctx, mc, e.manage, i.ID, e.v(i.ID), tc.typ, tc.id)
		if tc.want == nil && !isInvalid(err) || tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("add %s: %v", tc.typ, err)
		}
	}
	// A type the caller may not see is refused without a lookup, like a missing record.
	noChanges := e.manage
	noChanges.ChangesView = false
	if _, _, err := e.svc.AddItem(ctx, mc, noChanges, i.ID, e.v(i.ID), "change", chg); !errors.Is(err, application.ErrReferenceInvalid) {
		t.Errorf("hidden type: %v", err)
	}
	for _, it := range []struct{ typ, id string }{{"change", chg}, {"task", task}, {"procurement_request", req}, {"service", svc}} {
		l, created, err := e.svc.AddItem(ctx, mc, e.manage, i.ID, e.v(i.ID), it.typ, it.id)
		if err != nil || !created || l.ID != it.id {
			t.Fatalf("add %s = %+v %v %v", it.typ, l, created, err)
		}
	}
	if _, created, err := e.svc.AddItem(ctx, mc, e.manage, i.ID, e.v(i.ID), "task", task); err != nil || created {
		t.Errorf("re-add = %v %v", created, err)
	}
	if e.audits("item_added", i.ID) != 4 {
		t.Errorf("item_added audits = %d", e.audits("item_added", i.ID))
	}
	// Full visibility.
	d, err := e.svc.Get(ctx, e.view, i.ID)
	if err != nil || len(d.Items.Items) != 4 {
		t.Fatalf("detail = %+v %v", d.Items, err)
	}
	for _, it := range d.Items.Items {
		if it.Hidden || it.Missing || it.Status == nil {
			t.Errorf("item = %+v", it)
		}
	}
	if d.Progress.ChangesByStatus["scheduled"] != 1 || d.Progress.TasksByStatus["open"] != 1 || d.Progress.RequestsByStatus["open"] != 1 || d.Progress.Items != 4 {
		t.Errorf("progress = %+v", d.Progress)
	}
	// planning.view without other module permissions: placeholders, no counts.
	bare := application.Principal{UserID: e.viewer, View: true}
	d, err = e.svc.Get(ctx, bare, i.ID)
	if err != nil {
		t.Fatal(err)
	}
	for n, it := range d.Items.Items {
		if !it.Hidden || it.Title != nil || it.Status != nil || !strings.HasPrefix(it.ID, "hidden-") || slices.Contains([]string{chg, task, req, svc}, it.ID) {
			t.Errorf("item %d leaks: %+v", n, it)
		}
	}
	if d.Progress.ChangesByStatus != nil || d.Progress.TasksByStatus != nil || d.Progress.RequestsByStatus != nil || d.Progress.Items != 4 {
		t.Errorf("redacted progress = %+v", d.Progress)
	}
	// Paging.
	p1, err := e.svc.Items(ctx, e.view, i.ID, application.Page{Limit: 3})
	if err != nil || len(p1.Items) != 3 || p1.NextCursor == "" {
		t.Fatalf("page 1 = %+v %v", p1, err)
	}
	p2, err := e.svc.Items(ctx, e.view, i.ID, application.Page{Limit: 3, Cursor: p1.NextCursor})
	if err != nil || len(p2.Items) != 1 || p2.NextCursor != "" {
		t.Fatalf("page 2 = %+v %v", p2, err)
	}
	if p1.NextCursor == p1.Items[2].RelationshipID || strings.Contains(p1.NextCursor, "-") {
		t.Errorf("cursor exposed relationship id: %q", p1.NextCursor)
	}
	barePage, err := e.svc.Items(ctx, bare, i.ID, application.Page{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range barePage.Items {
		if !it.Hidden || it.ID != "hidden-2" || it.RelationshipID != "hidden-1" {
			t.Errorf("item placeholders not scoped to row: %+v", it)
		}
	}
	if _, err := e.svc.Items(ctx, e.view, i.ID, application.Page{Cursor: p1.Items[2].RelationshipID}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Errorf("relationship id accepted as cursor: %v", err)
	}
	if _, err := e.svc.Items(ctx, e.view, i.ID, application.Page{Cursor: "nope"}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Errorf("bad cursor: %v", err)
	}
	// A record that disappeared shows as missing.
	delete(e.tasks.byID, task)
	d, _ = e.svc.Get(ctx, e.view, i.ID)
	missing := 0
	for _, it := range d.Items.Items {
		if it.Missing {
			missing++
		}
	}
	if missing != 1 {
		t.Errorf("missing = %d", missing)
	}
	// Removal: hidden types are refused, unknown links succeed silently.
	if err := e.svc.RemoveItem(ctx, mc, noChanges, i.ID, e.v(i.ID), "change", chg); !errors.Is(err, application.ErrReferenceInvalid) {
		t.Errorf("remove hidden: %v", err)
	}
	if err := e.svc.RemoveItem(ctx, mc, e.manage, i.ID, e.v(i.ID), "change", chg); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RemoveItem(ctx, mc, e.manage, i.ID, e.v(i.ID), "change", chg); err != nil {
		t.Errorf("remove twice: %v", err)
	}
	if e.audits("item_removed", i.ID) != 1 {
		t.Errorf("item_removed audits = %d", e.audits("item_removed", i.ID))
	}
	if err := e.svc.RemoveItem(ctx, mc, e.manage, i.ID, nil, "service", svc); !isInvalid(err) {
		t.Errorf("remove without version: %v", err)
	}
	// Not editable while proposed.
	p := e.drive("proposed")
	if _, _, err := e.svc.AddItem(ctx, mc, e.manage, p.ID, e.v(p.ID), "service", svc); !isTransition(err) {
		t.Errorf("add while proposed: %v", err)
	}
	// Cap: MaxItems links per Initiative.
	capped := e.drive("idea")
	err = pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		for n := 0; n < application.MaxItems; n++ {
			if _, _, err := e.graph.Link(ctx, tx, relationships.LinkInput{Owner: application.RelationshipOwner,
				Source: relationships.Node{Type: "initiative", ID: capped.ID}, Type: application.RelIncludes,
				Target: relationships.Node{Type: "service", ID: e.uuid()}, Confidence: relationships.ConfidenceDeclared}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.AddItem(ctx, mc, e.manage, capped.ID, e.v(capped.ID), "service", svc); !errors.Is(err, application.ErrTooMany) {
		t.Errorf("item cap: %v", err)
	}
	if d, err := e.svc.Get(ctx, e.view, capped.ID); err != nil || len(d.Items.Items) != application.DefaultItemLimit || d.Items.NextCursor == "" || d.Progress.Items != application.MaxItems {
		t.Errorf("capped detail: %d items, cursor %q, progress %+v, %v", len(d.Items.Items), d.Items.NextCursor, d.Progress, err)
	}
}

func TestOwnerAndItemAreRecheckedAfterInitiativeLock(t *testing.T) {
	e := newEnv(t)
	ctx, mc := e.ctx(), e.caller(e.mgr)
	i := e.drive("planning")
	newOwner := e.uuid()
	e.dir.afterActiveUsers = func() {
		e.dir.inactive[newOwner] = true
		e.dir.afterActiveUsers = nil
	}
	if _, err := e.svc.UpdateDetails(ctx, mc, e.manage, i.ID, e.v(i.ID), application.Details{OwnerUserID: &newOwner}); !errors.Is(err, application.ErrReferenceInvalid) {
		t.Errorf("owner became inactive before row lock: %v", err)
	}
	if got := e.get(i.ID).OwnerID; got != e.owner {
		t.Errorf("inactive owner was persisted: %s", got)
	}
	serviceID := e.uuid()
	e.services.byID[serviceID] = application.ServiceInfo{ID: serviceID, Reference: "SVC-9", Name: "Mail", Status: "operational"}
	e.services.afterLookup = func() {
		delete(e.services.byID, serviceID)
		e.services.afterLookup = nil
	}
	if _, _, err := e.svc.AddItem(ctx, mc, e.manage, i.ID, e.v(i.ID), "service", serviceID); !errors.Is(err, application.ErrReferenceInvalid) {
		t.Errorf("service disappeared before row lock: %v", err)
	}
	if n := e.count(`SELECT count(*) FROM platform.relationships WHERE source_type = 'initiative' AND source_id = $1::uuid AND target_id = $2::uuid AND valid_until IS NULL`, i.ID, serviceID); n != 0 {
		t.Errorf("disappeared service linked: %d", n)
	}
}

// ---- calendar ----

func TestMaintenanceCalendar(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	from := time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 31)
	if _, err := e.svc.MaintenanceCalendar(ctx, e.view, from, from.AddDate(0, 0, 93)); !isInvalid(err) {
		t.Errorf("range longer than 92 days: %v", err)
	}
	if _, err := e.svc.MaintenanceCalendar(ctx, e.view, from, from.Add(92*24*time.Hour)); err != nil {
		t.Errorf("exactly 92 days: %v", err)
	}
	if _, err := e.svc.MaintenanceCalendar(ctx, e.view, to, from); !isInvalid(err) {
		t.Errorf("reversed range: %v", err)
	}
	if _, err := e.svc.MaintenanceCalendar(ctx, e.outsiderP, from, to); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("without permission: %v", err)
	}
	ws, we := from.Add(48*time.Hour), from.Add(50*time.Hour)
	chg, own, svc, loc, vm := e.uuid(), e.uuid(), e.uuid(), e.uuid(), e.uuid()
	e.services.byID[svc] = application.ServiceInfo{ID: svc, Reference: "SVC-9", Name: "Mail", Status: "operational"}
	e.dir.locs[loc] = "Berlin"
	e.changes.calendar = []application.ChangeCalendarEntry{
		{Change: application.ChangeInfo{ID: chg, Reference: "CHG-9", Title: "Core switch firmware", Kind: "normal", Risk: "high", Status: "scheduled", RequesterID: e.mgr, WindowStart: &ws, WindowEnd: &we},
			Affected: []application.Node{{Type: "service", ID: svc}, {Type: "location", ID: loc}, {Type: "vm", ID: vm}}},
		{Change: application.ChangeInfo{ID: own, Reference: "CHG-10", Title: "Own change", Status: "approved", RequesterID: e.outsider, WindowStart: &ws, WindowEnd: &we},
			Affected: []application.Node{{Type: "service", ID: e.uuid()}, {Type: "service", ID: svc}}},
	}
	e.changes.truncated = true
	// The Initiative that includes the change is listed for readers of the Initiative.
	i := e.drive("planning")
	e.changes.byID[chg] = e.changes.calendar[0].Change
	if _, _, err := e.svc.AddItem(ctx, e.caller(e.mgr), e.manage, i.ID, e.v(i.ID), "change", chg); err != nil {
		t.Fatal(err)
	}
	i = e.activate(i)
	cal, err := e.svc.MaintenanceCalendar(ctx, e.view, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if !e.changes.from.Equal(from) || !e.changes.to.Equal(to) || !cal.Truncated || len(cal.Items) != 2 {
		t.Fatalf("calendar = %+v", cal)
	}
	first := cal.Items[0]
	if first.Title == nil || first.Kind == nil || *first.Kind != "normal" || first.Risk == nil || *first.Risk != "high" || len(first.Affected) != 2 || first.Affected[0].Name == nil || *first.Affected[0].Name != "Mail" || *first.Affected[1].Name != "Berlin" {
		t.Errorf("first = %+v", first)
	}
	if len(first.Initiatives) != 1 || first.Initiatives[0].ID != i.ID {
		t.Errorf("initiatives = %+v", first.Initiatives)
	}
	// planning.view only: no titles of other people's Changes, hidden affected records.
	bare := application.Principal{UserID: e.viewer, View: true}
	cal, err = e.svc.MaintenanceCalendar(ctx, bare, from, to)
	if err != nil {
		t.Fatal(err)
	}
	first = cal.Items[0]
	if first.Title != nil || first.Kind != nil || first.Risk != nil || first.Reference != "CHG-9" || first.Status != "scheduled" {
		t.Errorf("title leaked: %+v", first)
	}
	for _, a := range first.Affected {
		if !a.Hidden || a.Name != nil || a.ID == svc || a.ID == loc {
			t.Errorf("affected leaked: %+v", a)
		}
	}
	if cal.Items[1].Affected[1].ID == first.Affected[0].ID {
		t.Errorf("hidden placeholders correlate across rows: %+v %+v", first.Affected, cal.Items[1].Affected)
	}
	// changes.view only: titles, but Initiatives only when readable (here: not the owner).
	changesOnly := application.Principal{UserID: e.outsider, ChangesView: true}
	cal, err = e.svc.MaintenanceCalendar(ctx, changesOnly, from, to)
	if err != nil || cal.Items[0].Title == nil || len(cal.Items[0].Initiatives) != 0 {
		t.Fatalf("changes-only calendar = %+v %v", cal.Items, err)
	}
	// The owner of an Initiative with changes.view sees it.
	cal, _ = e.svc.MaintenanceCalendar(ctx, application.Principal{UserID: e.owner, ChangesView: true}, from, to)
	if len(cal.Items[0].Initiatives) != 1 {
		t.Errorf("owner initiatives = %+v", cal.Items[0].Initiatives)
	}
	// Without changes.view the requester still sees the title of their own Change.
	requesterOnly := application.Principal{UserID: e.outsider, View: true}
	cal, _ = e.svc.MaintenanceCalendar(ctx, requesterOnly, from, to)
	if cal.Items[1].Title == nil || cal.Items[0].Title != nil {
		t.Errorf("requester titles = %v %v", cal.Items[0].Title, cal.Items[1].Title)
	}
}

func TestMaintenanceCalendarChunksNames(t *testing.T) {
	e := newEnv(t)
	from := time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	entry := application.ChangeCalendarEntry{Change: application.ChangeInfo{ID: e.uuid(), Reference: "CHG-501", Title: "Wide maintenance", RequesterID: e.mgr, WindowStart: &from, WindowEnd: &to}}
	for n := 0; n < 501; n++ {
		svc, loc := e.uuid(), e.uuid()
		e.services.byID[svc] = application.ServiceInfo{ID: svc, Name: "Service"}
		e.dir.locs[loc] = "Location"
		entry.Affected = append(entry.Affected, application.Node{Type: "service", ID: svc}, application.Node{Type: "location", ID: loc})
	}
	e.changes.calendar = []application.ChangeCalendarEntry{entry}
	cal, err := e.svc.MaintenanceCalendar(e.ctx(), e.view, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(cal.Items) != 1 || len(cal.Items[0].Affected) != 1002 || e.services.maxBatch != 500 || e.dir.maxLocationBatch != 500 {
		t.Errorf("calendar affected=%d, service batch=%d, location batch=%d", len(cal.Items[0].Affected), e.services.maxBatch, e.dir.maxLocationBatch)
	}
}

func TestMaintenanceCalendarReportsIncludedLinksTruncation(t *testing.T) {
	e := newEnv(t)
	from := time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	changeID := e.uuid()
	e.changes.calendar = []application.ChangeCalendarEntry{{Change: application.ChangeInfo{ID: changeID, Reference: "CHG-1", WindowStart: &from, WindowEnd: &to}}}
	_, err := e.pool.Exec(e.ctx(), `INSERT INTO platform.relationships (source_type, source_id, type, target_type, target_id, confidence, created_by)
		SELECT 'initiative', uuidv7(), 'INCLUDES', 'change', $1::uuid, 'declared', $2::uuid FROM generate_series(1, 5001)`, changeID, e.mgr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM platform.relationships WHERE target_type = 'change' AND target_id = $1::uuid AND created_by = $2::uuid`, changeID, e.mgr)
	})
	cal, err := e.svc.MaintenanceCalendar(e.ctx(), e.view, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if !cal.Truncated {
		t.Error("included initiative links were truncated without a signal")
	}
}

// ---- access ----

func TestPermissionsAndOwnership(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	i := e.drive("idea")
	if _, err := e.svc.Create(ctx, e.caller(e.viewer), e.view, application.NewInitiative{Title: "x"}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("create without manage: %v", err)
	}
	if _, err := e.svc.Create(ctx, e.caller(e.mgr2), e.manage, application.NewInitiative{Title: "x"}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("create as someone else: %v", err)
	}
	if _, err := e.svc.StartPlanning(ctx, e.caller(e.owner), e.ownerP, i.ID, e.v(i.ID)); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("owner without manage: %v", err)
	}
	// The owner reads their Initiative; an outsider gets not found (no existence hint).
	if _, err := e.svc.Get(ctx, e.ownerP, i.ID); err != nil {
		t.Errorf("owner read: %v", err)
	}
	for _, fn := range []func() error{
		func() error { _, err := e.svc.Get(ctx, e.outsiderP, i.ID); return err },
		func() error { _, err := e.svc.Transitions(ctx, e.outsiderP, i.ID, application.Page{}); return err },
		func() error { _, err := e.svc.Items(ctx, e.outsiderP, i.ID, application.Page{}); return err },
		func() error { _, err := e.svc.Get(ctx, e.view, "not-a-uuid"); return err },
		func() error { _, err := e.svc.Get(ctx, e.view, e.uuid()); return err },
	} {
		if err := fn(); !errors.Is(err, application.ErrNotFound) {
			t.Errorf("read: %v", err)
		}
	}
	// Lists: viewers see all, owners only their own, outsiders nothing.
	mine, err := e.svc.List(ctx, e.ownerP, application.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range mine.Items {
		if x.OwnerID != e.owner {
			t.Errorf("owner list leaks %s", x.ID)
		}
	}
	if len(mine.Items) == 0 {
		t.Error("owner list is empty")
	}
	if out, _ := e.svc.List(ctx, e.outsiderP, application.Filter{}); len(out.Items) != 0 {
		t.Errorf("outsider list = %d", len(out.Items))
	}
	if out, _ := e.svc.List(ctx, e.view, application.Filter{OwnerID: e.owner, Status: "idea", Query: "core SWITCH"}); len(out.Items) != 1 {
		t.Errorf("filtered list = %d", len(out.Items))
	}
	if _, err := e.svc.List(ctx, e.view, application.Filter{Status: "planned"}); !isInvalid(err) {
		t.Errorf("unknown status: %v", err)
	}
	target := time.Date(2027, 6, 30, 0, 0, 0, 0, time.UTC)
	if _, err := e.svc.UpdateDetails(ctx, e.caller(e.mgr), e.manage, i.ID, e.v(i.ID), application.Details{TargetDate: &target}); err != nil {
		t.Fatal(err)
	}
	from, to := target.AddDate(0, 0, -1), target
	if out, _ := e.svc.List(ctx, e.view, application.Filter{OwnerID: e.owner, TargetFrom: &from, TargetTo: &to}); len(out.Items) != 1 {
		t.Errorf("target range list = %d", len(out.Items))
	}
	if out, _ := e.svc.List(ctx, e.view, application.Filter{OwnerID: e.owner, TargetFrom: &to, TargetTo: &from}); out.Items != nil {
		t.Errorf("reversed target range should be refused")
	}
	tr, err := e.svc.Transitions(ctx, e.ownerP, i.ID, application.Page{})
	if err != nil || len(tr.Items) != 1 || tr.Items[0].Operation != "create" {
		t.Errorf("transitions = %+v %v", tr.Items, err)
	}
	ops := application.AllowedOperations(e.get(i.ID), e.ownerP)
	if len(ops) != 0 {
		t.Errorf("owner without manage gets operations %v", ops)
	}
}

func TestApproverReadOnlyDuringProposal(t *testing.T) {
	e := newEnv(t)
	for _, status := range []string{"planning", "proposed", "approved", "active"} {
		i := e.drive(status)
		e.appr.viewers[i.ID+":"+e.approver] = true
		p := application.Principal{UserID: e.approver}
		_, err := e.svc.Get(e.ctx(), p, i.ID)
		if status == "proposed" && err != nil {
			t.Errorf("approver should read proposed: %v", err)
		} else if status != "proposed" && !errors.Is(err, application.ErrNotFound) {
			t.Errorf("approver read %s: %v", status, err)
		}
	}
}

// ---- notifications ----

func TestStatusNotification(t *testing.T) {
	e := newEnv(t)
	i := e.drive("planning")
	var ev events.OutboxEvent
	var payload []byte
	var actor *string
	if err := e.pool.QueryRow(e.ctx(), `SELECT id::text, payload, actor_id::text FROM platform.outbox_events WHERE event_type = 'InitiativeStatusChanged' AND payload->>'initiativeId' = $1`, i.ID).
		Scan(&ev.ID, &payload, &actor); err != nil {
		t.Fatal(err)
	}
	ev.EventType, ev.Payload, ev.ActorID, ev.CorrelationID = application.EventStatusChanged, payload, actor, e.corr
	if _, err := e.svc.UpdateDetails(e.ctx(), e.caller(e.mgr), e.manage, i.ID, e.v(i.ID), application.Details{OwnerUserID: &e.mgr2}); err != nil {
		t.Fatal(err)
	}
	run := func(ev events.OutboxEvent) {
		if err := pgx.BeginFunc(e.ctx(), e.pool, func(tx pgx.Tx) error { return e.notes.OnStatusChanged(e.ctx(), tx, ev) }); err != nil {
			t.Fatal(err)
		}
	}
	run(ev)
	run(ev) // idempotent
	if len(e.notifier.intent) != 1 || e.notifier.intent[0].RecipientUserID != e.owner || e.notifier.intent[0].Category != "initiative.state" || e.notifier.intent[0].Params["title"] != i.Reference {
		t.Fatalf("notifications = %+v", e.notifier.intent)
	}
	// The owner is not told about their own change, nor when inactive.
	ev.ID, ev.ActorID = "other", &e.owner
	run(ev)
	ev.ID, ev.ActorID = "third", nil
	e.dir.inactive[e.owner] = true
	run(ev)
	if len(e.notifier.intent) != 1 {
		t.Errorf("notifications = %d", len(e.notifier.intent))
	}
	ev.Payload = []byte(`{"initiativeId":"x"}`)
	if err := pgx.BeginFunc(e.ctx(), e.pool, func(tx pgx.Tx) error { return e.notes.OnStatusChanged(e.ctx(), tx, ev) }); !events.IsPermanent(err) {
		t.Errorf("bad payload: %v", err)
	}
}

// ---- concurrency ----

func TestConcurrentActivateAndCancel(t *testing.T) {
	e := newEnv(t)
	for round := 0; round < 5; round++ {
		i := e.drive("approved")
		v := e.v(i.ID)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, errs[0] = e.svc.Activate(e.ctx(), e.caller(e.mgr), e.manage, i.ID, v)
		}()
		go func() {
			defer wg.Done()
			_, errs[1] = e.svc.Cancel(e.ctx(), e.caller(e.mgr2), e.manage2, i.ID, v, "reprioritized")
		}()
		wg.Wait()
		ok := 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, application.ErrVersionConflict) || isTransition(err):
			default:
				t.Fatalf("unexpected error: %v", err)
			}
		}
		if ok != 1 {
			t.Fatalf("round %d: %d operations succeeded: %v", round, ok, errs)
		}
		if n := e.count(`SELECT count(*) FROM planning.initiative_transitions WHERE initiative_id = $1::uuid AND from_status = 'approved'`, i.ID); n != 1 {
			t.Fatalf("round %d: %d transitions from approved", round, n)
		}
	}
}

// ---- database invariants ----

func pgCode(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code
	}
	return ""
}

func TestDatabaseInvariants(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	i := e.drive("planning")
	m, err := e.svc.AddMilestone(ctx, e.caller(e.mgr), e.manage, i.ID, e.v(i.ID), application.MilestoneInput{Title: "M", DueDate: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	i = e.activate(i)
	for name, sql := range map[string]string{
		"unknown status":       `UPDATE planning.initiatives SET status = 'planned' WHERE id = $1::uuid`,
		"hold without reason":  `UPDATE planning.initiatives SET status = 'on_hold', status_reason = NULL WHERE id = $1::uuid`,
		"proposed no approval": `UPDATE planning.initiatives SET status = 'proposed', approval_id = NULL, approved_at = NULL, activated_at = NULL WHERE id = $1::uuid`,
		"active without start": `UPDATE planning.initiatives SET activated_at = NULL WHERE id = $1::uuid`,
		"completed not closed": `UPDATE planning.initiatives SET status = 'completed' WHERE id = $1::uuid`,
		"closed while active":  `UPDATE planning.initiatives SET closed_at = now() WHERE id = $1::uuid`,
		"idea with approval":   `UPDATE planning.initiatives SET status = 'idea', activated_at = NULL WHERE id = $1::uuid`,
		"reason free text":     `UPDATE planning.initiatives SET status_reason = 'Not A Code' WHERE id = $1::uuid`,
		"blank title":          `UPDATE planning.initiatives SET title = ' x' WHERE id = $1::uuid`,
		"time order":           `UPDATE planning.initiatives SET activated_at = approved_at - interval '1 day' WHERE id = $1::uuid`,
		"approved no approval": `UPDATE planning.initiatives SET approval_id = NULL WHERE id = $1::uuid`,
	} {
		if _, err := e.pool.Exec(ctx, sql, i.ID); pgCode(err) != "23514" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := e.pool.Exec(ctx, `UPDATE planning.milestones SET removed_at = now() WHERE id = $1::uuid`, m.ID); pgCode(err) != "23514" {
		t.Errorf("removed without reason: %v", err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE planning.milestones SET removed_by = $2::uuid WHERE id = $1::uuid`, m.ID, e.mgr); pgCode(err) != "23514" {
		t.Errorf("removed by without removal time: %v", err)
	}
	if _, err := e.pool.Exec(ctx, `DELETE FROM planning.milestones WHERE id = $1::uuid`, m.ID); pgCode(err) != "23001" {
		t.Errorf("delete milestone: %v", err)
	}
	for name, sql := range map[string]string{
		"invalid from": `INSERT INTO planning.initiative_transitions(initiative_id, from_status, to_status, operation, actor_system, correlation_id) VALUES ($1::uuid, 'unknown', 'active', 'test', 'test', 'test')`,
		"invalid to":   `INSERT INTO planning.initiative_transitions(initiative_id, from_status, to_status, operation, actor_system, correlation_id) VALUES ($1::uuid, 'active', 'unknown', 'test', 'test', 'test')`,
	} {
		if _, err := e.pool.Exec(ctx, sql, i.ID); pgCode(err) != "23514" {
			t.Errorf("%s transition: %v", name, err)
		}
	}
	// Transitions are append-only and keep their Initiative.
	for name, sql := range map[string]string{
		"update": `UPDATE planning.initiative_transitions SET reason = 'other' WHERE initiative_id = $1::uuid`,
		"delete": `DELETE FROM planning.initiative_transitions WHERE initiative_id = $1::uuid`,
	} {
		if _, err := e.pool.Exec(ctx, sql, i.ID); pgCode(err) != "23001" {
			t.Errorf("%s transitions: %v", name, err)
		}
	}
	if _, err := e.pool.Exec(ctx, `TRUNCATE planning.initiative_transitions`); err == nil {
		t.Error("truncate transitions succeeded")
	}
	if _, err := e.pool.Exec(ctx, `DELETE FROM planning.initiatives WHERE id = $1::uuid`, i.ID); pgCode(err) != "23001" && pgCode(err) != "23503" {
		t.Errorf("delete initiative with history: %v", err)
	}
}

func TestApprovalAndActivationUseDatabaseTime(t *testing.T) {
	e := newEnv(t)
	e.clock = time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC)
	var before time.Time
	if err := e.pool.QueryRow(e.ctx(), `SELECT now()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	i := e.drive("approved")
	if i.ApprovedAt == nil || i.ApprovedAt.Before(before) || i.ApprovedAt.After(time.Now().Add(time.Minute)) {
		t.Fatalf("approved_at did not use database time: %v", i.ApprovedAt)
	}
	e.clock = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	out, err := e.svc.Activate(e.ctx(), e.caller(e.mgr), e.manage, i.ID, e.v(i.ID))
	if err != nil {
		t.Fatal(err)
	}
	if out.ActivatedAt == nil || out.ActivatedAt.Before(*out.ApprovedAt) || out.ActivatedAt.Before(before) {
		t.Fatalf("activated_at did not use ordered database time: approved=%v activated=%v", out.ApprovedAt, out.ActivatedAt)
	}
	done, err := e.svc.Complete(e.ctx(), e.caller(e.mgr), e.manage, i.ID, e.v(i.ID))
	if err != nil || done.ClosedAt == nil || done.ClosedAt.Before(*out.ActivatedAt) {
		t.Fatalf("closed_at did not preserve database time order: %+v %v", done, err)
	}
}
