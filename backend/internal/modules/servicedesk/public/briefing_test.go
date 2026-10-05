package public

import (
	"context"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"testing"
)

func TestOpenMajorIncidentsScope(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	var id string
	err := pool.QueryRow(ctx, `INSERT INTO servicedesk.major_incidents(title,summary) VALUES ('private incident','status') RETURNING id::text`).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM servicedesk.major_incidents WHERE id=$1::uuid`, id) })
	b := NewBriefing(pool)
	for _, tc := range []struct {
		scope ReadScope
		title string
	}{{ReadScope{}, ""}, {ReadScope{IncludeDetails: true}, "private incident"}} {
		list, err := b.OpenMajorIncidents(ctx, tc.scope)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, v := range list {
			if v.ID == id {
				found = true
				if v.Title != tc.title {
					t.Fatalf("title = %q, want %q", v.Title, tc.title)
				}
			}
		}
		if !found {
			t.Fatal("test incident absent")
		}
	}
}

func TestAutotaskSyncHealthCountsFailures(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	var entity string
	if err := pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&entity); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform.external_references WHERE system='autotask' AND entity_type='ticket' AND entity_id=$1::uuid`, entity)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO platform.external_references(system,entity_type,entity_id,sync_state) VALUES ('autotask','ticket',$1::uuid,'failed')`, entity); err != nil {
		t.Fatal(err)
	}
	h, err := NewBriefing(pool).SyncHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h.Failed < 1 || h.OldestFailureAt == nil {
		t.Fatalf("health = %+v", h)
	}
}

func TestBriefingTicketCountsUseOpenScope(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	var incidentID, ticketID, userID string
	if err := pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO servicedesk.major_incidents(title,summary) VALUES ('count incident','status') RETURNING id::text`).Scan(&incidentID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM servicedesk.major_incidents WHERE id=$1::uuid`, incidentID) })
	if err := pool.QueryRow(ctx, `INSERT INTO servicedesk.tickets(title,reporter_user_id,affected_user_id,major_incident_id)
 VALUES ('count ticket',$1::uuid,$1::uuid,$2::uuid) RETURNING id::text`, userID, incidentID).Scan(&ticketID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM servicedesk.tickets WHERE id=$1::uuid`, ticketID) })
	b := NewBriefing(pool)
	hidden, err := b.UnassignedOpenTickets(ctx, ReadScope{})
	if err != nil || hidden != 0 {
		t.Fatalf("hidden count = %d, %v", hidden, err)
	}
	count, err := b.UnassignedOpenTickets(ctx, ReadScope{IncludeDetails: true})
	if err != nil || count < 1 {
		t.Fatalf("visible count = %d, %v", count, err)
	}
	incidents, err := b.OpenMajorIncidents(ctx, ReadScope{IncludeDetails: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, incident := range incidents {
		if incident.ID == incidentID {
			if incident.LinkedTicketsTotal != 1 || incident.OpenLinkedTickets != 1 {
				t.Fatalf("counts = %+v", incident)
			}
			if _, err := pool.Exec(ctx, `UPDATE servicedesk.tickets SET status='resolved',resolved_at=now() WHERE id=$1::uuid`, ticketID); err != nil {
				t.Fatal(err)
			}
			refreshed, err := b.OpenMajorIncidents(ctx, ReadScope{IncludeDetails: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range refreshed {
				if v.ID == incidentID && (v.LinkedTicketsTotal != 1 || v.OpenLinkedTickets != 0) {
					t.Fatalf("resolved counts = %+v", v)
				}
			}
			return
		}
	}
	t.Fatal("incident absent")
}
