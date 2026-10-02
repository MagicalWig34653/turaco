package repository

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
)

type recFixture struct {
	*fixture
	defs *Definitions
}

func newRecFixture(t *testing.T) *recFixture {
	f := newFixture(t)
	r := &recFixture{fixture: f, defs: NewDefinitions(f.pool)}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE target_type = 'task' AND correlation_id LIKE 'recurrence:%' AND metadata->>'recurrenceDefinitionId' IN (SELECT id::text FROM platform.recurring_task_definitions WHERE title LIKE $1)`, "rt-"+f.corr+"%")
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.tasks WHERE recurrence_definition_id IN (SELECT id FROM platform.recurring_task_definitions WHERE title LIKE $1)`, "rt-"+f.corr+"%")
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.recurring_task_definitions WHERE title LIKE $1`, "rt-"+f.corr+"%")
	})
	return r
}

func (r *recFixture) rule() application.Rule {
	return application.Rule{Frequency: application.FreqDaily, Interval: 1, TimeOfDay: "09:00", Timezone: "UTC", StartsOn: "2026-01-01"}
}

func (r *recFixture) create(suffix string, next time.Time, mutate func(*application.NewDefinition)) application.Definition {
	r.t.Helper()
	n := application.NewDefinition{
		Title: "rt-" + r.corr + suffix, Priority: "high", Rule: r.rule(), NextRunAt: next,
	}
	if mutate != nil {
		mutate(&n)
	}
	d, err := r.defs.Insert(context.Background(), r.caller(), n)
	if err != nil {
		r.t.Fatalf("insert definition: %v", err)
	}
	return d
}

func (r *recFixture) generatedTasks(defID string) int {
	return r.count(`SELECT count(*) FROM platform.tasks WHERE recurrence_definition_id = $1::uuid`, defID)
}

func planTo(next time.Time) func(application.Definition) (application.Generation, error) {
	return func(d application.Definition) (application.Generation, error) {
		return application.Generation{AssignedUserID: d.AssignedUserID, AssignedTeamID: d.AssignedTeamID, NextRunAt: next}, nil
	}
}

func TestGenerateDueCreatesTheTaskAdvancesAndAudits(t *testing.T) {
	f := newRecFixture(t)
	ctx := context.Background()
	scheduled := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	user := "00000000-0000-7000-8000-0000000000b1"
	d := f.create("-a", scheduled, func(n *application.NewDefinition) {
		n.AssignedUserID = &user
		n.DueAfterHours = intp(48)
		desc := "Check the nightly job"
		n.Description = &desc
	})
	next := time.Now().UTC().Add(23 * time.Hour).Truncate(time.Second)

	found, generated, err := f.defs.GenerateDue(ctx, time.Now().UTC(), planTo(next))
	if err != nil || !found || !generated {
		t.Fatalf("found=%v generated=%v err=%v", found, generated, err)
	}
	var (
		title, priority, status string
		due, sched              time.Time
		assigned                *string
		createdBy               *string
	)
	err = f.pool.QueryRow(ctx, `SELECT title, priority, status, due_at, scheduled_for, assigned_user_id::text, created_by_user_id::text
		FROM platform.tasks WHERE recurrence_definition_id = $1::uuid`, d.ID).Scan(&title, &priority, &status, &due, &sched, &assigned, &createdBy)
	if err != nil {
		t.Fatal(err)
	}
	if title != d.Title || priority != "high" || status != "open" || assigned == nil || *assigned != user || createdBy != nil {
		t.Errorf("task = %q %q %q assigned=%v createdBy=%v", title, priority, status, assigned, createdBy)
	}
	if !sched.Equal(scheduled) || !due.Equal(scheduled.Add(48*time.Hour)) {
		t.Errorf("scheduled_for=%v due=%v, want %v and +48h", sched, due, scheduled)
	}
	got, _ := f.defs.Get(ctx, d.ID)
	if got.NextRunAt == nil || !got.NextRunAt.Equal(next) || got.LastGeneratedAt == nil || got.Version != 2 {
		t.Errorf("definition after generation = %+v", got)
	}
	// Audit: one task.created by the system actor "recurrence", no title or description.
	var action, actor, meta string
	err = f.pool.QueryRow(ctx, `
		SELECT a.action, a.metadata->>'actor', a.metadata::text FROM platform.audit_events a
		JOIN platform.tasks t ON t.id::text = a.target_id
		WHERE t.recurrence_definition_id = $1::uuid`, d.ID).Scan(&action, &actor, &meta)
	if err != nil || action != "tasks.task.created" || actor != "recurrence" || !contains(meta, d.ID) || contains(meta, "nightly") {
		t.Errorf("audit = %q %q %q %v", action, actor, meta, err)
	}
	// An assigned generated task emits TaskAssigned without an actor user.
	if n := f.count(`SELECT count(*) FROM platform.outbox_events WHERE event_type = 'TaskAssigned' AND actor_id IS NULL AND payload->>'taskId' IN (SELECT id::text FROM platform.tasks WHERE recurrence_definition_id = $1::uuid)`, d.ID); n != 1 {
		t.Errorf("TaskAssigned events = %d, want 1", n)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE event_type = 'TaskAssigned' AND payload->>'taskId' NOT IN (SELECT id::text FROM platform.tasks)`)
	})
	// Nothing else is due.
	if found, _, _ := f.defs.GenerateDue(ctx, time.Now().UTC(), planTo(next)); found && f.generatedTasks(d.ID) != 1 {
		t.Error("generated a second task for the same definition")
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

func intp(n int) *int { return &n }

// A run is generated at most once even if the schedule is reset (crash recovery,
// manual correction): the unique (definition, scheduled_for) pair absorbs it.
func TestGenerationIsIdempotentPerScheduledRun(t *testing.T) {
	f := newRecFixture(t)
	ctx := context.Background()
	scheduled := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	d := f.create("-idem", scheduled, nil)
	later := time.Now().UTC().Add(24 * time.Hour)
	if _, generated, err := f.defs.GenerateDue(ctx, time.Now().UTC(), planTo(later)); err != nil || !generated {
		t.Fatalf("first: generated=%v err=%v", generated, err)
	}
	// Reset the schedule to the same run, as a restored backup would.
	if _, err := f.pool.Exec(ctx, `UPDATE platform.recurring_task_definitions SET next_run_at = $2 WHERE id = $1::uuid`, d.ID, scheduled); err != nil {
		t.Fatal(err)
	}
	found, generated, err := f.defs.GenerateDue(ctx, time.Now().UTC(), planTo(later))
	if err != nil || !found || generated {
		t.Fatalf("second: found=%v generated=%v err=%v, want the run recognised as done", found, generated, err)
	}
	if n := f.generatedTasks(d.ID); n != 1 {
		t.Errorf("tasks = %d, want 1", n)
	}
	got, _ := f.defs.Get(ctx, d.ID)
	if got.NextRunAt == nil || !got.NextRunAt.Equal(later.Truncate(time.Microsecond)) {
		t.Errorf("the schedule must still move on: %v", got.NextRunAt)
	}
}

func TestConcurrentWorkersGenerateEachRunOnce(t *testing.T) {
	f := newRecFixture(t)
	ctx := context.Background()
	const n = 12
	for i := 0; i < n; i++ {
		f.create("-c"+string(rune('a'+i)), time.Now().UTC().Add(-time.Hour), nil)
	}
	var generated atomic.Int32
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				found, created, err := f.defs.GenerateDue(ctx, time.Now().UTC(), planTo(time.Now().UTC().Add(24*time.Hour)))
				if err != nil {
					t.Errorf("generate: %v", err)
					return
				}
				if created {
					generated.Add(1)
				}
				if !found {
					return
				}
			}
		}()
	}
	wg.Wait()
	if got := f.count(`SELECT count(*) FROM platform.tasks t JOIN platform.recurring_task_definitions d ON d.id = t.recurrence_definition_id WHERE d.title LIKE $1`, "rt-"+f.corr+"-c%"); got != n || int(generated.Load()) < n {
		t.Errorf("tasks=%d generated=%d, want %d each (other tests' due definitions may add to generated)", got, generated.Load(), n)
	}
}

func TestPlanErrorRollsBackEverything(t *testing.T) {
	f := newRecFixture(t)
	ctx := context.Background()
	scheduled := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	d := f.create("-fail", scheduled, nil)
	boom := errors.New("boom")
	_, _, err := f.defs.GenerateDue(ctx, time.Now().UTC(), func(application.Definition) (application.Generation, error) {
		return application.Generation{}, boom
	})
	if !errors.Is(err, boom) {
		// Another test's definition may have been claimed first; only assert on ours.
		t.Logf("claimed another due definition first: %v", err)
	}
	got, _ := f.defs.Get(ctx, d.ID)
	if f.generatedTasks(d.ID) != 0 || got.NextRunAt == nil || !got.NextRunAt.Equal(scheduled) || got.Version != 1 {
		t.Errorf("a failed plan changed state: tasks=%d def=%+v", f.generatedTasks(d.ID), got)
	}
}

func TestDefinitionLifecycleAndAudit(t *testing.T) {
	f := newRecFixture(t)
	ctx := context.Background()
	d := f.create("-life", time.Now().UTC().Add(time.Hour), nil)
	if d.Version != 1 || !d.Active || d.Rule.Frequency != "daily" || d.Rule.Weekday != 0 || d.Rule.StartsOn != "2026-01-01" {
		t.Fatalf("definition = %+v", d)
	}
	paused, err := f.defs.Change(ctx, f.caller(), d.ID, func(cur application.Definition) (application.DefinitionChange, error) {
		n := cur
		n.Active, n.NextRunAt = false, nil
		return application.DefinitionChange{Next: n, Action: "tasks.recurrence.paused"}, nil
	})
	if err != nil || paused.Active || paused.NextRunAt != nil || paused.Version != 2 {
		t.Fatalf("paused = %+v %v", paused, err)
	}
	if err := f.defs.Delete(ctx, f.caller(), d.ID, intp(1)); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale delete: %v", err)
	}
	if err := f.defs.Delete(ctx, f.caller(), d.ID, intp(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.defs.Get(ctx, d.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
	if n := f.count(`SELECT count(*) FROM platform.audit_events WHERE target_type = 'recurring_task_definition' AND target_id = $1 AND correlation_id = $2`, d.ID, f.corr); n != 3 {
		t.Errorf("audit events = %d, want created, paused and deleted", n)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE target_type = 'recurring_task_definition' AND target_id = $1`, d.ID)
	})
}

func TestDefinitionDatabaseInvariants(t *testing.T) {
	f := newRecFixture(t)
	d := f.create("-inv", time.Now().UTC().Add(time.Hour), nil)
	for name, sql := range map[string]string{
		"active without next run": `UPDATE platform.recurring_task_definitions SET next_run_at = NULL WHERE id = $1`,
		"paused with next run":    `UPDATE platform.recurring_task_definitions SET active = false WHERE id = $1`,
		"weekly without weekday":  `UPDATE platform.recurring_task_definitions SET frequency = 'weekly' WHERE id = $1`,
		"daily with a weekday":    `UPDATE platform.recurring_task_definitions SET weekday = 3 WHERE id = $1`,
		"bad time of day":         `UPDATE platform.recurring_task_definitions SET time_of_day = '25:00' WHERE id = $1`,
		"zero interval":           `UPDATE platform.recurring_task_definitions SET interval_count = 0 WHERE id = $1`,
		"due offset out of range": `UPDATE platform.recurring_task_definitions SET due_after_hours = 0 WHERE id = $1`,
		"blank title":             `UPDATE platform.recurring_task_definitions SET title = '  ' WHERE id = $1`,
	} {
		if _, err := f.pool.Exec(context.Background(), sql, d.ID); err == nil {
			t.Errorf("%s: database accepted inconsistent state", name)
		}
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE platform.tasks SET scheduled_for = now() WHERE false`); err != nil {
		t.Fatal(err)
	}
}
