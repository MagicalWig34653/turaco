package repository

import (
	"context"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
)

func TestTaskOverviewCountsOverdueAndUnowned(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	q := application.OverviewQuery{Now: time.Now().UTC(), OwnerLimit: 5, OldestLimit: 10}
	before, err := f.repo.TaskOverview(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	longAgo := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	soon := time.Now().UTC().Add(48 * time.Hour)
	overdue := f.insert("ovw overdue", "high", &longAgo)
	f.insert("ovw not yet due", "normal", &soon)
	f.insert("ovw no due date", "low", nil)

	after, err := f.repo.TaskOverview(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if got := after.Totals.Open - before.Totals.Open; got != 3 {
		t.Errorf("open delta = %d, want 3", got)
	}
	if got := after.Totals.Overdue - before.Totals.Overdue; got != 1 {
		t.Errorf("overdue delta = %d, want 1", got)
	}
	if got := after.Totals.Unowned - before.Totals.Unowned; got != 3 {
		t.Errorf("unowned delta = %d, want 3", got)
	}
	if len(after.OldestOverdue) == 0 || after.OldestOverdue[0].ID != overdue.ID && !containsTask(after.OldestOverdue, overdue.ID) {
		t.Errorf("oldest overdue does not list the overdue task: %+v", after.OldestOverdue)
	}
	if len(after.OldestOverdue) > q.OldestLimit || len(after.Users) > q.OwnerLimit || len(after.Teams) > q.OwnerLimit {
		t.Errorf("limits exceeded: %d %d %d", len(after.OldestOverdue), len(after.Users), len(after.Teams))
	}
}

func containsTask(ts []application.Task, id string) bool {
	for _, t := range ts {
		if t.ID == id {
			return true
		}
	}
	return false
}
