package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

// TestMajorIncidentChannelPostsAreReferenceOnlyIdempotentAndSkipExercises declares and updates a Major Incident and
// checks the Teams channel deliveries: one per event and route, the reference number and no title, nothing for an
// exercise, nothing for the fan-out continuation and nothing while the module is off.
func TestMajorIncidentChannelPostsAreReferenceOnlyIdempotentAndSkipExercises(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	svc := wiring.MajorIncidents(w.pool)
	key := "wk-" + w.corr
	t.Cleanup(func() {
		_, _ = w.pool.Exec(ctx, `DELETE FROM platform.jobs WHERE job_type = $1 AND payload->>'deliveryId' IN (SELECT id::text FROM platform.notification_deliveries WHERE destination_key = $2)`, notifications.ChannelPostJobType, key)
		_, _ = w.pool.Exec(ctx, `DELETE FROM platform.notification_deliveries WHERE destination_key = $1`, key)
		_, _ = w.pool.Exec(ctx, `DELETE FROM platform.notification_channel_routes WHERE destination_key = $1`, key)
		_, _ = w.pool.Exec(ctx, `DELETE FROM servicedesk.major_incidents WHERE declared_by = $1::uuid`, w.assignee)
	})
	if _, err := w.pool.Exec(ctx, `INSERT INTO platform.notification_channel_routes(channel, category, destination_key, created_by)
		VALUES ('teams_channel', 'majorincident.update', $1, $2::uuid)`, key, w.assignee); err != nil {
		t.Fatal(err)
	}
	c := servicedeskapp.Caller{Actor: audit.UserActor(w.assignee), CorrelationID: w.corr}
	m, err := svc.Declare(ctx, c, true, "Dialysis ward network outage", "Patient monitoring is affected.", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Declare(ctx, c, true, "Drill", "Exercise only.", true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PostUpdate(ctx, c, true, m.ID, "A fix is rolling out."); err != nil {
		t.Fatal(err)
	}
	on := true
	opts := notifications.ChannelOptions{Enabled: func(context.Context) (bool, error) { return on, nil }}
	dispatch := func() {
		t.Helper()
		d := events.NewDispatcher(w.pool, events.DispatcherOptions{PollInterval: 10 * time.Millisecond, MaxAttempts: 2, EventTypes: allTestEventTypes},
			slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err := registerConsumersWith(d, w.pool, testCategories(t), false, w.everyone(), &opts); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 1000; i++ {
			ok, err := d.DispatchOne(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				return
			}
		}
	}
	dispatch()
	rows, err := w.pool.Query(ctx, `SELECT payload::text, status FROM platform.notification_deliveries WHERE destination_key = $1 ORDER BY created_at`, key)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var payloads []string
	for rows.Next() {
		var p, status string
		if err := rows.Scan(&p, &status); err != nil {
			t.Fatal(err)
		}
		if status != "pending" {
			t.Errorf("status = %s", status)
		}
		payloads = append(payloads, p)
	}
	rows.Close()
	if len(payloads) != 2 {
		t.Fatalf("deliveries = %d (%v), want declared and updated of the real incident only", len(payloads), payloads)
	}
	for _, p := range payloads {
		for _, secret := range []string{"Dialysis", "Patient", "rolling out", "Drill"} {
			if strings.Contains(p, secret) {
				t.Errorf("channel post payload carries record content %q: %s", secret, p)
			}
		}
		if !strings.Contains(p, `"reference": "MI-`) && !strings.Contains(p, `"reference":"MI-`) {
			t.Errorf("payload has no reference: %s", p)
		}
	}

	// Module off: the next update creates nothing.
	on = false
	if _, err := svc.PostUpdate(ctx, c, true, m.ID, "Second update."); err != nil {
		t.Fatal(err)
	}
	dispatch()
	var n int
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM platform.notification_deliveries WHERE destination_key = $1`, key).Scan(&n); err != nil || n != 2 {
		t.Errorf("deliveries with the module off = %d err=%v, want still 2", n, err)
	}
}
