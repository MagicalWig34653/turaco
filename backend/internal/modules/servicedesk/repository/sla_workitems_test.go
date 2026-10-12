package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
)

func TestMyWorkOrdersByEarliestSLADueDateWhileTheSwitchIsOn(t *testing.T) {
	ctx := context.Background()
	f := newSweepFixture(t, false, nil)
	assignee := sweepUser
	base := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	// Created later = due later. The ticket without a clock is a plain urgent one.
	late := f.ticket(base.Add(30 * time.Minute))
	soon := f.ticket(base)
	middle := f.ticket(base.Add(10 * time.Minute))
	var plain string
	if err := f.pool.QueryRow(ctx, `INSERT INTO servicedesk.tickets(kind,title,status,priority,reporter_user_id,affected_user_id,queue_id) VALUES('incident','No clock','open','urgent',$1,$1,$2) RETURNING id::text`, sweepUser, f.queueID).Scan(&plain); err != nil {
		t.Fatal(err)
	}
	ids := []string{late, soon, middle, plain}
	if _, err := f.pool.Exec(ctx, `UPDATE servicedesk.tickets SET assignee_user_id=$1 WHERE id = ANY($2::uuid[])`, assignee, ids); err != nil {
		t.Fatal(err)
	}
	mine := map[string]bool{}
	for _, id := range ids {
		mine[id] = true
	}
	// page walks the whole list in pages of two and keeps only this test's tickets.
	page := func(sla bool) ([]string, []*time.Time) {
		t.Helper()
		var out []string
		var due []*time.Time
		q := application.WorkQuery{Source: application.WorkAssigned, UserID: assignee, Global: true, SLA: sla, Limit: 2}
		for {
			rows, err := f.repo.WorkTickets(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rows {
				if mine[r.Ticket.ID] {
					out = append(out, r.Ticket.ID)
					due = append(due, r.DueAt)
				}
			}
			if len(rows) < q.Limit {
				return out, due
			}
			last := rows[len(rows)-1]
			q.AfterRank, q.AfterID, q.AfterDue = last.Rank, last.Ticket.ID, last.DueAt
		}
	}
	got, due := page(true)
	want := []string{soon, middle, late, plain}
	if len(got) != 4 {
		t.Fatalf("expected 4 tickets, got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SLA order: got %v want %v", got, want)
		}
	}
	if due[0] == nil || !due[0].Equal(base.Add(100*time.Minute)) || due[3] != nil {
		t.Fatalf("due times: %v", due)
	}
	// Switch off: no due times, the order falls back to priority (the urgent ticket first), then id.
	got, due = page(false)
	if len(got) != 4 || got[0] != plain {
		t.Fatalf("order without SLA: %v", got)
	}
	for _, d := range due {
		if d != nil {
			t.Fatal("due time reported while the switch is off")
		}
	}
}
