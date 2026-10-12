package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/scheduling"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestClockBusinessTimeAcrossDSTAndClosure(t *testing.T) {
	cal := scheduling.BusinessCalendar{Timezone: "Europe/Berlin", WeeklyHours: map[int][]scheduling.Interval{7: {{Start: 60, End: 240}}}}
	for _, tc := range []struct {
		from    string
		minutes int
		want    string
	}{
		{"2026-03-29T01:30:00+01:00", 90, "2026-03-29T04:00:00+02:00"},
		{"2026-10-25T01:30:00+02:00", 150, "2026-10-25T03:00:00+01:00"},
	} {
		due, err := scheduling.AddBusinessDuration(cal, at(tc.from), time.Duration(tc.minutes)*time.Minute)
		if err != nil || !due.Equal(at(tc.want)) {
			t.Fatalf("due from %s: %v %v", tc.from, due, err)
		}
		elapsed, err := scheduling.BusinessDurationBetween(cal, at(tc.from), due)
		if err != nil || elapsed != time.Duration(tc.minutes)*time.Minute {
			t.Fatalf("elapsed: %v %v", elapsed, err)
		}
	}
	cal.Closures = map[string]struct{}{"2026-10-25": {}}
	due, err := scheduling.AddBusinessDuration(cal, at("2026-10-25T01:30:00+02:00"), time.Hour)
	if err != nil || !due.After(at("2026-10-25T03:00:00+01:00")) {
		t.Fatalf("closure due: %v %v", due, err)
	}
}

func TestExemptRequiresManagerWithoutDatabase(t *testing.T) {
	svc := application.NewService(nil, nil, nil)
	err := svc.ExemptSLA(context.Background(), application.Caller{Actor: audit.UserActor("00000000-0000-7000-8000-000000000001"), CorrelationID: "test"}, application.Principal{UserID: "00000000-0000-7000-8000-000000000001"}, "ticket", new(int), "test_ticket")
	if !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("exempt without sla.manage: %v", err)
	}
}

func TestClockStateAndProjection(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	actor := "00000000-0000-7000-8000-000000000001"
	var calendarID, policyID, queueID, ticketID string
	if err := tx.QueryRow(ctx, `INSERT INTO platform.business_calendars(key,name,timezone,weekly_hours) VALUES('clock_test_'||substr(uuidv7()::text,1,8),'Clock test','UTC','{"1":[{"start":540,"end":1020}],"2":[{"start":540,"end":1020}]}') RETURNING id::text`).Scan(&calendarID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO servicedesk.sla_policies(key,name,calendar_id,pause_waiting_reasons) VALUES('clock_test_'||substr(uuidv7()::text,1,8),'Clock test',$1,ARRAY['customer']) RETURNING id::text`, calendarID).Scan(&policyID); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"first_response", "resolution"} {
		if _, err := tx.Exec(ctx, `INSERT INTO servicedesk.sla_targets(policy_id,priority,kind,target_minutes,time_basis) VALUES($1,'normal',$2,120,'business')`, policyID, kind); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO servicedesk.sla_targets(policy_id,priority,kind,target_minutes,time_basis) VALUES($1,'urgent',$2,10,'business')`, policyID, kind); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.QueryRow(ctx, `INSERT INTO servicedesk.queues(key,prefix,name,sla_policy_id) VALUES('clock-test-'||substr(uuidv7()::text,1,8),'CLK'||upper(substr(uuidv7()::text,1,5)),'Clock test',$1) RETURNING id::text`, policyID).Scan(&queueID); err != nil {
		t.Fatal(err)
	}
	start := at("2026-10-12T09:00:00Z")
	if err := tx.QueryRow(ctx, `INSERT INTO servicedesk.tickets(kind,title,status,priority,reporter_user_id,affected_user_id,queue_id,created_at) VALUES('incident','Clock test','new','normal',$1,$1,$2,$3) RETURNING id::text`, actor, queueID, start).Scan(&ticketID); err != nil {
		t.Fatal(err)
	}
	r := repository.New(pool)
	ticket := application.Ticket{ID: ticketID, QueueID: queueID, Priority: "normal", CreatedAt: start}
	caller := application.Caller{Actor: audit.UserActor(actor), CorrelationID: "clock-test"}
	apply := func(op, reason string, when time.Time) {
		t.Helper()
		_, err := r.ApplySLATx(ctx, tx, ticket, op, reason, caller, when)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
	}
	apply("create", "", start)
	var version int
	var due time.Time
	if err := tx.QueryRow(ctx, `SELECT version,sla_resolution_due_at FROM servicedesk.tickets WHERE id=$1`, ticketID).Scan(&version, &due); err != nil {
		t.Fatal(err)
	}
	if !due.Equal(at("2026-10-12T11:00:00Z")) {
		t.Fatalf("initial due: %v", due)
	}
	apply("wait", "vendor", at("2026-10-12T09:15:00Z"))
	var stillDue *time.Time
	if err := tx.QueryRow(ctx, `SELECT sla_resolution_due_at FROM servicedesk.tickets WHERE id=$1`, ticketID).Scan(&stillDue); err != nil {
		t.Fatal(err)
	}
	if stillDue == nil {
		t.Fatal("unconfigured wait paused the clock")
	}
	apply("wait", "customer", at("2026-10-12T09:30:00Z"))
	apply("resume", "", at("2026-10-12T10:00:00Z"))
	if err := tx.QueryRow(ctx, `SELECT sla_resolution_due_at FROM servicedesk.tickets WHERE id=$1`, ticketID).Scan(&due); err != nil {
		t.Fatal(err)
	}
	if !due.Equal(at("2026-10-12T11:30:00Z")) {
		t.Fatalf("resumed due: %v", due)
	}
	apply("first_response", "", at("2026-10-12T10:10:00Z"))
	var responseStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM servicedesk.ticket_sla_clocks WHERE ticket_id=$1 AND kind='first_response'`, ticketID).Scan(&responseStatus); err != nil {
		t.Fatal(err)
	}
	if responseStatus != "completed" {
		t.Fatalf("first response: %s", responseStatus)
	}
	apply("resolve", "", at("2026-10-12T10:30:00Z"))
	apply("reopen", "", at("2026-10-12T11:40:00Z"))
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM servicedesk.ticket_sla_clocks WHERE ticket_id=$1 AND kind='resolution'`, ticketID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Fatalf("reopened status: %s", status)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.business_calendar_closures(calendar_id,closed_on,name) VALUES($1,'2026-10-12','Holiday')`, calendarID); err != nil {
		t.Fatal(err)
	}
	if err := r.RecomputeCalendarClocksTx(ctx, tx, calendarID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT sla_resolution_due_at FROM servicedesk.tickets WHERE id=$1`, ticketID).Scan(&due); err != nil {
		t.Fatal(err)
	}
	if !due.Equal(at("2026-10-13T10:00:00Z")) {
		t.Fatalf("holiday-adjusted due: %v", due)
	}
	ticket.Priority = "urgent"
	apply("set_priority", "", at("2026-10-12T11:45:00Z"))
	var breach time.Time
	if err := tx.QueryRow(ctx, `SELECT breached_at FROM servicedesk.ticket_sla_clocks WHERE ticket_id=$1 AND kind='resolution'`, ticketID).Scan(&breach); err != nil {
		t.Fatal(err)
	}
	ticket.Priority = "normal"
	apply("set_priority", "", at("2026-10-12T11:50:00Z"))
	var still time.Time
	if err := tx.QueryRow(ctx, `SELECT breached_at FROM servicedesk.ticket_sla_clocks WHERE ticket_id=$1 AND kind='resolution'`, ticketID).Scan(&still); err != nil {
		t.Fatal(err)
	}
	if !still.Equal(breach) {
		t.Fatalf("breach cleared or changed: %v -> %v", breach, still)
	}
	apply("cancel", "", at("2026-10-12T12:00:00Z"))
	var cancelledDue *time.Time
	if err := tx.QueryRow(ctx, `SELECT c.status,t.sla_resolution_due_at FROM servicedesk.tickets t JOIN servicedesk.ticket_sla_clocks c ON c.ticket_id=t.id AND c.kind='resolution' WHERE t.id=$1`, ticketID).Scan(&status, &cancelledDue); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" || cancelledDue != nil {
		t.Fatalf("cancelled clock: status=%s due=%v", status, cancelledDue)
	}
	var after int
	if err := tx.QueryRow(ctx, `SELECT version FROM servicedesk.tickets WHERE id=$1`, ticketID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != version {
		t.Fatalf("projection bumped ticket version: %d -> %d", version, after)
	}
}
