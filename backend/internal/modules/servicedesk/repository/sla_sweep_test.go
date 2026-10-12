package repository_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

const sweepUser = "00000000-0000-7000-8000-000000000001"

// sweepFixture is a Queue whose policy has one continuous resolution target of 100 minutes (warning at 80 percent).
// Its data is committed because the sweep runs in its own transactions; every key is unique, so runs do not collide.
type sweepFixture struct {
	t        *testing.T
	pool     *pgxpool.Pool
	repo     *repository.Repository
	queueID  string
	policyID string
}

func newSweepFixture(t *testing.T, escalation bool, teamID *string) *sweepFixture {
	t.Helper()
	pool := dbtest.Pool(t)
	ctx := context.Background()
	f := &sweepFixture{t: t, pool: pool, repo: repository.New(pool)}
	if err := pool.QueryRow(ctx, `INSERT INTO servicedesk.sla_policies(key,name,escalation_task) VALUES('sweep_'||right(uuidv7()::text,8),'Sweep test',$1) RETURNING id::text`, escalation).Scan(&f.policyID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO servicedesk.sla_targets(policy_id,priority,kind,target_minutes,time_basis) VALUES($1,'normal','resolution',100,'continuous')`, f.policyID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO servicedesk.queues(key,prefix,name,sla_policy_id,default_team_id) VALUES('sweep-'||right(uuidv7()::text,8),'SW'||upper(right(uuidv7()::text,5)),'Sweep test',$1,$2) RETURNING id::text`, f.policyID, teamID).Scan(&f.queueID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbtest.PurgeSLAFixtures(t, pool, "sweep-") })
	return f
}

// ticket creates an open Ticket created at the given time and starts its clock through the real create operation.
func (f *sweepFixture) ticket(created time.Time) string {
	f.t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO servicedesk.tickets(kind,title,status,priority,reporter_user_id,affected_user_id,queue_id,created_at) VALUES('incident','Sweep test','open','normal',$1,$1,$2,$3) RETURNING id::text`, sweepUser, f.queueID, created).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	ticket := application.Ticket{ID: id, QueueID: f.queueID, Priority: "normal", CreatedAt: created}
	if _, err := f.repo.ApplySLATx(ctx, tx, ticket, "create", "", application.Caller{Actor: audit.UserActor(sweepUser), CorrelationID: "sweep-test"}, created); err != nil {
		f.t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *sweepFixture) apply(ticketID, op, reason string, at time.Time) {
	f.t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := f.repo.ApplySLATx(ctx, tx, application.Ticket{ID: ticketID, QueueID: f.queueID, Priority: "normal"}, op, reason, application.Caller{Actor: audit.UserActor(sweepUser), CorrelationID: "sweep-test"}, at); err != nil {
		f.t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		f.t.Fatal(err)
	}
}

func (f *sweepFixture) sweeper(enabled bool, now time.Time) *application.SLASweeper {
	return wiring.SLASweeper(f.pool, func(context.Context) (bool, error) { return enabled, nil }).WithNow(func() time.Time { return now })
}

func (f *sweepFixture) events(ticketID, typ string) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM platform.outbox_events WHERE event_type=$1 AND payload->>'ticketId'=$2`, typ, ticketID).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

type clockFacts struct {
	warned, breached *time.Time
	version          int
	ticketVersion    int
	projectedBreach  *time.Time
	transitions      int
}

func (f *sweepFixture) facts(ticketID string) clockFacts {
	f.t.Helper()
	var c clockFacts
	ctx := context.Background()
	if err := f.pool.QueryRow(ctx, `SELECT c.warned_at,c.breached_at,c.version,t.version,t.sla_breached_at FROM servicedesk.ticket_sla_clocks c JOIN servicedesk.tickets t ON t.id=c.ticket_id WHERE c.ticket_id=$1 AND c.kind='resolution'`, ticketID).
		Scan(&c.warned, &c.breached, &c.version, &c.ticketVersion, &c.projectedBreach); err != nil {
		f.t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM servicedesk.ticket_sla_clock_transitions WHERE ticket_id=$1 AND operation IN ('mark_warned','mark_breached')`, ticketID).Scan(&c.transitions); err != nil {
		f.t.Fatal(err)
	}
	return c
}

func TestSweepWarnsThenBreachesIdempotentlyWithoutBumpingTicketVersion(t *testing.T) {
	f := newSweepFixture(t, false, nil)
	created := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Microsecond)
	id := f.ticket(created)
	before := f.facts(id)

	// 85 minutes in: past the warning point (80), before the deadline (100).
	sum, err := f.sweeper(true, created.Add(85*time.Minute)).Sweep(context.Background(), "sweep-test")
	if err != nil || sum.Warned < 1 {
		t.Fatalf("warning sweep: %+v %v", sum, err)
	}
	got := f.facts(id)
	if got.warned == nil || !got.warned.Equal(created.Add(80*time.Minute)) || got.breached != nil {
		t.Fatalf("after warning: %+v", got)
	}
	if got.ticketVersion != before.ticketVersion {
		t.Fatalf("sweep bumped the ticket version: %d -> %d", before.ticketVersion, got.ticketVersion)
	}
	if f.events(id, "TicketSLAWarningReached") != 1 || f.events(id, "TicketSLABreached") != 0 {
		t.Fatal("exactly one warning event expected")
	}
	// Re-running records nothing twice.
	if _, err := f.sweeper(true, created.Add(86*time.Minute)).Sweep(context.Background(), "sweep-test"); err != nil {
		t.Fatal(err)
	}
	if again := f.facts(id); again.version != got.version || f.events(id, "TicketSLAWarningReached") != 1 {
		t.Fatal("a re-run changed the warned clock")
	}

	// 105 minutes in: past the deadline.
	if _, err := f.sweeper(true, created.Add(105*time.Minute)).Sweep(context.Background(), "sweep-test"); err != nil {
		t.Fatal(err)
	}
	done := f.facts(id)
	if done.breached == nil || !done.breached.Equal(created.Add(100*time.Minute)) {
		t.Fatalf("breached_at must be the factual deadline: %+v", done.breached)
	}
	if done.projectedBreach == nil || !done.projectedBreach.Equal(*done.breached) || done.ticketVersion != before.ticketVersion {
		t.Fatalf("projection or version: %+v", done)
	}
	if done.transitions != 2 || f.events(id, "TicketSLABreached") != 1 || f.events(id, "TicketSLAWarningReached") != 1 {
		t.Fatalf("expected one warning and one breach: %+v", done)
	}
	if _, err := f.sweeper(true, created.Add(200*time.Minute)).Sweep(context.Background(), "sweep-test"); err != nil {
		t.Fatal(err)
	}
	if final := f.facts(id); final.version != done.version || final.transitions != 2 || f.events(id, "TicketSLABreached") != 1 {
		t.Fatal("a re-run changed the breached clock")
	}
}

func TestSweepBreachOvertakingTheWarningAnnouncesOnlyTheBreach(t *testing.T) {
	f := newSweepFixture(t, false, nil)
	created := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Microsecond)
	id := f.ticket(created)
	if _, err := f.sweeper(true, created.Add(130*time.Minute)).Sweep(context.Background(), "sweep-test"); err != nil {
		t.Fatal(err)
	}
	got := f.facts(id)
	if got.warned == nil || got.breached == nil || got.transitions != 2 {
		t.Fatalf("both facts must be recorded: %+v", got)
	}
	if f.events(id, "TicketSLAWarningReached") != 0 || f.events(id, "TicketSLABreached") != 1 {
		t.Fatal("only the breach is announced when the sweep is late")
	}
}

func TestSweepTwoWorkersRecordOneBreach(t *testing.T) {
	f := newSweepFixture(t, false, nil)
	created := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Microsecond)
	id := f.ticket(created)
	now := created.Add(150 * time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.sweeper(true, now).Sweep(context.Background(), "sweep-test"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	// A worker that found the clock locked leaves it for the next pass: run once more, then it is complete.
	if _, err := f.sweeper(true, now).Sweep(context.Background(), "sweep-test"); err != nil {
		t.Fatal(err)
	}
	if got := f.facts(id); got.transitions != 2 || f.events(id, "TicketSLABreached") != 1 || f.events(id, "TicketSLAWarningReached") != 0 {
		t.Fatalf("two workers must record every fact once: %+v", got)
	}
}

func TestSweepSkipsPausedAndExemptClocksAndHonoursSwitch(t *testing.T) {
	f := newSweepFixture(t, false, nil)
	created := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Microsecond)
	paused := f.ticket(created)
	f.apply(paused, "wait", "customer", created.Add(10*time.Minute))
	exempt := f.ticket(created)
	f.apply(exempt, "exempt", "test_ticket", created.Add(10*time.Minute))
	off := f.ticket(created)

	now := created.Add(150 * time.Minute)
	// Switch off: nothing is recorded and the clock is untouched.
	sum, err := f.sweeper(false, now).Sweep(context.Background(), "sweep-test")
	if err != nil || sum != (application.SLASweepSummary{}) {
		t.Fatalf("switch off: %+v %v", sum, err)
	}
	if got := f.facts(off); got.warned != nil || got.breached != nil || got.transitions != 0 {
		t.Fatalf("a clock changed while the switch was off: %+v", got)
	}
	if _, err := f.sweeper(true, now).Sweep(context.Background(), "sweep-test"); err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]string{"paused": paused, "exempt": exempt} {
		if got := f.facts(id); got.warned != nil || got.breached != nil || got.transitions != 0 {
			t.Fatalf("%s clock was swept: %+v", name, got)
		}
		if f.events(id, "TicketSLAWarningReached")+f.events(id, "TicketSLABreached") != 0 {
			t.Fatalf("%s clock published an event", name)
		}
	}
	// Re-enabled: the running clock is caught up.
	if got := f.facts(off); got.breached == nil {
		t.Fatalf("the running clock must be breached once the switch is on: %+v", got)
	}
}

func TestSweepBeforeTheWarningPointRecordsNothing(t *testing.T) {
	f := newSweepFixture(t, false, nil)
	created := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Microsecond)
	id := f.ticket(created)
	if _, err := f.sweeper(true, created.Add(10*time.Minute)).Sweep(context.Background(), "sweep-test"); err != nil {
		t.Fatal(err)
	}
	if got := f.facts(id); got.warned != nil || got.breached != nil {
		t.Fatalf("early sweep recorded facts: %+v", got)
	}
}

func TestSweepBreachCreatesOneEscalationTaskWhenPolicyAsks(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Pool(t)
	var team string
	if err := pool.QueryRow(ctx, `INSERT INTO organization.teams(name) VALUES ('sweep team '||right(uuidv7()::text,8)) RETURNING id::text`).Scan(&team); err != nil {
		t.Fatal(err)
	}
	asked := newSweepFixture(t, true, &team)
	declined := newSweepFixture(t, false, &team)
	created := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Microsecond)
	withTask, withoutTask := asked.ticket(created), declined.ticket(created)
	now := created.Add(150 * time.Minute)
	for i := 0; i < 2; i++ {
		if _, err := asked.sweeper(true, now).Sweep(ctx, "sweep-test"); err != nil {
			t.Fatal(err)
		}
	}
	count := func(id string) (n int, assigned string) {
		rows, err := pool.Query(ctx, `SELECT coalesce(assigned_team_id::text,'') FROM platform.tasks WHERE context_type='ticket' AND context_id=$1::uuid AND title LIKE 'SLA breached: %'`, id)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			n++
			if err := rows.Scan(&assigned); err != nil {
				t.Fatal(err)
			}
		}
		return n, assigned
	}
	if n, assigned := count(withTask); n != 1 || assigned != team {
		t.Fatalf("escalation task: %d tasks for team %q", n, assigned)
	}
	if n, _ := count(withoutTask); n != 0 {
		t.Fatalf("policy without escalation_task created %d tasks", n)
	}
}
