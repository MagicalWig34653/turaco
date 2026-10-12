package main

import (
	"context"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"testing"
	"time"

	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	servicedeskrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

// TestSLASweepToNotificationFlow runs the sweep and the real consumer registrations: the breach of an assigned
// Ticket reaches the assignee once, a second sweep and a second dispatch add nothing.
func TestSLASweepToNotificationFlow(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	var policy, queue, ticket string
	if err := w.pool.QueryRow(ctx, `INSERT INTO servicedesk.sla_policies(key,name) VALUES('flow_'||right(uuidv7()::text,8),'Flow') RETURNING id::text`).Scan(&policy); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pool.Exec(ctx, `INSERT INTO servicedesk.sla_targets(policy_id,priority,kind,target_minutes,time_basis) VALUES($1,'normal','resolution',60,'continuous')`, policy); err != nil {
		t.Fatal(err)
	}
	if err := w.pool.QueryRow(ctx, `INSERT INTO servicedesk.queues(key,prefix,name,sla_policy_id) VALUES('flow-'||right(uuidv7()::text,8),'FL'||upper(right(uuidv7()::text,5)),'Flow',$1) RETURNING id::text`, policy).Scan(&queue); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pool.Exec(ctx, `INSERT INTO servicedesk.queue_grants (queue_id, subject_type, subject_id, level) VALUES ($1,'user',$2::uuid,'work')`, queue, w.assignee); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = w.pool.Exec(ctx, `DELETE FROM servicedesk.queue_grants WHERE subject_id = $1::uuid`, w.assignee)
		dbtest.PurgeSLAFixtures(t, w.pool, "flow-")
	})
	created := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := tx.QueryRow(ctx, `INSERT INTO servicedesk.tickets(kind,title,status,priority,reporter_user_id,affected_user_id,assignee_user_id,queue_id,created_at) VALUES('incident','Flow','open','normal',$1,$1,$2,$3,$4) RETURNING id::text`,
		w.creator, w.assignee, queue, created).Scan(&ticket); err != nil {
		t.Fatal(err)
	}
	if _, err := servicedeskrepository.New(w.pool).ApplySLATx(ctx, tx, servicedeskapp.Ticket{ID: ticket, QueueID: queue, Priority: "normal", CreatedAt: created}, "create", "",
		servicedeskapp.Caller{Actor: audit.UserActor(w.creator), CorrelationID: w.corr}, created); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	sweeper := wiring.SLASweeper(w.pool, func(context.Context) (bool, error) { return true, nil })
	for i := 0; i < 2; i++ {
		if _, err := sweeper.Sweep(ctx, w.corr); err != nil {
			t.Fatal(err)
		}
		w.dispatch()
	}
	if n := w.notified(w.assignee, "ticket.sla_breached"); n != 1 {
		t.Fatalf("assignee notifications: %d", n)
	}
	if n := w.notified(w.assignee, "ticket.sla_warning"); n != 0 {
		t.Fatalf("a late sweep must announce the breach only, got %d warnings", n)
	}
	if w.pendingEvents() != 0 {
		t.Errorf("%d events left unprocessed", w.pendingEvents())
	}
}
