package sla_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/sla"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/businesscalendars"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

func TestPolicyPersistenceAndCalendarArchive(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(b[:])
	actor := "00000000-0000-4000-8000-000000000001"
	calendarStore := businesscalendars.NewCalendarStore(pool).WithUsage(public.CalendarUsage{})
	calendar, err := calendarStore.Create(ctx, businesscalendars.CalendarInput{Key: "sla_" + suffix, Name: "SLA test", Timezone: "UTC", WeeklyHours: map[int][]businesscalendars.Interval{1: {{Start: 540, End: 1020}}, 2: {{Start: 540, End: 1020}}, 3: {{Start: 540, End: 1020}}, 4: {{Start: 540, End: 1020}}, 5: {{Start: 540, End: 1020}}}}, actor, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform.business_calendars WHERE id=$1`, calendar.ID)
	})
	store := sla.New(pool)
	policy, err := store.Create(ctx, sla.Input{Key: "sla_" + suffix, Name: "Support SLA", CalendarID: &calendar.ID, PauseWaitingReasons: []string{"customer"}, WarningPercent: 80}, actor, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM servicedesk.sla_policies WHERE id=$1`, policy.ID)
	})
	if _, err := calendarStore.SetStatus(ctx, calendar.ID, calendar.Version, "archived", actor, "test"); !errors.Is(err, businesscalendars.ErrCalendarInUse) {
		t.Fatalf("archive used calendar: %v", err)
	}
	policy, err = store.ReplaceTargets(ctx, policy.ID, policy.Version, []sla.Target{{Priority: "urgent", Kind: "first_response", TargetMinutes: 30, TimeBasis: "business"}}, actor, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Targets) != 1 {
		t.Fatalf("targets: %+v", policy.Targets)
	}
	if _, err := store.ReplaceTargets(ctx, policy.ID, policy.Version-1, nil, actor, "test"); !errors.Is(err, sla.ErrConflict) {
		t.Fatalf("stale targets: %v", err)
	}
	prefix := "S" + strings.ToUpper(suffix[:6])
	var queueID string
	err = pool.QueryRow(ctx, `INSERT INTO servicedesk.queues(key,prefix,name) VALUES($1,$2,'SLA test queue') RETURNING id::text`, "sla-"+suffix, prefix).Scan(&queueID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM servicedesk.queues WHERE id=$1`, queueID) })
	queue, err := store.SetQueuePolicy(ctx, queueID, &policy.ID, 1, actor, "test")
	if err != nil {
		t.Fatal(err)
	}
	if queue.PolicyID == nil || *queue.PolicyID != policy.ID {
		t.Fatalf("assignment: %+v", queue)
	}
	if _, err := store.SetStatus(ctx, policy.ID, policy.Version, "archived", actor, "test"); !errors.Is(err, sla.ErrInUse) {
		t.Fatalf("archive assigned policy: %v", err)
	}
	queue, err = store.SetQueuePolicy(ctx, queueID, nil, queue.Version, actor, "test")
	if err != nil {
		t.Fatal(err)
	}
	if queue.PolicyID != nil {
		t.Fatalf("unassign: %+v", queue)
	}
	policy, err = store.SetStatus(ctx, policy.ID, policy.Version, "archived", actor, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := calendarStore.SetStatus(ctx, calendar.ID, calendar.Version, "archived", actor, "test"); err != nil {
		t.Fatalf("archive unused calendar: %v", err)
	}
}
