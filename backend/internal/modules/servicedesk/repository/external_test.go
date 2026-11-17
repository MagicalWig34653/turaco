package repository_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/autotask"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/externalrefs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

type gw struct{ f *autotask.Fake }

func (g gw) Push(ctx context.Context, t application.TicketPayload, externalID string) (string, error) {
	id, err := g.f.Upsert(ctx, autotask.Ticket{Reference: t.Reference, Title: t.Title, Description: t.Description, Status: t.Status, Priority: t.Priority, Resolution: t.Resolution}, externalID)
	var ae *autotask.Error
	if errors.As(err, &ae) {
		return "", &application.GatewayError{Message: ae.Message, Permanent: ae.Permanent}
	}
	return id, err
}

func TestExternalSyncPushStateRetryAndInbound(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	fake := autotask.NewFake()
	sync := application.NewExternalSync(e.svc, repository.New(e.pool), e.pool, gw{fake}, true)
	t.Cleanup(func() {
		_, _ = e.pool.Exec(ctx, `DELETE FROM platform.external_events WHERE event_id LIKE $1`, e.corr+"%")
		_, _ = e.pool.Exec(ctx, `DELETE FROM platform.external_references WHERE entity_type = 'ticket' AND entity_id IN (SELECT id FROM servicedesk.tickets WHERE reporter_user_id = $1::uuid)`, e.alice)
		_, _ = e.pool.Exec(ctx, `DELETE FROM platform.jobs WHERE job_type = $1 AND payload->>'ticketId' IN (SELECT id::text FROM servicedesk.tickets WHERE reporter_user_id = $2::uuid)`, application.PushJobType, e.alice)
	})
	tk := e.raise()
	job := jobs.Job{Payload: []byte(`{"ticketId":"` + tk.ID + `"}`)}

	// A change requests a push (deduplicated); the push creates the external ticket and stores the mapping.
	req := func() {
		if err := sync.Retry(ctx, e.c(e.agent), e.staff(), tk.ID); err != nil {
			t.Fatal(err)
		}
	}
	req()
	req()
	var queued int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM platform.jobs WHERE job_type = $1 AND payload->>'ticketId' = $2`, application.PushJobType, tk.ID).Scan(&queued)
	if queued != 2 {
		t.Errorf("%d push jobs queued, want one per requested version", queued)
	}
	if err := sync.HandlePush(ctx, job); err != nil {
		t.Fatal(err)
	}
	st, err := sync.StateOf(ctx, application.Principal{UserID: e.agent, View: true}, tk.ID)
	if err != nil || st.SyncState != "synced" || st.ExternalID == nil || !st.Enabled {
		t.Fatalf("state after push = %+v %v", st, err)
	}
	if fake.Tickets[*st.ExternalID].Title != tk.Title {
		t.Error("the external ticket carries the title")
	}
	if _, err := sync.StateOf(ctx, e.user(), tk.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("an employee reads the sync state: %v", err)
	}

	// A job for an already pushed version does nothing; a new request updates the same external ticket.
	if err := sync.HandlePush(ctx, job); err != nil || len(fake.Tickets) != 1 {
		t.Errorf("redundant push = %v, %d external tickets", err, len(fake.Tickets))
	}
	req()
	if err := sync.HandlePush(ctx, job); err != nil || len(fake.Tickets) != 1 {
		t.Errorf("second push = %v, %d external tickets", err, len(fake.Tickets))
	}
	req()
	fake.Fail = &autotask.Error{Message: "rejected by Autotask", Permanent: true}
	err = sync.HandlePush(ctx, job)
	if !jobs.IsPermanent(err) {
		t.Errorf("a permanent failure must not be retried: %v", err)
	}
	if st, _ := sync.StateOf(ctx, application.Principal{UserID: e.agent, View: true}, tk.ID); st.SyncState != "failed" || st.LastError == nil || st.Attempts != 1 {
		t.Errorf("state after failure = %+v", st)
	}
	fake.Fail = errors.New("connection reset")
	if err := sync.HandlePush(ctx, job); err == nil || jobs.IsPermanent(err) {
		t.Errorf("a transient failure is retried by the job runner: %v", err)
	}
	fake.Fail = nil

	// Retry needs tickets.manage and enqueues a new push.
	if err := sync.Retry(ctx, e.c(e.viewer), e.view(), tk.ID); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("retry without tickets.manage: %v", err)
	}
	if err := sync.Retry(ctx, e.c(e.agent), e.staff(), tk.ID); err != nil {
		t.Fatal(err)
	}
	if err := sync.HandlePush(ctx, job); err != nil {
		t.Fatal(err)
	}

	// A change during a push is detected: the stale push leaves the reference pending.
	ref, _ := externalrefs.ForEntity(ctx, e.pool, "autotask", "ticket", tk.ID)
	if _, err := externalrefs.MarkPending(ctx, e.pool, ref.ID); err != nil {
		t.Fatal(err)
	}
	if err := externalrefs.MarkSynced(ctx, e.pool, ref.ID, *st.ExternalID, ref.Version); !errors.Is(err, externalrefs.ErrStale) {
		t.Errorf("a push that overlapped a change: %v", err)
	}
	if st, _ := sync.StateOf(ctx, e.staff(), tk.ID); st.SyncState != "pending" {
		t.Errorf("state after a stale push = %s, want pending", st.SyncState)
	}
	if _, err := sync.StateOf(ctx, e.staff(), "not-a-uuid"); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("malformed ticket id: %v", err)
	}
	if err := sync.HandlePush(ctx, job); err != nil {
		t.Fatal(err)
	}

	// Inbound: replays and unknown external records change nothing, "resolved" resolves the ticket once.
	ext := *st.ExternalID
	ev := application.InboundEvent{EventID: e.corr + "-1", ExternalID: ext, Status: "resolved", OccurredAt: time.Now()}
	if err := sync.ApplyInbound(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if err := sync.ApplyInbound(ctx, ev); err != nil {
		t.Errorf("replayed event: %v", err)
	}
	if err := sync.ApplyInbound(ctx, application.InboundEvent{EventID: e.corr + "-2", ExternalID: "T-unknown", Status: "resolved"}); err != nil {
		t.Errorf("unknown external record: %v", err)
	}
	d, _ := e.svc.Get(ctx, e.staff(), tk.ID)
	if d.Ticket.Status != "resolved" {
		t.Errorf("ticket status = %s, want resolved", d.Ticket.Status)
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'servicedesk.ticket.resolve'`, "autotask:"+ev.EventID) != 1 {
		t.Error("the inbound resolution is audited once, by the system actor")
	}
	if st, _ := sync.StateOf(ctx, e.staff(), tk.ID); st.ExternalUpdatedAt == nil {
		t.Error("the external update time is recorded")
	}
	// A replay after the reporter reopened the ticket must not resolve it again.
	if _, err := e.svc.Transition(ctx, e.c(e.alice), application.Principal{UserID: e.alice}, tk.ID, nil, application.OpReopen, application.Params{Reason: "still broken"}); err != nil {
		t.Fatal(err)
	}
	if err := sync.ApplyInbound(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if d, _ := e.svc.Get(ctx, e.staff(), tk.ID); d.Ticket.Status == "resolved" {
		t.Error("a replayed event undid the reopen")
	}
	if err := sync.ApplyInbound(ctx, application.InboundEvent{ExternalID: ext}); err == nil {
		t.Error("an event without an id was accepted")
	}

	// Disabled: nothing is requested or applied.
	off := application.NewExternalSync(e.svc, repository.New(e.pool), e.pool, gw{fake}, false)
	if err := off.ApplyInbound(ctx, ev); !errors.Is(err, application.ErrSyncDisabled) {
		t.Errorf("inbound while disabled: %v", err)
	}
	if err := off.Retry(ctx, e.c(e.agent), e.staff(), tk.ID); !errors.Is(err, application.ErrSyncDisabled) {
		t.Errorf("retry while disabled: %v", err)
	}
	if _, err := externalrefs.ForEntity(ctx, e.pool, "autotask", "ticket", "00000000-0000-7000-8000-000000000001"); !errors.Is(err, externalrefs.ErrNotFound) {
		t.Errorf("unknown mapping: %v", err)
	}
}

// A paused old push must prevent another worker from pushing the same ticket.
type blockingTicketGateway struct {
	mu      sync.Mutex
	calls   int
	last    application.TicketPayload
	entered chan struct{}
	release chan struct{}
}

func (g *blockingTicketGateway) Push(ctx context.Context, p application.TicketPayload, _ string) (string, error) {
	g.mu.Lock()
	g.calls++
	first := g.calls == 1
	g.mu.Unlock()
	g.entered <- struct{}{}
	if first {
		select {
		case <-g.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	g.mu.Lock()
	g.last = p
	g.mu.Unlock()
	return "external-serialized", nil
}

func externalSyncFixture(t *testing.T, gateway application.TicketGateway) (*env, *application.ExternalSync, application.Ticket, jobs.Job) {
	t.Helper()
	e := newEnv(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = e.pool.Exec(ctx, `DELETE FROM platform.external_references WHERE entity_type = 'ticket' AND entity_id IN (SELECT id FROM servicedesk.tickets WHERE reporter_user_id = $1::uuid)`, e.alice)
		_, _ = e.pool.Exec(ctx, `DELETE FROM platform.jobs WHERE job_type = $1 AND payload->>'ticketId' IN (SELECT id::text FROM servicedesk.tickets WHERE reporter_user_id = $2::uuid)`, application.PushJobType, e.alice)
	})
	tk := e.raise()
	worker := application.NewExternalSync(e.svc, repository.New(e.pool), e.pool, gateway, true)
	if err := worker.Retry(ctx, e.c(e.agent), e.staff(), tk.ID); err != nil {
		t.Fatal(err)
	}
	return e, worker, tk, jobs.Job{Payload: []byte(`{"ticketId":"` + tk.ID + `"}`)}
}

func TestExternalSyncSerializesPushesPerTicket(t *testing.T) {
	gateway := &blockingTicketGateway{entered: make(chan struct{}, 2), release: make(chan struct{})}
	e, first, tk, job := externalSyncFixture(t, gateway)
	// Separate service instances model independent workers; no process-local mutex can satisfy this test.
	second := application.NewExternalSync(e.svc, repository.New(e.pool), e.pool, gateway, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var release sync.Once
	unblock := func() { release.Do(func() { close(gateway.release) }) }
	a, b := make(chan error, 1), make(chan error, 1)
	var workers sync.WaitGroup
	workers.Add(1)
	go func() { defer workers.Done(); a <- first.HandlePush(ctx, job) }()
	defer func() { unblock(); cancel(); workers.Wait() }()
	select {
	case <-gateway.entered:
	case <-ctx.Done():
		t.Fatal("first push did not start")
	}
	if _, err := e.svc.Transition(ctx, e.c(e.agent), e.staff(), tk.ID, nil, application.OpResolve, application.Params{Reason: "fixed"}); err != nil {
		t.Fatal(err)
	}
	if err := second.Retry(ctx, e.c(e.agent), e.staff(), tk.ID); err != nil {
		t.Fatal(err)
	}
	workers.Add(1)
	go func() { defer workers.Done(); b <- second.HandlePush(ctx, job) }()
	// Observe PostgreSQL contention rather than relying on a scheduling delay.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	waiting := false
	for !waiting {
		select {
		case <-gateway.entered:
			t.Fatal("second worker pushed while the old push was still running")
		case <-ctx.Done():
			t.Fatal("second worker did not wait on the ticket lock")
		case <-ticker.C:
			err := e.pool.QueryRow(ctx, `SELECT EXISTS (
    SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND NOT granted
     AND classid = ((hashtextextended('servicedesk.external.push:' || $1, 0) >> 32) & 4294967295)::oid
     AND objid = (hashtextextended('servicedesk.external.push:' || $1, 0) & 4294967295)::oid
   )`, tk.ID).Scan(&waiting)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	unblock()
	// The waiting worker must reread and push the current state after the stale push.
	errA, errB := <-a, <-b
	if !errors.Is(errA, externalrefs.ErrStale) || errB != nil {
		t.Fatalf("push results: old=%v new=%v", errA, errB)
	}
	gateway.mu.Lock()
	last, calls := gateway.last, gateway.calls
	gateway.mu.Unlock()
	if last.Status != "resolved" || calls != 2 {
		t.Fatalf("last payload=%+v calls=%d", last, calls)
	}
	ref, err := externalrefs.ForEntity(ctx, e.pool, "autotask", "ticket", tk.ID)
	if err != nil || ref.SyncState != "synced" {
		t.Fatalf("reference=%+v error=%v", ref, err)
	}
}

func TestExternalSyncStaleSuccessRequiresCurrentPush(t *testing.T) {
	fake := autotask.NewFake()
	e, worker, tk, job := externalSyncFixture(t, gw{fake})
	ctx := context.Background()
	old, err := externalrefs.ForEntity(ctx, e.pool, "autotask", "ticket", tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Transition(ctx, e.c(e.agent), e.staff(), tk.ID, nil, application.OpResolve, application.Params{Reason: "fixed"}); err != nil {
		t.Fatal(err)
	}
	if err := worker.Retry(ctx, e.c(e.agent), e.staff(), tk.ID); err != nil {
		t.Fatal(err)
	}
	if err := worker.HandlePush(ctx, job); err != nil {
		t.Fatal(err)
	}
	current, err := externalrefs.ForEntity(ctx, e.pool, "autotask", "ticket", tk.ID)
	if err != nil || current.ExternalID == nil {
		t.Fatalf("reference=%+v error=%v", current, err)
	}
	// Reproduce an already executed stale push after a newer success (for example an old worker during rollout).
	if _, err := (gw{fake}).Push(ctx, application.TicketPayload{Reference: tk.Reference, Title: tk.Title, Status: tk.Status, Priority: tk.Priority}, *current.ExternalID); err != nil {
		t.Fatal(err)
	}
	if err := externalrefs.MarkSynced(ctx, e.pool, old.ID, *current.ExternalID, old.Version); !errors.Is(err, externalrefs.ErrStale) {
		t.Fatalf("stale success: %v", err)
	}
	pending, err := externalrefs.ForEntity(ctx, e.pool, "autotask", "ticket", tk.ID)
	if err != nil || pending.SyncState != "pending" {
		t.Errorf("stale success must request repair: %+v %v", pending, err)
	}
	if err := worker.HandlePush(ctx, job); err != nil {
		t.Fatal(err)
	}
	if got := fake.Tickets[*current.ExternalID].Status; got != "resolved" {
		t.Fatalf("retry left external status %q", got)
	}
}
