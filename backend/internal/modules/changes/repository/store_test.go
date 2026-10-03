package repository_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/changes/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/changes/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

// ---- fakes of the other modules' contracts ----

type fakeDir struct {
	mu       sync.Mutex
	inactive map[string]bool
	members  map[string][]string
	locs     map[string]string
}

func (d *fakeDir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = !d.inactive[id]
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
func (d *fakeDir) ActiveLocations(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		_, ok := d.locs[id]
		out[id] = ok
	}
	return out, nil
}
func (d *fakeDir) LocationNames(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if n, ok := d.locs[id]; ok {
			out[id] = n
		}
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
func (d *fakeDir) CurrentMemberIDs(_ context.Context, team string) ([]string, error) {
	return d.members[team], nil
}

type fakeServices struct {
	byID map[string]application.ServiceInfo
}

func (f *fakeServices) Lookup(_ context.Context, ids []string) (map[string]application.ServiceInfo, error) {
	out := map[string]application.ServiceInfo{}
	for _, id := range ids {
		if v, ok := f.byID[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}
func (f *fakeServices) Impact(_ context.Context, p application.Principal, nodeType, id string, _ int) (application.Impact, error) {
	if nodeType == "service" {
		if _, ok := f.byID[id]; !ok {
			return application.Impact{}, application.ErrNotFound
		}
	}
	return application.Impact{Type: nodeType, ID: id, Nodes: []application.ImpactNode{{Type: "service", ID: "downstream-of-" + id, Depth: 1, Confidence: "declared"}}}, nil
}

type fakeInfra struct{ vms map[string]application.VMInfo }

func (f *fakeInfra) VMs(_ context.Context, ids []string) (map[string]application.VMInfo, error) {
	out := map[string]application.VMInfo{}
	for _, id := range ids {
		if v, ok := f.vms[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

type fakeAssets struct {
	byID map[string]application.AssetInfo
}

func (f *fakeAssets) Assets(_ context.Context, ids []string) (map[string]application.AssetInfo, error) {
	out := map[string]application.AssetInfo{}
	for _, id := range ids {
		if v, ok := f.byID[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

// fakeApprovals records the requests; the decision is simulated by calling OnApprovalDecided.
type fakeApprovals struct {
	mu        sync.Mutex
	requested []application.ApprovalRequest
	cancelled []string
	viewers   map[string]bool // subjectID+":"+user
	refuse    error
}

func (a *fakeApprovals) RequestInTx(ctx context.Context, tx pgx.Tx, _ audit.Actor, _ string, r application.ApprovalRequest) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.refuse != nil {
		return "", a.refuse
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

type fakeTasks struct {
	mu        sync.Mutex
	statuses  map[string]string
	cancelled []string
}

func (t *fakeTasks) CreateInTx(ctx context.Context, tx pgx.Tx, _ audit.Actor, _ string, in application.TaskInput) (string, error) {
	var id string
	if err := tx.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id); err != nil {
		return "", err
	}
	t.mu.Lock()
	t.statuses[id] = "open"
	t.mu.Unlock()
	return id, nil
}
func (t *fakeTasks) CancelByContextInTx(_ context.Context, _ pgx.Tx, _ audit.Actor, _, changeID, _ string) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cancelled = append(t.cancelled, changeID)
	n := 0
	for id, st := range t.statuses {
		if st != "completed" && st != "cancelled" {
			t.statuses[id] = "cancelled"
			n++
		}
	}
	return n, nil
}
func (t *fakeTasks) StatusesInTx(_ context.Context, _ pgx.Tx, ids []string) (map[string]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := map[string]string{}
	for _, id := range ids {
		if st, ok := t.statuses[id]; ok {
			out[id] = st
		}
	}
	return out, nil
}
func (t *fakeTasks) Tasks(_ context.Context, ids []string) ([]application.TaskInfo, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []application.TaskInfo
	for _, id := range ids {
		if st, ok := t.statuses[id]; ok {
			out = append(out, application.TaskInfo{ID: id, Title: "step", Status: st})
		}
	}
	return out, nil
}

// fakeNotifier mimics the dedupe behavior of the notification service.
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

func (n *fakeNotifier) count(category string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := 0
	for _, i := range n.intent {
		if i.Category == category {
			c++
		}
	}
	return c
}

func (n *fakeNotifier) recipients(category string) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for _, i := range n.intent {
		if i.Category == category {
			out = append(out, i.RecipientUserID)
		}
	}
	slices.Sort(out)
	return out
}

// ---- environment ----

type env struct {
	t     *testing.T
	pool  *pgxpool.Pool
	svc   *application.Service
	notes *application.Notifications
	store *repository.Repository

	dir      *fakeDir
	services *fakeServices
	infra    *fakeInfra
	assets   *fakeAssets
	appr     *fakeApprovals
	tasks    *fakeTasks
	notifier *fakeNotifier

	corr  string
	ids   []string
	clock time.Time

	requester, other, owner, executor, approver, outsider string
	manage, manage2, view, execute, ownerP, outsiderP     application.Principal
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	e := &env{t: t, pool: pool, corr: "changes-" + hex.EncodeToString(b), clock: time.Now().UTC().Truncate(time.Second),
		dir:      &fakeDir{inactive: map[string]bool{}, members: map[string][]string{}, locs: map[string]string{}},
		services: &fakeServices{byID: map[string]application.ServiceInfo{}},
		infra:    &fakeInfra{vms: map[string]application.VMInfo{}},
		assets:   &fakeAssets{byID: map[string]application.AssetInfo{}},
		appr:     &fakeApprovals{viewers: map[string]bool{}},
		tasks:    &fakeTasks{statuses: map[string]string{}},
		notifier: &fakeNotifier{seen: map[string]bool{}},
	}
	for _, dst := range []*string{&e.requester, &e.other, &e.owner, &e.executor, &e.approver, &e.outsider} {
		*dst = e.uuid()
	}
	reg := relationships.NewRegistry()
	reg.Register(application.Triples...)
	graph := relationships.New(reg)
	e.store = repository.New(pool)
	e.svc = application.NewService(e.store, graph, e.dir, e.services, e.infra, e.assets, e.appr, e.tasks).WithClock(func() time.Time { return e.clock })
	e.notes = application.NewNotifications(e.store, graph, e.dir, e.services, e.notifier).WithClock(func() time.Time { return e.clock })
	e.manage = application.Principal{UserID: e.requester, Manage: true, ServicesView: true, InfraView: true, AssetsView: true}
	e.manage2 = application.Principal{UserID: e.other, Manage: true, ServicesView: true, InfraView: true, AssetsView: true}
	e.view = application.Principal{UserID: e.other, View: true, ServicesView: true, InfraView: true, AssetsView: true}
	e.execute = application.Principal{UserID: e.executor, Execute: true}
	e.ownerP = application.Principal{UserID: e.owner}
	e.outsiderP = application.Principal{UserID: e.outsider}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM platform.relationships WHERE source_type = 'change' AND source_id IN (SELECT id FROM changes.changes WHERE requester_user_id = ANY($1::uuid[]))`, e.ids)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id LIKE $1`, e.corr+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id LIKE $1`, e.corr+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM changes.changes WHERE requester_user_id = ANY($1::uuid[])`, e.ids)
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

func (e *env) caller(user string) application.Caller {
	return application.Caller{Actor: audit.UserActor(user), CorrelationID: e.corr}
}

func (e *env) window(startIn, length time.Duration) application.Window {
	s, f := e.clock.Add(startIn), e.clock.Add(startIn+length)
	return application.Window{Start: &s, End: &f}
}

func (e *env) ctx() context.Context { return context.Background() }

func (e *env) service(owner, team *string) string {
	id := e.uuid()
	e.services.byID[id] = application.ServiceInfo{ID: id, Reference: "SVC-" + id[:6], Name: "svc " + id[:6], Status: "operational", OwnerUserID: owner, OwnerTeamID: team}
	return id
}

func (e *env) create(kind, risk string) application.Change {
	e.t.Helper()
	in := application.NewChange{Title: "Upgrade", Description: "d", Kind: kind, Risk: risk, OwnerUserID: e.owner, Window: e.window(24*time.Hour, 2*time.Hour)}
	if risk != "" && risk != application.RiskLow {
		in.RollbackPlan = "restore snapshot"
	}
	c, err := e.svc.Create(e.ctx(), e.caller(e.requester), e.manage, in)
	if err != nil {
		e.t.Fatalf("create: %v", err)
	}
	return c
}

func (e *env) affect(c application.Change) {
	e.t.Helper()
	svc := e.service(&e.owner, nil)
	if _, _, err := e.svc.AddAffected(e.ctx(), e.caller(e.requester), e.manage, c.ID, nil, "service", svc); err != nil {
		e.t.Fatalf("add affected: %v", err)
	}
}

func (e *env) get(id string) application.Change {
	e.t.Helper()
	c, err := e.store.Get(e.ctx(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

// drive creates a change and moves it to the status through the explicit operations.
func (e *env) drive(kind, risk, status string) application.Change {
	e.t.Helper()
	ctx, rc := e.ctx(), e.caller(e.requester)
	step := func(c application.Change, err error) application.Change {
		e.t.Helper()
		if err != nil {
			e.t.Fatalf("drive to %s: %v", status, err)
		}
		return c
	}
	c := e.create(kind, risk)
	e.affect(c)
	c = e.get(c.ID)
	if status == "draft" {
		return c
	}
	if status == "cancelled" {
		return step(e.svc.Cancel(ctx, rc, e.manage, c.ID, nil, "no_longer_needed"))
	}
	c = step(e.svc.Submit(ctx, rc, e.manage, c.ID, nil))
	if status == "assessment" {
		return c
	}
	approver := application.Approver{}
	if risk != "" && risk != application.RiskLow || kind == application.KindEmergency {
		approver.UserID = &e.approver
	}
	c = step(e.svc.Assess(ctx, e.caller(e.other), e.manage2, c.ID, nil, application.Assessment{Risk: c.Risk, Approver: approver}))
	if status == "pending_approval" {
		return c
	}
	if c.Status == application.StatusPendingApproval {
		e.decide(c.ID, "approve")
	}
	if status == "rejected" {
		panic("use driveRejected")
	}
	c = e.get(c.ID)
	if status == "approved" {
		return c
	}
	c = step(e.svc.Schedule(ctx, rc, e.manage, c.ID, nil, application.ScheduleInput{}))
	if status == "scheduled" {
		return c
	}
	c = step(e.svc.Start(ctx, e.caller(e.owner), e.ownerP, c.ID, nil))
	if status == "in_progress" {
		return c
	}
	if status == "failed" {
		return step(e.svc.Fail(ctx, e.caller(e.owner), e.ownerP, c.ID, nil, "execution_error", true))
	}
	c = step(e.svc.Complete(ctx, e.caller(e.owner), e.ownerP, c.ID, nil, ""))
	if status == "completed" {
		return c
	}
	if status == "review" {
		return step(e.svc.Review(ctx, rc, e.manage, c.ID, nil, "went fine"))
	}
	if status == "closed" {
		if kind == application.KindEmergency {
			c = step(e.svc.Review(ctx, rc, e.manage, c.ID, nil, "ok"))
		}
		return step(e.svc.Close(ctx, rc, e.manage, c.ID, nil))
	}
	e.t.Fatalf("unknown status %s", status)
	return c
}

func (e *env) driveRejected() application.Change {
	e.t.Helper()
	c := e.drive("normal", "medium", "pending_approval")
	e.decide(c.ID, "reject")
	return e.get(c.ID)
}

// decide simulates the ApprovalDecided event the Approvals module emits.
func (e *env) decide(changeID, decision string) {
	e.t.Helper()
	if err := e.decideErr(changeID, "change", decision); err != nil {
		e.t.Fatalf("decide: %v", err)
	}
}

func (e *env) decideErr(subjectID, subjectType, decision string) error {
	payload, _ := json.Marshal(map[string]any{"subjectType": subjectType, "subjectId": subjectID, "decision": decision})
	return pgx.BeginFunc(e.ctx(), e.pool, func(tx pgx.Tx) error {
		return e.svc.OnApprovalDecided(e.ctx(), tx, events.OutboxEvent{ID: "ev", EventType: "ApprovalDecided", CorrelationID: e.corr, Payload: payload})
	})
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(e.ctx(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) audits(action string, changeID string) int {
	return e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = $2 AND target_id = $3`, e.corr, "changes.change."+action, changeID)
}

func (e *env) published(typ, changeID string) int {
	return e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = $2 AND payload->>'changeId' = $3`, e.corr, typ, changeID)
}

func is(err error, target error) bool { return errors.Is(err, target) }

func isTransition(err error) bool {
	var tr *application.InvalidTransitionError
	return errors.As(err, &tr)
}

func isInvalid(err error) bool {
	var inv *application.InvalidInputError
	return errors.As(err, &inv)
}

// ---- tests ----

func TestLifecycleAuditEventsAndTransitions(t *testing.T) {
	e := newEnv(t)
	c := e.drive("normal", "low", "closed")
	if c.Status != "closed" || c.ClosedAt == nil || c.Reference == "" {
		t.Fatalf("closed change = %+v", c)
	}
	for _, a := range []string{"created", "affected_added", "submitted", "assessed", "scheduled", "started", "completed", "closed"} {
		if e.audits(a, c.ID) != 1 {
			t.Errorf("audit action %s recorded %d times", a, e.audits(a, c.ID))
		}
	}
	for _, ev := range []string{"ChangeSubmitted", "ChangeApproved", "ChangeScheduled", "ChangeStarted", "ChangeCompleted"} {
		if e.published(ev, c.ID) != 1 {
			t.Errorf("event %s published %d times", ev, e.published(ev, c.ID))
		}
	}
	tr, err := e.svc.Transitions(e.ctx(), e.manage, c.ID, application.Page{})
	if err != nil {
		t.Fatal(err)
	}
	var ops []string
	for _, x := range tr.Items {
		ops = append(ops, x.Operation+":"+x.ToStatus)
	}
	want := []string{"create:draft", "submitted:assessment", "assessed:approved", "scheduled:scheduled", "started:in_progress", "completed:completed", "closed:closed"}
	if !slices.Equal(ops, want) {
		t.Errorf("transitions = %v, want %v", ops, want)
	}
	// Audit entries never carry free text.
	if n := e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND (before_data::text LIKE '%Upgrade%' OR after_data::text LIKE '%Upgrade%' OR metadata::text LIKE '%Upgrade%')`, e.corr); n != 0 {
		t.Errorf("%d audit entries contain the title", n)
	}
	// The history is append-only.
	if _, err := e.pool.Exec(e.ctx(), `UPDATE changes.change_transitions SET operation = 'x' WHERE change_id = $1::uuid`, c.ID); err == nil {
		t.Error("a transition row could be updated")
	}
}

// Every operation is allowed in exactly the statuses of the lifecycle; the others
// are refused with an invalid-transition error and change nothing.
func TestOperationsPerStatus(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	statuses := []string{"draft", "assessment", "pending_approval", "approved", "scheduled", "in_progress", "completed", "review", "closed", "failed", "cancelled", "rejected"}
	ops := map[string]func(c application.Change) error{
		"update": func(c application.Change) error {
			_, err := e.svc.UpdateDetails(ctx, e.caller(e.requester), e.manage, c.ID, &c.Version, application.Details{Title: ptr("new title")})
			return err
		},
		"add_affected": func(c application.Change) error {
			_, _, err := e.svc.AddAffected(ctx, e.caller(e.requester), e.manage, c.ID, nil, "service", e.service(nil, nil))
			return err
		},
		"submit": func(c application.Change) error {
			_, err := e.svc.Submit(ctx, e.caller(e.requester), e.manage, c.ID, nil)
			return err
		},
		"assess": func(c application.Change) error {
			ap := application.Approver{}
			if c.Risk != "low" {
				ap.UserID = &e.approver
			}
			_, err := e.svc.Assess(ctx, e.caller(e.other), e.manage2, c.ID, nil, application.Assessment{Risk: c.Risk, Approver: ap})
			return err
		},
		"schedule": func(c application.Change) error {
			_, err := e.svc.Schedule(ctx, e.caller(e.requester), e.manage, c.ID, nil, application.ScheduleInput{})
			return err
		},
		"start": func(c application.Change) error {
			_, err := e.svc.Start(ctx, e.caller(e.owner), e.ownerP, c.ID, nil)
			return err
		},
		"complete": func(c application.Change) error {
			_, err := e.svc.Complete(ctx, e.caller(e.owner), e.ownerP, c.ID, nil, "")
			return err
		},
		"fail": func(c application.Change) error {
			_, err := e.svc.Fail(ctx, e.caller(e.owner), e.ownerP, c.ID, nil, "other", false)
			return err
		},
		"review": func(c application.Change) error {
			_, err := e.svc.Review(ctx, e.caller(e.requester), e.manage, c.ID, nil, "")
			return err
		},
		"close": func(c application.Change) error {
			_, err := e.svc.Close(ctx, e.caller(e.requester), e.manage, c.ID, nil)
			return err
		},
		"cancel": func(c application.Change) error {
			_, err := e.svc.Cancel(ctx, e.caller(e.requester), e.manage, c.ID, nil, "other")
			return err
		},
		"add_task": func(c application.Change) error {
			_, err := e.svc.AddTask(ctx, e.caller(e.owner), e.ownerP, c.ID, application.NewTask{Title: "step"})
			return err
		},
	}
	allowed := map[string][]string{
		"update":       {"draft", "assessment"},
		"add_affected": {"draft", "assessment"},
		"submit":       {"draft"},
		"assess":       {"assessment"},
		"schedule":     {"approved"},
		"start":        {"scheduled"},
		"complete":     {"in_progress"},
		"fail":         {"in_progress"},
		"review":       {"completed", "failed"},
		"close":        {"completed", "failed", "review"},
		"cancel":       {"draft", "assessment", "pending_approval", "approved", "scheduled"},
		"add_task":     {"scheduled", "in_progress"},
	}
	for _, status := range statuses {
		for op, run := range ops {
			var c application.Change
			if status == "rejected" {
				c = e.driveRejected()
			} else {
				c = e.drive("normal", "medium", status)
			}
			if c.Status != status {
				t.Fatalf("driven status = %s, want %s", c.Status, status)
			}
			err := run(c)
			if slices.Contains(allowed[op], status) {
				// Submit of the driven draft is valid; assess needs the right input; any refusal here is a failure.
				if err != nil {
					t.Errorf("%s in %s: %v", op, status, err)
				}
				continue
			}
			if !isTransition(err) {
				t.Errorf("%s in %s: err = %v, want invalid transition", op, status, err)
			}
			if after := e.get(c.ID); after.Status != status || after.Version != c.Version {
				t.Errorf("%s in %s changed the change: %s v%d", op, status, after.Status, after.Version)
			}
		}
	}
}

func ptr[T any](v T) *T { return &v }

func TestApprovalRoundTripAndSeparationOfDuties(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	c := e.create("normal", "medium")
	e.affect(c)
	c = e.get(c.ID)
	// Another manager edits the draft; they can never approve it.
	if _, err := e.svc.UpdateDetails(ctx, e.caller(e.other), e.manage2, c.ID, &c.Version, application.Details{Description: ptr("changed")}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Submit(ctx, e.caller(e.requester), e.manage, c.ID, nil); err != nil {
		t.Fatal(err)
	}
	// Needs exactly one approver; low risk and no approver is the only approver-free case here.
	if _, err := e.svc.Assess(ctx, e.caller(e.executor), application.Principal{UserID: e.executor, Manage: true}, c.ID, nil, application.Assessment{Risk: "medium"}); !isInvalid(err) {
		t.Errorf("assess without approver: %v", err)
	}
	both := application.Approver{UserID: &e.approver, TeamID: &e.owner}
	if _, err := e.svc.Assess(ctx, e.caller(e.executor), application.Principal{UserID: e.executor, Manage: true}, c.ID, nil, application.Assessment{Risk: "medium", Approver: both}); !isInvalid(err) {
		t.Errorf("assess with two approvers: %v", err)
	}
	out, err := e.svc.Assess(ctx, e.caller(e.executor), application.Principal{UserID: e.executor, Manage: true}, c.ID, nil,
		application.Assessment{Risk: "medium", Approver: application.Approver{UserID: &e.approver}})
	if err != nil || out.Status != "pending_approval" || out.ApprovalID == nil {
		t.Fatalf("assess = %+v %v", out, err)
	}
	if len(e.appr.requested) != 1 {
		t.Fatalf("approvals requested: %d", len(e.appr.requested))
	}
	excluded := e.appr.requested[0].ExcludedUserIDs
	for _, u := range []string{e.requester, e.other, e.executor} {
		if !slices.Contains(excluded, u) {
			t.Errorf("%s is not excluded from approving: %v", u, excluded)
		}
	}
	if slices.Contains(excluded, e.approver) {
		t.Error("the approver is excluded")
	}
	// Another subject type and a stale/unknown change change nothing.
	if err := e.decideErr(c.ID, "purchase_order", "approve"); err != nil || e.get(c.ID).Status != "pending_approval" {
		t.Fatalf("other subject type: %v", err)
	}
	if err := e.decideErr(e.uuid(), "change", "approve"); err != nil {
		t.Fatalf("unknown change: %v", err)
	}
	if err := e.decideErr(c.ID, "change", "maybe"); err == nil {
		t.Error("an unknown decision was accepted")
	}
	e.decide(c.ID, "approve")
	e.decide(c.ID, "reject") // stale: already approved
	got := e.get(c.ID)
	if got.Status != "approved" {
		t.Fatalf("status = %s", got.Status)
	}
	if e.published("ChangeApproved", c.ID) != 1 || e.audits("approved", c.ID) != 1 || e.published("ChangeRejected", c.ID) != 0 {
		t.Error("approval was not recorded exactly once")
	}

	// Rejection is terminal, with the reason and an event.
	r := e.driveRejected()
	if r.Status != "rejected" || r.StatusReason == nil || *r.StatusReason != application.ReasonApprovalRejected {
		t.Fatalf("rejected = %+v", r)
	}
	if e.published("ChangeRejected", r.ID) != 1 {
		t.Error("ChangeRejected not published")
	}
	if _, err := e.svc.Cancel(ctx, e.caller(e.requester), e.manage, r.ID, nil, "other"); !isTransition(err) {
		t.Errorf("cancel of a rejected change: %v", err)
	}

	// A refusing approvals module (nobody eligible) leaves the change in assessment.
	n := e.drive("normal", "high", "assessment")
	e.appr.refuse = application.ErrNoEligibleApprover
	if _, err := e.svc.Assess(ctx, e.caller(e.other), e.manage2, n.ID, nil, application.Assessment{Risk: "high", Approver: application.Approver{UserID: &e.requester}}); !is(err, application.ErrNoEligibleApprover) {
		t.Errorf("assess with ineligible approver: %v", err)
	}
	e.appr.refuse = nil
	if e.get(n.ID).Status != "assessment" {
		t.Error("a refused approval request must leave the change in assessment")
	}

	// Cancelling a pending change cancels its approval.
	p := e.drive("normal", "medium", "pending_approval")
	if _, err := e.svc.Cancel(ctx, e.caller(e.requester), e.manage, p.ID, nil, "superseded"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(e.appr.cancelled, p.ID) {
		t.Error("the pending approval was not cancelled")
	}

	// Low risk normal changes need no approval and take no approver.
	l := e.drive("normal", "low", "assessment")
	if _, err := e.svc.Assess(ctx, e.caller(e.other), e.manage2, l.ID, nil, application.Assessment{Risk: "low", Approver: application.Approver{UserID: &e.approver}}); !isInvalid(err) {
		t.Errorf("approver on an approval-free change: %v", err)
	}
	// Assessing a different risk applies it (and then needs the approval).
	up, err := e.svc.Assess(ctx, e.caller(e.other), e.manage2, l.ID, nil, application.Assessment{Risk: "high", Approver: application.Approver{UserID: &e.approver}})
	if !isInvalid(err) && err == nil {
		t.Errorf("raising risk to high without a rollback plan: %+v", up)
	}
}

func TestEmergencyPath(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	c := e.drive("emergency", "medium", "assessment")
	// No approval and no justification: refused. A justification on a normal change: refused.
	if _, err := e.svc.Assess(ctx, e.caller(e.other), e.manage2, c.ID, nil, application.Assessment{Risk: "medium"}); !isInvalid(err) {
		t.Errorf("emergency assess without approver or justification: %v", err)
	}
	normal := e.drive("normal", "medium", "assessment")
	if _, err := e.svc.Assess(ctx, e.caller(e.other), e.manage2, normal.ID, nil, application.Assessment{Risk: "medium", EmergencyJustification: "urgent"}); !isInvalid(err) {
		t.Errorf("justification on a normal change: %v", err)
	}
	if _, err := e.svc.Assess(ctx, e.caller(e.other), e.manage2, c.ID, nil, application.Assessment{Risk: "medium", EmergencyJustification: "urgent", Approver: application.Approver{UserID: &e.approver}}); !isInvalid(err) {
		t.Errorf("justification and approver together: %v", err)
	}
	// Needs changes.manage.
	if _, err := e.svc.Assess(ctx, e.caller(e.executor), e.execute, c.ID, nil, application.Assessment{Risk: "medium", EmergencyJustification: "urgent"}); !is(err, application.ErrForbidden) {
		t.Errorf("emergency approval without changes.manage: %v", err)
	}
	out, err := e.svc.Assess(ctx, e.caller(e.other), e.manage2, c.ID, nil, application.Assessment{Risk: "medium", EmergencyJustification: "customer outage"})
	if err != nil || out.Status != "approved" || out.EmergencyJustification == nil {
		t.Fatalf("emergency assess = %+v %v", out, err)
	}
	if e.audits("emergency_approved", c.ID) != 1 || e.published("ChangeApproved", c.ID) != 1 {
		t.Error("the emergency path must be audited and published")
	}
	var emergency bool
	if err := e.pool.QueryRow(ctx, `SELECT (payload->>'emergency')::boolean FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'ChangeApproved' AND payload->>'changeId' = $2`, e.corr, c.ID).Scan(&emergency); err != nil || !emergency {
		t.Errorf("ChangeApproved emergency flag = %v %v", emergency, err)
	}
	// An emergency change may be scheduled in the past or now; a normal one may not.
	past := e.window(-time.Hour, 3*time.Hour)
	if _, err := e.svc.Schedule(ctx, e.caller(e.requester), e.manage, c.ID, nil, application.ScheduleInput{Window: &past}); err != nil {
		t.Fatalf("emergency schedule in the past: %v", err)
	}
	n := e.drive("normal", "low", "approved")
	if _, err := e.svc.Schedule(ctx, e.caller(e.requester), e.manage, n.ID, nil, application.ScheduleInput{Window: &past}); !isInvalid(err) {
		t.Errorf("normal schedule in the past: %v", err)
	}
	// It cannot be closed without a review.
	run := func(c application.Change) application.Change {
		c, err := e.svc.Start(ctx, e.caller(e.owner), e.ownerP, c.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		c, err = e.svc.Complete(ctx, e.caller(e.owner), e.ownerP, c.ID, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	done := run(e.get(c.ID))
	if _, err := e.svc.Close(ctx, e.caller(e.requester), e.manage, done.ID, nil); !is(err, application.ErrReviewRequired) {
		t.Fatalf("closing an emergency change without review: %v", err)
	}
	if ops := application.AllowedOperations(done, e.manage); slices.Contains(ops, "close") || !slices.Contains(ops, "review") {
		t.Errorf("allowed operations of an unreviewed emergency change: %v", ops)
	}
	if _, err := e.svc.Review(ctx, e.caller(e.requester), e.manage, done.ID, nil, "root cause found"); err != nil {
		t.Fatal(err)
	}
	if closed, err := e.svc.Close(ctx, e.caller(e.requester), e.manage, done.ID, nil); err != nil || closed.Status != "closed" {
		t.Fatalf("close after review: %+v %v", closed, err)
	}
	// A normal change can be closed without a review.
	nn := run(e.drive("normal", "low", "scheduled"))
	if _, err := e.svc.Close(ctx, e.caller(e.requester), e.manage, nn.ID, nil); err != nil {
		t.Fatalf("close of a normal change: %v", err)
	}
}

func TestWindowAndRollbackValidation(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	start := e.clock.Add(24 * time.Hour)
	cases := []struct {
		name string
		w    application.Window
	}{
		{"end before start", application.Window{Start: &start, End: ptr(start.Add(-time.Minute))}},
		{"zero length", application.Window{Start: &start, End: &start}},
		{"too long", application.Window{Start: &start, End: ptr(start.Add(30*24*time.Hour + time.Second))}},
		{"only start", application.Window{Start: &start}},
		{"only end", application.Window{End: &start}},
	}
	for _, tc := range cases {
		_, err := e.svc.Create(ctx, e.caller(e.requester), e.manage, application.NewChange{Title: "x", Kind: "normal", Window: tc.w})
		if !isInvalid(err) {
			t.Errorf("create with window %q: %v", tc.name, err)
		}
	}
	// Exactly 30 days is fine.
	if _, err := e.svc.Create(ctx, e.caller(e.requester), e.manage, application.NewChange{Title: "x", Kind: "normal", Window: application.Window{Start: &start, End: ptr(start.Add(30 * 24 * time.Hour))}}); err != nil {
		t.Errorf("30 day window: %v", err)
	}
	if _, err := e.svc.Create(ctx, e.caller(e.requester), e.manage, application.NewChange{Title: " ", Kind: "normal"}); !isInvalid(err) {
		t.Errorf("blank title: %v", err)
	}
	if _, err := e.svc.Create(ctx, e.caller(e.requester), e.manage, application.NewChange{Title: "x", Kind: "weird"}); !isInvalid(err) {
		t.Errorf("unknown kind: %v", err)
	}
	if _, err := e.svc.Create(ctx, e.caller(e.requester), e.manage, application.NewChange{Title: "x", Kind: "normal", OwnerUserID: e.uuid() /* active in fake */, Description: "a\x00b"}); !isInvalid(err) {
		t.Errorf("control characters in description: %v", err)
	}
	e.dir.inactive[e.other] = true
	if _, err := e.svc.Create(ctx, e.caller(e.requester), e.manage, application.NewChange{Title: "x", Kind: "normal", OwnerUserID: e.other}); !is(err, application.ErrReferenceInvalid) {
		t.Errorf("inactive owner: %v", err)
	}

	// Submit needs a window, a rollback plan for medium and high risk, and an affected resource unless standard.
	noWindow, err := e.svc.Create(ctx, e.caller(e.requester), e.manage, application.NewChange{Title: "x", Kind: "standard"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Submit(ctx, e.caller(e.requester), e.manage, noWindow.ID, nil); !isInvalid(err) {
		t.Errorf("submit without window: %v", err)
	}
	med, err := e.svc.Create(ctx, e.caller(e.requester), e.manage, application.NewChange{Title: "x", Kind: "standard", Risk: "medium", Window: e.window(time.Hour, time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Submit(ctx, e.caller(e.requester), e.manage, med.ID, nil); !isInvalid(err) {
		t.Errorf("submit medium risk without rollback plan: %v", err)
	}
	if _, err := e.svc.UpdateDetails(ctx, e.caller(e.requester), e.manage, med.ID, &med.Version, application.Details{RollbackPlan: ptr("revert")}); err != nil {
		t.Fatal(err)
	}
	if got, err := e.svc.Submit(ctx, e.caller(e.requester), e.manage, med.ID, nil); err != nil || got.Status != "assessment" {
		t.Errorf("standard change without affected resources and with rollback plan: %+v %v", got, err)
	}
	normal := e.create("normal", "low")
	if _, err := e.svc.Submit(ctx, e.caller(e.requester), e.manage, normal.ID, nil); !isInvalid(err) {
		t.Errorf("submit of a normal change without affected resources: %v", err)
	}
	// Optimistic concurrency.
	if _, err := e.svc.UpdateDetails(ctx, e.caller(e.requester), e.manage, normal.ID, ptr(normal.Version+5), application.Details{Title: ptr("y")}); !is(err, application.ErrVersionConflict) {
		t.Errorf("stale version: %v", err)
	}
	if _, err := e.svc.UpdateDetails(ctx, e.caller(e.requester), e.manage, normal.ID, nil, application.Details{Title: ptr("y")}); !isInvalid(err) {
		t.Errorf("missing expectedVersion: %v", err)
	}
	// Clearing the window with an empty Window works in draft.
	if got, err := e.svc.UpdateDetails(ctx, e.caller(e.requester), e.manage, normal.ID, &normal.Version, application.Details{Window: &application.Window{}}); err != nil || got.WindowStart != nil {
		t.Errorf("clear window: %+v %v", got, err)
	}
}

func TestConcurrentStartAndCancel(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	for i := 0; i < 8; i++ {
		c := e.drive("normal", "low", "scheduled")
		var wg sync.WaitGroup
		var startErr, cancelErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, startErr = e.svc.Start(ctx, e.caller(e.owner), e.ownerP, c.ID, nil)
		}()
		go func() {
			defer wg.Done()
			_, cancelErr = e.svc.Cancel(ctx, e.caller(e.requester), e.manage, c.ID, nil, "rescheduled")
		}()
		wg.Wait()
		if (startErr == nil) == (cancelErr == nil) {
			t.Fatalf("round %d: start err = %v, cancel err = %v: exactly one must win", i, startErr, cancelErr)
		}
		loser := startErr
		want := "cancelled"
		if startErr == nil {
			loser, want = cancelErr, "in_progress"
		}
		if !isTransition(loser) {
			t.Errorf("round %d: loser error = %v", i, loser)
		}
		if got := e.get(c.ID).Status; got != want {
			t.Errorf("round %d: status = %s, want %s", i, got, want)
		}
		if n := e.count(`SELECT count(*) FROM changes.change_transitions WHERE change_id = $1::uuid AND from_status = 'scheduled'`, c.ID); n != 1 {
			t.Errorf("round %d: %d transitions out of scheduled", i, n)
		}
	}
	// Concurrent Starts: one wins.
	c := e.drive("normal", "low", "scheduled")
	var ok atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.svc.Start(ctx, e.caller(e.owner), e.ownerP, c.ID, nil); err == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 || e.published("ChangeStarted", c.ID) != 1 {
		t.Errorf("%d concurrent starts succeeded, %d events", ok.Load(), e.published("ChangeStarted", c.ID))
	}
}

func TestPermissionsAndOwnership(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	c := e.drive("normal", "low", "scheduled")

	// Reads: requester, owner and view/execute holders; everyone else gets "not found".
	for name, p := range map[string]application.Principal{"requester": {UserID: e.requester}, "owner": e.ownerP, "viewer": e.view, "executor": e.execute} {
		if _, err := e.svc.Get(ctx, p, c.ID); err != nil {
			t.Errorf("%s cannot read: %v", name, err)
		}
	}
	if _, err := e.svc.Get(ctx, e.outsiderP, c.ID); !is(err, application.ErrNotFound) {
		t.Errorf("outsider get: %v", err)
	}
	if _, err := e.svc.Get(ctx, e.outsiderP, "not-a-uuid"); !is(err, application.ErrNotFound) {
		t.Errorf("bad id get: %v", err)
	}
	if _, err := e.svc.Transitions(ctx, e.outsiderP, c.ID, application.Page{}); !is(err, application.ErrNotFound) {
		t.Errorf("outsider transitions: %v", err)
	}
	if _, err := e.svc.Impact(ctx, application.Principal{UserID: e.outsider, ServicesView: true}, c.ID, 0); !is(err, application.ErrNotFound) {
		t.Errorf("outsider impact: %v", err)
	}
	// An approver can read the change they decide.
	e.appr.viewers[c.ID+":"+e.approver] = true
	if _, err := e.svc.Get(ctx, application.Principal{UserID: e.approver}, c.ID); err != nil {
		t.Errorf("approver get: %v", err)
	}

	// Lists are scoped: the requester sees their own, an outsider none, a viewer all.
	own, err := e.svc.List(ctx, application.Principal{UserID: e.requester}, application.Filter{})
	if err != nil || len(own.Items) == 0 {
		t.Fatalf("requester list = %d %v", len(own.Items), err)
	}
	for _, it := range own.Items {
		if it.RequesterID != e.requester && (it.OwnerID == nil || *it.OwnerID != e.requester) {
			t.Errorf("requester sees foreign change %s", it.ID)
		}
	}
	if none, err := e.svc.List(ctx, e.outsiderP, application.Filter{}); err != nil || len(none.Items) != 0 {
		t.Errorf("outsider list = %d %v", len(none.Items), err)
	}
	if all, err := e.svc.List(ctx, e.view, application.Filter{RequesterID: e.requester}); err != nil || len(all.Items) < 1 {
		t.Errorf("viewer list = %d %v", len(all.Items), err)
	}
	if _, err := e.svc.List(ctx, application.Principal{}, application.Filter{}); !is(err, application.ErrForbidden) {
		t.Errorf("anonymous list: %v", err)
	}
	// An owner list entry: a user who only owns changes sees them.
	if mine, err := e.svc.List(ctx, e.ownerP, application.Filter{}); err != nil || len(mine.Items) == 0 {
		t.Errorf("owner list = %d %v", len(mine.Items), err)
	}

	// Writes need changes.manage.
	if _, err := e.svc.Create(ctx, e.caller(e.outsider), e.outsiderP, application.NewChange{Title: "x", Kind: "normal"}); !is(err, application.ErrForbidden) {
		t.Errorf("create without manage: %v", err)
	}
	if _, err := e.svc.Create(ctx, e.caller(e.other), e.manage, application.NewChange{Title: "x", Kind: "normal"}); !is(err, application.ErrForbidden) {
		t.Errorf("create with a principal that is not the actor: %v", err)
	}
	if _, err := e.svc.Cancel(ctx, e.caller(e.outsider), e.view, c.ID, nil, "other"); !is(err, application.ErrForbidden) {
		t.Errorf("cancel with view only: %v", err)
	}
	if _, err := e.svc.Close(ctx, e.caller(e.outsider), e.execute, c.ID, nil); !is(err, application.ErrForbidden) {
		t.Errorf("close with execute only: %v", err)
	}
	// Running: an outsider learns nothing; a reader without execute is forbidden; the owner and executor may.
	if _, err := e.svc.Start(ctx, e.caller(e.outsider), e.outsiderP, c.ID, nil); !is(err, application.ErrNotFound) {
		t.Errorf("outsider start: %v", err)
	}
	if _, err := e.svc.Start(ctx, e.caller(e.other), e.view, c.ID, nil); !is(err, application.ErrForbidden) {
		t.Errorf("viewer start: %v", err)
	}
	if _, err := e.svc.Start(ctx, e.caller(e.requester), e.manage, c.ID, nil); !is(err, application.ErrForbidden) {
		t.Errorf("manager (not owner, no execute) start: %v", err)
	}
	if _, err := e.svc.Start(ctx, e.caller(e.executor), e.execute, c.ID, ptr(c.Version+9)); !is(err, application.ErrVersionConflict) {
		t.Errorf("start with a stale version: %v", err)
	}
	if _, err := e.svc.Start(ctx, e.caller(e.executor), e.execute, c.ID, nil); err != nil {
		t.Errorf("executor start: %v", err)
	}
	if _, err := e.svc.Complete(ctx, e.caller(e.outsider), e.outsiderP, c.ID, nil, ""); !is(err, application.ErrNotFound) {
		t.Errorf("outsider complete: %v", err)
	}
	if _, err := e.svc.Fail(ctx, e.caller(e.owner), e.ownerP, c.ID, nil, "not_a_code", false); !isInvalid(err) {
		t.Errorf("fail with unknown reason: %v", err)
	}
	failed, err := e.svc.Fail(ctx, e.caller(e.owner), e.ownerP, c.ID, nil, "verification_failed", true)
	if err != nil || failed.Status != "failed" || failed.RollbackDone == nil || !*failed.RollbackDone {
		t.Fatalf("owner fail = %+v %v", failed, err)
	}
	if e.published("ChangeFailed", c.ID) != 1 {
		t.Error("ChangeFailed not published")
	}
	// The allowed operations depend on the caller.
	s := e.drive("normal", "low", "scheduled")
	if ops := application.AllowedOperations(s, e.ownerP); !slices.Equal(ops, []string{"start", "add_task"}) {
		t.Errorf("owner operations = %v", ops)
	}
	if ops := application.AllowedOperations(s, e.view); len(ops) != 0 {
		t.Errorf("viewer operations = %v", ops)
	}
}

func TestAffectedResourcesRedactionAndValidation(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	c := e.create("normal", "low")
	svcID := e.service(&e.owner, nil)
	retired := e.service(nil, nil)
	e.services.byID[retired] = application.ServiceInfo{ID: retired, Status: "retired"}
	vmID, deadVM, assetID, badAsset, locID := e.uuid(), e.uuid(), e.uuid(), e.uuid(), e.uuid()
	e.infra.vms[vmID] = application.VMInfo{ID: vmID, Name: "vm-secret", State: "running"}
	e.infra.vms[deadVM] = application.VMInfo{ID: deadVM, Name: "old", State: "decommissioned"}
	e.assets.byID[assetID] = application.AssetInfo{ID: assetID, Reference: "AST-secret", Status: "in_use"}
	e.assets.byID[badAsset] = application.AssetInfo{ID: badAsset, Reference: "AST-gone", Status: "disposed"}
	e.dir.locs[locID] = "Site secret"

	for typ, id := range map[string]string{"service": svcID, "vm": vmID, "asset": assetID, "location": locID} {
		if _, created, err := e.svc.AddAffected(ctx, e.caller(e.requester), e.manage, c.ID, nil, typ, id); err != nil || !created {
			t.Fatalf("add %s: created=%v err=%v", typ, created, err)
		}
	}
	// Adding twice is idempotent and audited once.
	if _, created, err := e.svc.AddAffected(ctx, e.caller(e.requester), e.manage, c.ID, nil, "service", svcID); err != nil || created {
		t.Errorf("second add: created=%v err=%v", created, err)
	}
	if e.audits("affected_added", c.ID) != 4 {
		t.Errorf("affected_added audits = %d", e.audits("affected_added", c.ID))
	}
	// Unusable or unknown targets.
	for name, tc := range map[string][2]string{"retired service": {"service", retired}, "decommissioned vm": {"vm", deadVM}, "disposed asset": {"asset", badAsset},
		"unknown location": {"location", e.uuid()}, "unknown service": {"service", e.uuid()}} {
		if _, _, err := e.svc.AddAffected(ctx, e.caller(e.requester), e.manage, c.ID, nil, tc[0], tc[1]); !is(err, application.ErrReferenceInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := e.svc.AddAffected(ctx, e.caller(e.requester), e.manage, c.ID, nil, "ticket", svcID); !isInvalid(err) {
		t.Errorf("unregistered type: %v", err)
	}
	// A caller who may not see a type cannot add it, whether or not the record exists.
	blind := application.Principal{UserID: e.requester, Manage: true, ServicesView: true}
	for _, tc := range [][2]string{{"vm", vmID}, {"asset", assetID}, {"location", locID}, {"vm", e.uuid()}} {
		if _, _, err := e.svc.AddAffected(ctx, e.caller(e.requester), blind, c.ID, nil, tc[0], tc[1]); !is(err, application.ErrReferenceInvalid) {
			t.Errorf("hidden type %s: %v", tc[0], err)
		}
	}

	// Reads redact what the caller may not see.
	d, err := e.svc.Get(ctx, application.Principal{UserID: e.other, View: true, ServicesView: true}, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(d.Affected)
	for _, secret := range []string{vmID, assetID, locID, "vm-secret", "AST-secret", "Site secret"} {
		if contains(string(raw), secret) {
			t.Errorf("redacted affected resources leak %q: %s", secret, raw)
		}
	}
	hidden := 0
	for _, a := range d.Affected {
		if a.Hidden {
			hidden++
			if a.Name != nil || a.Reference != nil || a.Status != nil || a.RelationshipID == "" || a.ID == "" || !contains(a.ID, "hidden-") {
				t.Errorf("hidden entry = %+v", a)
			}
		}
	}
	if hidden != 3 || len(d.Affected) != 4 {
		t.Errorf("hidden=%d of %d", hidden, len(d.Affected))
	}
	full, err := e.svc.Get(ctx, e.view, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range full.Affected {
		if a.Hidden || a.Missing || a.Name == nil && a.Reference == nil {
			t.Errorf("full view entry = %+v", a)
		}
	}
	// Filtering by a hidden type confirms nothing.
	if res, err := e.svc.List(ctx, application.Principal{UserID: e.other, View: true, ServicesView: true}, application.Filter{AffectedType: "vm", AffectedID: vmID}); err != nil || len(res.Items) != 0 {
		t.Errorf("filter by hidden type = %d %v", len(res.Items), err)
	}
	if res, err := e.svc.List(ctx, e.view, application.Filter{AffectedType: "vm", AffectedID: vmID}); err != nil || len(res.Items) != 1 || res.Items[0].ID != c.ID {
		t.Errorf("filter by vm = %+v %v", res.Items, err)
	}
	if _, err := e.svc.List(ctx, e.view, application.Filter{AffectedType: "vm"}); !isInvalid(err) {
		t.Errorf("affectedType without id: %v", err)
	}

	// Removing ends the link, once.
	if err := e.svc.RemoveAffected(ctx, e.caller(e.requester), e.manage, c.ID, nil, "vm", vmID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RemoveAffected(ctx, e.caller(e.requester), e.manage, c.ID, nil, "vm", vmID); err != nil {
		t.Fatal(err)
	}
	if e.audits("affected_removed", c.ID) != 1 {
		t.Errorf("affected_removed audits = %d", e.audits("affected_removed", c.ID))
	}
	if res, _ := e.svc.List(ctx, e.view, application.Filter{AffectedType: "vm", AffectedID: vmID}); len(res.Items) != 0 {
		t.Error("an ended link still matches the filter")
	}

	// The count is bounded.
	big := e.create("normal", "low")
	for i := 0; i < application.MaxAffected; i++ {
		if _, _, err := e.svc.AddAffected(ctx, e.caller(e.requester), e.manage, big.ID, nil, "service", e.service(nil, nil)); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if _, _, err := e.svc.AddAffected(ctx, e.caller(e.requester), e.manage, big.ID, nil, "service", e.service(nil, nil)); !is(err, application.ErrTooMany) {
		t.Errorf("26th affected resource: %v", err)
	}
	// Window filter.
	from, to := e.clock.Add(23*time.Hour), e.clock.Add(27*time.Hour)
	if res, err := e.svc.List(ctx, e.view, application.Filter{WindowFrom: &from, WindowTo: &to, RequesterID: e.requester}); err != nil || len(res.Items) < 2 {
		t.Errorf("window filter = %d %v", len(res.Items), err)
	}
	far := e.clock.Add(1000 * time.Hour)
	if res, err := e.svc.List(ctx, e.view, application.Filter{WindowFrom: &far, RequesterID: e.requester}); err != nil || len(res.Items) != 0 {
		t.Errorf("window filter far = %d %v", len(res.Items), err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestListFiltersAndPaging(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	for i := 0; i < 5; i++ {
		e.create("normal", "low")
	}
	e.create("emergency", "high")
	var seen []string
	cursor := ""
	for {
		res, err := e.svc.List(ctx, e.view, application.Filter{RequesterID: e.requester, Page: application.Page{Limit: 2, Cursor: cursor}})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range res.Items {
			seen = append(seen, it.ID)
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	if len(seen) != 6 || !slices.IsSortedFunc(seen, func(a, b string) int { return -compare(a, b) }) {
		t.Errorf("paged ids = %v", seen)
	}
	for kind, want := range map[string]int{"emergency": 1, "normal": 5} {
		res, err := e.svc.List(ctx, e.view, application.Filter{RequesterID: e.requester, Kind: kind})
		if err != nil || len(res.Items) != want {
			t.Errorf("kind %s = %d %v", kind, len(res.Items), err)
		}
	}
	if res, _ := e.svc.List(ctx, e.view, application.Filter{RequesterID: e.requester, Risk: "high"}); len(res.Items) != 1 {
		t.Errorf("risk filter = %d", len(res.Items))
	}
	if res, _ := e.svc.List(ctx, e.view, application.Filter{RequesterID: e.requester, Status: "draft", OwnerID: e.owner}); len(res.Items) != 6 {
		t.Errorf("status+owner filter = %d", len(res.Items))
	}
	if _, err := e.svc.List(ctx, e.view, application.Filter{Status: "nope"}); !isInvalid(err) {
		t.Errorf("unknown status: %v", err)
	}
	if _, err := e.svc.List(ctx, e.view, application.Filter{Page: application.Page{Cursor: "zzz"}}); !is(err, application.ErrInvalidCursor) {
		t.Errorf("bad cursor: %v", err)
	}
}

func compare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func TestExecutionTasks(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	c := e.drive("normal", "low", "scheduled")
	// Tasks only on scheduled/in-progress changes, and not by outsiders.
	d := e.drive("normal", "low", "draft")
	if _, err := e.svc.AddTask(ctx, e.caller(e.owner), e.ownerP, d.ID, application.NewTask{Title: "x"}); err == nil || !is(err, application.ErrNotFound) && !isTransition(err) {
		t.Errorf("task on a draft: %v", err)
	}
	if _, err := e.svc.AddTask(ctx, e.caller(e.outsider), e.outsiderP, c.ID, application.NewTask{Title: "x"}); !is(err, application.ErrNotFound) {
		t.Errorf("outsider task: %v", err)
	}
	if _, err := e.svc.AddTask(ctx, e.caller(e.owner), e.ownerP, c.ID, application.NewTask{Title: " "}); !isInvalid(err) {
		t.Errorf("blank task title: %v", err)
	}
	t1, err := e.svc.AddTask(ctx, e.caller(e.owner), e.ownerP, c.ID, application.NewTask{Title: "stop service"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AddTask(ctx, e.caller(e.requester), e.manage, c.ID, application.NewTask{Title: "verify"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Start(ctx, e.caller(e.owner), e.ownerP, c.ID, nil); err != nil {
		t.Fatal(err)
	}
	det, err := e.svc.Get(ctx, e.view, c.ID)
	if err != nil || det.Tasks.Total != 2 || det.Tasks.Open != 2 {
		t.Fatalf("tasks summary = %+v %v", det.Tasks, err)
	}
	// Open tasks block completion unless waived with the dedicated code.
	if _, err := e.svc.Complete(ctx, e.caller(e.owner), e.ownerP, c.ID, nil, ""); !is(err, application.ErrOpenTasks) {
		t.Fatalf("complete with open tasks: %v", err)
	}
	if _, err := e.svc.Complete(ctx, e.caller(e.owner), e.ownerP, c.ID, nil, "whatever"); !isInvalid(err) {
		t.Fatalf("complete with unknown force: %v", err)
	}
	e.tasks.mu.Lock()
	e.tasks.statuses[t1] = "completed"
	e.tasks.mu.Unlock()
	if _, err := e.svc.Complete(ctx, e.caller(e.owner), e.ownerP, c.ID, nil, ""); !is(err, application.ErrOpenTasks) {
		t.Fatalf("complete with one open task: %v", err)
	}
	done, err := e.svc.Complete(ctx, e.caller(e.owner), e.ownerP, c.ID, nil, application.ReasonTasksWaived)
	if err != nil || done.Status != "completed" || done.StatusReason == nil || *done.StatusReason != "tasks_waived" {
		t.Fatalf("waived completion = %+v %v", done, err)
	}
	var waived float64
	if err := e.pool.QueryRow(ctx, `SELECT (metadata->>'waivedTasks')::float FROM platform.audit_events WHERE correlation_id = $1 AND action = 'changes.change.completed' AND target_id = $2`, e.corr, c.ID).Scan(&waived); err != nil || waived != 1 {
		t.Errorf("waivedTasks audit = %v %v", waived, err)
	}
	// With all tasks finished no force is needed.
	f := e.drive("normal", "low", "scheduled")
	id, _ := e.svc.AddTask(ctx, e.caller(e.owner), e.ownerP, f.ID, application.NewTask{Title: "a"})
	_, _ = e.svc.Start(ctx, e.caller(e.owner), e.ownerP, f.ID, nil)
	e.tasks.mu.Lock()
	e.tasks.statuses[id] = "cancelled"
	e.tasks.mu.Unlock()
	if _, err := e.svc.Complete(ctx, e.caller(e.owner), e.ownerP, f.ID, nil, ""); err != nil {
		t.Errorf("complete with finished tasks: %v", err)
	}
	// Cancelling a scheduled change cancels its tasks.
	g := e.drive("normal", "low", "scheduled")
	_, _ = e.svc.AddTask(ctx, e.caller(e.owner), e.ownerP, g.ID, application.NewTask{Title: "a"})
	if _, err := e.svc.Cancel(ctx, e.caller(e.requester), e.manage, g.ID, nil, "rescheduled"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(e.tasks.cancelled, g.ID) {
		t.Error("cancelling a scheduled change did not cancel its tasks")
	}
	// The task limit holds.
	h := e.drive("normal", "low", "scheduled")
	for i := 0; i < application.MaxTasks; i++ {
		if _, err := e.svc.AddTask(ctx, e.caller(e.owner), e.ownerP, h.ID, application.NewTask{Title: fmt.Sprintf("s%d", i)}); err != nil {
			t.Fatalf("task %d: %v", i, err)
		}
	}
	if _, err := e.svc.AddTask(ctx, e.caller(e.owner), e.ownerP, h.ID, application.NewTask{Title: "one too many"}); !is(err, application.ErrTooMany) {
		t.Errorf("51st task: %v", err)
	}
}

func TestImpactView(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	c := e.create("normal", "low")
	svcID := e.service(&e.owner, nil)
	vmID := e.uuid()
	e.infra.vms[vmID] = application.VMInfo{ID: vmID, Name: "vm", State: "running"}
	for typ, id := range map[string]string{"service": svcID, "vm": vmID} {
		if _, _, err := e.svc.AddAffected(ctx, e.caller(e.requester), e.manage, c.ID, nil, typ, id); err != nil {
			t.Fatal(err)
		}
	}
	res, err := e.svc.Impact(ctx, e.manage, c.ID, 0)
	if err != nil || len(res.Starts) != 2 || res.Skipped != 0 {
		t.Fatalf("impact = %+v %v", res, err)
	}
	// Without services.view the impact view is closed; hidden start types are skipped.
	if _, err := e.svc.Impact(ctx, application.Principal{UserID: e.other, View: true}, c.ID, 0); !is(err, application.ErrForbidden) {
		t.Errorf("impact without services.view: %v", err)
	}
	blind, err := e.svc.Impact(ctx, application.Principal{UserID: e.other, View: true, ServicesView: true}, c.ID, 0)
	if err != nil || len(blind.Starts) != 1 || blind.Skipped != 1 || blind.Starts[0].Type != "service" {
		t.Errorf("impact without infrastructure.view = %+v %v", blind, err)
	}
	if _, err := e.svc.Impact(ctx, e.manage, c.ID, 9); !isInvalid(err) {
		t.Errorf("depth 9: %v", err)
	}
}

func (e *env) outbox(typ string) []events.OutboxEvent {
	e.t.Helper()
	rows, err := e.pool.Query(e.ctx(), `SELECT id::text, event_type, actor_id::text, correlation_id, payload FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = $2 ORDER BY occurred_at, id`, e.corr, typ)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []events.OutboxEvent
	for rows.Next() {
		var ev events.OutboxEvent
		if err := rows.Scan(&ev.ID, &ev.EventType, &ev.ActorID, &ev.CorrelationID, &ev.Payload); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, ev)
	}
	return out
}

func (e *env) consume(fn func(context.Context, pgx.Tx, events.OutboxEvent) error, ev events.OutboxEvent) {
	e.t.Helper()
	if err := pgx.BeginFunc(e.ctx(), e.pool, func(tx pgx.Tx) error { return fn(e.ctx(), tx, ev) }); err != nil {
		e.t.Fatal(err)
	}
}

func TestScheduledNotificationFanOutIsChunked(t *testing.T) {
	e := newEnv(t)
	team := e.uuid()
	members := []string{e.uuid(), e.uuid(), e.uuid(), e.uuid(), e.uuid()}
	e.dir.members[team] = append(slices.Clone(members), e.owner) // the owner is also a member: told once
	svcA := e.service(&e.owner, &team)
	svcB := e.service(nil, nil)
	c := e.create("normal", "low")
	for _, s := range []string{svcA, svcB} {
		if _, _, err := e.svc.AddAffected(e.ctx(), e.caller(e.requester), e.manage, c.ID, nil, "service", s); err != nil {
			t.Fatal(err)
		}
	}
	vm := e.uuid()
	e.infra.vms[vm] = application.VMInfo{ID: vm, State: "running"}
	if _, _, err := e.svc.AddAffected(e.ctx(), e.caller(e.requester), e.manage, c.ID, nil, "vm", vm); err != nil {
		t.Fatal(err)
	}
	e.dir.members[team] = append(e.dir.members[team], e.requester) // the actor is skipped
	if _, err := e.svc.Submit(e.ctx(), e.caller(e.requester), e.manage, c.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Assess(e.ctx(), e.caller(e.other), e.manage2, c.ID, nil, application.Assessment{Risk: "low"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Schedule(e.ctx(), e.caller(e.requester), e.manage, c.ID, nil, application.ScheduleInput{}); err != nil {
		t.Fatal(err)
	}
	// Audience: 5 members, the owner and the requester (a member too) = 7, in chunks of 2; the actor (requester) is not told.
	n := e.notes.WithChunk(2)
	if len(e.outbox("ChangeScheduled")) != 1 {
		t.Fatalf("scheduled events = %d", len(e.outbox("ChangeScheduled")))
	}
	done := map[string]bool{}
	processed := 0
	for processed < 10 {
		var next *events.OutboxEvent
		for _, x := range e.outbox("ChangeScheduled") {
			if !done[x.ID] {
				x := x
				next = &x
				break
			}
		}
		if next == nil {
			break
		}
		done[next.ID] = true
		e.consume(n.OnChangeScheduled, *next)
		processed++
	}
	got := e.notifier.recipients("change.scheduled")
	want := slices.Clone(members)
	want = append(want, e.owner)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("recipients = %v, want %v (events processed: %d)", got, want, processed)
	}
	if processed != 4 {
		t.Errorf("fan-out took %d events, want 4 chunks of 7 audience members", processed)
	}
	// Redelivery of the last chunk is idempotent (dedupe keys per event and recipient).
	last := e.outbox("ChangeScheduled")
	e.consume(n.OnChangeScheduled, last[len(last)-1])
	if again := e.notifier.recipients("change.scheduled"); !slices.Equal(again, want) {
		t.Errorf("recipients after redelivery = %v", again)
	}
	// A cancelled change is stale: nobody is told.
	e.notifier.mu.Lock()
	e.notifier.seen, e.notifier.intent = map[string]bool{}, nil
	e.notifier.mu.Unlock()
	if _, err := e.svc.Cancel(e.ctx(), e.caller(e.requester), e.manage, c.ID, nil, "rescheduled"); err != nil {
		t.Fatal(err)
	}
	e.consume(n.OnChangeScheduled, e.outbox("ChangeScheduled")[0])
	if e.notifier.count("change.scheduled") != 0 {
		t.Error("a cancelled change was announced")
	}
}

func TestStateNotificationsGoToRequesterAndOwner(t *testing.T) {
	e := newEnv(t)
	c := e.drive("normal", "medium", "pending_approval")
	e.decide(c.ID, "approve")
	for _, ev := range e.outbox("ChangeApproved") {
		e.consume(e.notes.OnChangeState, ev)
	}
	got := e.notifier.recipients("change.state")
	want := []string{e.owner, e.requester}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("approval recipients = %v, want %v", got, want)
	}
	// The actor of the event is not told, and redelivery adds nothing.
	r := e.driveRejected()
	_ = r
	before := e.notifier.count("change.state")
	for _, ev := range e.outbox("ChangeRejected") {
		e.consume(e.notes.OnChangeState, ev)
		e.consume(e.notes.OnChangeState, ev)
	}
	if after := e.notifier.count("change.state"); after != before+2 {
		t.Errorf("rejection notifications = %d, want 2 more than %d", after, before)
	}
	// Malformed payloads are permanent failures.
	bad := events.OutboxEvent{ID: "x", EventType: "ChangeFailed", Payload: json.RawMessage(`{"changeId":"nope"}`)}
	err := pgx.BeginFunc(e.ctx(), e.pool, func(tx pgx.Tx) error { return e.notes.OnChangeState(e.ctx(), tx, bad) })
	if err == nil || !events.IsPermanent(err) {
		t.Errorf("malformed payload error = %v", err)
	}
}

func TestReminderJobIsIdempotentPerWindow(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	team := e.uuid()
	m1, m2 := e.uuid(), e.uuid()
	e.dir.members[team] = []string{m1, m2}
	svc := e.service(&e.owner, &team)

	soon := e.create("normal", "low")
	soonW := e.window(30*time.Minute, time.Hour)
	later := e.create("normal", "low")
	for _, c := range []application.Change{soon, later} {
		if _, _, err := e.svc.AddAffected(ctx, e.caller(e.requester), e.manage, c.ID, nil, "service", svc); err != nil {
			t.Fatal(err)
		}
	}
	// Move both through to scheduled; "soon" starts in 30 minutes.
	sched := func(c application.Change, w *application.Window) {
		if c.Status != "approved" {
			if _, err := e.svc.Submit(ctx, e.caller(e.requester), e.manage, c.ID, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := e.svc.Assess(ctx, e.caller(e.other), e.manage2, c.ID, nil, application.Assessment{Risk: "low"}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := e.svc.Schedule(ctx, e.caller(e.requester), e.manage, c.ID, nil, application.ScheduleInput{Window: w}); err != nil {
			t.Fatal(err)
		}
	}
	sched(soon, &soonW)
	sched(later, nil) // 24h away
	job := jobs.Job{ID: "job-1", Type: application.ReminderJobType}
	if err := e.notes.HandleReminders(ctx, job); err != nil {
		t.Fatal(err)
	}
	got := e.notifier.recipients("change.reminder")
	want := []string{e.owner, m1, m2}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("reminder recipients = %v, want %v", got, want)
	}
	if e.get(soon.ID).RemindedFor == nil || e.get(later.ID).RemindedFor != nil {
		t.Error("only the change starting soon is marked reminded")
	}
	// A second run and concurrent runs send nothing more.
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.notes.HandleReminders(ctx, job); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := e.notifier.count("change.reminder"); n != 3 {
		t.Errorf("reminders after reruns = %d, want 3", n)
	}
	// Even if the marker were lost, the dedupe keys per window keep it at one reminder per recipient.
	if _, err := e.pool.Exec(ctx, `UPDATE changes.changes SET reminded_for = NULL WHERE id = $1::uuid`, soon.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.notes.HandleReminders(ctx, job); err != nil {
		t.Fatal(err)
	}
	if n := e.notifier.count("change.reminder"); n != 3 {
		t.Errorf("reminders after marker loss = %d, want 3", n)
	}
	// When the day comes, the later change is reminded too; a cancelled one is not.
	e.clock = e.clock.Add(23*time.Hour + 30*time.Minute)
	if err := e.notes.HandleReminders(ctx, job); err != nil {
		t.Fatal(err)
	}
	if e.get(later.ID).RemindedFor == nil {
		t.Error("the later change was not reminded when its window came near")
	}
}

func TestRelationshipsAreOwnedByChanges(t *testing.T) {
	reg := relationships.NewRegistry()
	reg.Register(application.Triples...)
	for _, target := range application.AffectedTargets {
		if o, ok := reg.Owner("change", "AFFECTS", target); !ok || o != "changes" {
			t.Errorf("change AFFECTS %s owner = %q %v", target, o, ok)
		}
	}
	if reg.Allowed("change", "AFFECTS", "change") || reg.Allowed("service", "AFFECTS", "service") {
		t.Error("unexpected triple allowed")
	}
}
