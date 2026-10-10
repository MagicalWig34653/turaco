package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	repo *Repository
	corr string
	ids  []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	f := &fixture{t: t, pool: pool, repo: New(pool), corr: "tasks-test-" + hex.EncodeToString(b)}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, f.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, f.corr)
		for _, id := range f.ids {
			_, _ = pool.Exec(ctx, `DELETE FROM platform.tasks WHERE id = $1`, id)
		}
	})
	return f
}

func (f *fixture) caller() application.Caller {
	return application.Caller{Actor: audit.SystemActor("test"), CorrelationID: f.corr}
}

func (f *fixture) insert(title, priority string, due *time.Time) application.Task {
	f.t.Helper()
	tk, err := f.repo.Insert(context.Background(), f.caller(), application.NewTask{Title: title, Priority: priority, DueAt: due})
	if err != nil {
		f.t.Fatalf("insert: %v", err)
	}
	f.ids = append(f.ids, tk.ID)
	return tk
}

func (f *fixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestInsertGetRoundTrip(t *testing.T) {
	f := newFixture(t)
	due := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Microsecond)
	tk := f.insert("Replace toner", "high", &due)
	if tk.Status != "open" || tk.Version != 1 || tk.StatusReason != nil || tk.CompletedAt != nil {
		t.Errorf("inserted = %+v", tk)
	}
	got, err := f.repo.Get(context.Background(), tk.ID)
	if err != nil || got.Title != "Replace toner" || got.DueAt == nil || !got.DueAt.Equal(due) {
		t.Fatalf("get = %+v %v", got, err)
	}
	if _, err := f.repo.Get(context.Background(), "00000000-0000-7000-8000-000000000000"); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
	if _, err := f.repo.Get(context.Background(), "not-a-uuid"); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("malformed id: %v", err)
	}
	if n := f.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'tasks.task.created' AND target_id = $2`, f.corr, tk.ID); n != 1 {
		t.Errorf("created audit events = %d, want 1", n)
	}
}

func TestAuditNeverContainsTitleOrDescription(t *testing.T) {
	f := newFixture(t)
	tk := f.insert("SECRET-TITLE-xyz", "normal", nil)
	var n int
	err := f.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM platform.audit_events
		WHERE target_id = $1 AND (before_data::text LIKE '%SECRET-TITLE%' OR after_data::text LIKE '%SECRET-TITLE%' OR metadata::text LIKE '%SECRET-TITLE%')`, tk.ID).Scan(&n)
	if err != nil || n != 0 {
		t.Errorf("audit rows leaking the title: %d (%v)", n, err)
	}
}

func TestChangeIsAtomicWithAuditAndOutbox(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tk := f.insert("t", "normal", nil)
	user := "00000000-0000-7000-8000-0000000000b1"

	out, err := f.repo.Change(ctx, f.caller(), tk.ID, func(cur application.Task) (application.Change, error) {
		n := cur
		n.AssignedUserID = &user
		return application.Change{Next: n, Action: "tasks.task.assigned", Events: []application.Event{
			{Type: "TaskAssigned", Payload: map[string]any{"taskId": cur.ID, "assignedUserId": user}},
		}}, nil
	})
	if err != nil || out.Version != 2 || out.AssignedUserID == nil {
		t.Fatalf("change = %+v %v", out, err)
	}
	if n := f.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'tasks.task.assigned' AND target_id = $2`, f.corr, tk.ID); n != 1 {
		t.Errorf("assigned audit events = %d", n)
	}
	if n := f.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'TaskAssigned' AND payload->>'taskId' = $2`, f.corr, tk.ID); n != 1 {
		t.Errorf("TaskAssigned outbox events = %d", n)
	}

	// A decision error rolls everything back.
	boom := errors.New("boom")
	if _, err := f.repo.Change(ctx, f.caller(), tk.ID, func(cur application.Task) (application.Change, error) { return application.Change{}, boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	// An unregistered event type fails the whole change, including the task update.
	_, err = f.repo.Change(ctx, f.caller(), tk.ID, func(cur application.Task) (application.Change, error) {
		n := cur
		n.Title = "changed"
		return application.Change{Next: n, Action: "tasks.task.details_updated", Events: []application.Event{{Type: "NoSuchEvent", Payload: map[string]any{}}}}, nil
	})
	if err == nil {
		t.Fatal("unregistered event must fail the change")
	}
	got, _ := f.repo.Get(ctx, tk.ID)
	if got.Title != "t" || got.Version != 2 {
		t.Errorf("failed change leaked: %+v", got)
	}
	// NoChange leaves version, audit and outbox alone.
	before := f.count(`SELECT count(*) FROM platform.audit_events WHERE target_id = $1`, tk.ID)
	same, err := f.repo.Change(ctx, f.caller(), tk.ID, func(cur application.Task) (application.Change, error) {
		return application.Change{NoChange: true, Next: cur}, nil
	})
	if err != nil || same.Version != 2 || f.count(`SELECT count(*) FROM platform.audit_events WHERE target_id = $1`, tk.ID) != before {
		t.Errorf("no-op changed state: %+v %v", same, err)
	}
	if _, err := f.repo.Change(ctx, f.caller(), "00000000-0000-7000-8000-000000000000", func(application.Task) (application.Change, error) {
		return application.Change{NoChange: true}, nil
	}); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
}

func TestDatabaseRejectsInconsistentState(t *testing.T) {
	f := newFixture(t)
	tk := f.insert("t", "normal", nil)
	for name, sql := range map[string]string{
		"blocked without reason":  `UPDATE platform.tasks SET status = 'blocked' WHERE id = $1`,
		"reason on open task":     `UPDATE platform.tasks SET status_reason = 'x' WHERE id = $1`,
		"completed without time":  `UPDATE platform.tasks SET status = 'completed' WHERE id = $1`,
		"completed_at while open": `UPDATE platform.tasks SET completed_at = now() WHERE id = $1`,
		"context type only":       `UPDATE platform.tasks SET context_type = 'ticket' WHERE id = $1`,
		"empty title":             `UPDATE platform.tasks SET title = '' WHERE id = $1`,
		"zero version":            `UPDATE platform.tasks SET version = 0 WHERE id = $1`,
	} {
		if _, err := f.pool.Exec(context.Background(), sql, tk.ID); err == nil {
			t.Errorf("%s: database accepted inconsistent state", name)
		}
	}
}

// Two concurrent completions: exactly one performs the transition, the other
// sees the completed state and is rejected by the decision function.
func TestConcurrentChangesAreSerialized(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tk := f.insert("t", "normal", nil)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.repo.Change(ctx, f.caller(), tk.ID, func(cur application.Task) (application.Change, error) {
				if cur.Status != "open" {
					return application.Change{}, &application.InvalidTransitionError{Operation: "complete", From: cur.Status}
				}
				n := cur
				n.Status = "completed"
				now := time.Now().UTC()
				n.CompletedAt = &now
				return application.Change{Next: n, Action: "tasks.task.completed", Events: []application.Event{
					{Type: "TaskCompleted", Payload: map[string]any{"taskId": cur.ID}},
				}}, nil
			})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	var ok, rejected int
	for err := range results {
		var tr *application.InvalidTransitionError
		switch {
		case err == nil:
			ok++
		case errors.As(err, &tr):
			rejected++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || rejected != 7 {
		t.Errorf("ok=%d rejected=%d, want 1 and 7", ok, rejected)
	}
	if n := f.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'TaskCompleted'`, f.corr); n != 1 {
		t.Errorf("TaskCompleted events = %d, want 1", n)
	}
	got, _ := f.repo.Get(ctx, tk.ID)
	if got.Version != 2 {
		t.Errorf("version = %d, want 2", got.Version)
	}
}

func TestListOrderAndKeysetPagination(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	soon := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	later := soon.Add(24 * time.Hour)
	// Expected order: due soon (urgent, low), due later (normal), no due (high, normal).
	want := []string{
		f.insert("a-soon-urgent", "urgent", &soon).ID,
		f.insert("b-soon-low", "low", &soon).ID,
		f.insert("c-later", "normal", &later).ID,
		f.insert("d-nodue-high", "high", nil).ID,
		f.insert("e-nodue-normal", "normal", nil).ID,
	}
	// Restrict to this test's tasks through a title prefix of unique letters is
	// not possible; use a large page and filter by the fixture's ids instead.
	known := map[string]bool{}
	for _, id := range want {
		known[id] = true
	}
	var got []string
	cursor := ""
	for page := 0; page < 1000; page++ {
		res, err := f.repo.List(ctx, application.ListQuery{Page: application.Page{Limit: 3, Cursor: cursor}})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Items) > 3 {
			t.Fatalf("page of %d items exceeds the limit", len(res.Items))
		}
		for _, tk := range res.Items {
			if known[tk.ID] {
				got = append(got, tk.ID)
			}
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	if len(got) != len(want) {
		t.Fatalf("got %d of %d tasks while paging", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestListFilters(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	user := "00000000-0000-7000-8000-0000000000b1"
	team := "00000000-0000-7000-8000-0000000000e1"
	past := time.Now().UTC().Add(-time.Hour)
	overdue := f.insert("zz-overdue", "normal", &past)
	mine, _ := f.repo.Change(ctx, f.caller(), f.insert("zz-mine", "normal", nil).ID, func(cur application.Task) (application.Change, error) {
		n := cur
		n.AssignedUserID = &user
		return application.Change{Next: n, Action: "tasks.task.assigned"}, nil
	})
	viaTeam, _ := f.repo.Change(ctx, f.caller(), f.insert("zz-team", "normal", nil).ID, func(cur application.Task) (application.Change, error) {
		n := cur
		n.AssignedTeamID = &team
		return application.Change{Next: n, Action: "tasks.task.assigned"}, nil
	})
	done, _ := f.repo.Change(ctx, f.caller(), f.insert("zz-done", "normal", &past).ID, func(cur application.Task) (application.Change, error) {
		n := cur
		n.Status = "completed"
		now := time.Now().UTC()
		n.CompletedAt = &now
		return application.Change{Next: n, Action: "tasks.task.completed"}, nil
	})

	ids := func(q application.ListQuery) map[string]bool {
		q.Page = application.Page{Limit: 200}
		res, err := f.repo.List(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, tk := range res.Items {
			out[tk.ID] = true
		}
		return out
	}
	if got := ids(application.ListQuery{Mine: &application.Mine{UserID: user, TeamIDs: []string{team}}}); !got[mine.ID] || !got[viaTeam.ID] || got[overdue.ID] {
		t.Errorf("mine filter = %v", got)
	}
	if got := ids(application.ListQuery{Mine: &application.Mine{UserID: user}}); !got[mine.ID] || got[viaTeam.ID] {
		t.Errorf("mine without teams = %v", got)
	}
	if got := ids(application.ListQuery{Overdue: true}); !got[overdue.ID] || got[done.ID] || got[mine.ID] {
		t.Errorf("overdue filter must exclude finished and undated tasks: %v", got)
	}
	if got := ids(application.ListQuery{Statuses: []string{"completed"}, TitlePrefix: "ZZ-"}); !got[done.ID] || got[overdue.ID] {
		t.Errorf("status+prefix filter = %v", got)
	}
	if got := ids(application.ListQuery{TitlePrefix: "zz-%"}); len(got) != 0 {
		t.Errorf("LIKE metacharacters must be literal: %v", got)
	}
	if got := ids(application.ListQuery{AssignedUserID: user}); !got[mine.ID] || got[viaTeam.ID] {
		t.Errorf("assigned user filter = %v", got)
	}
	if got := ids(application.ListQuery{AssignedUserID: "garbage"}); len(got) != 0 {
		t.Errorf("malformed id filter = %v", got)
	}
	if _, err := f.repo.List(ctx, application.ListQuery{Page: application.Page{Cursor: "%%%"}}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Errorf("invalid cursor: %v", err)
	}
}

func TestResultNoteIsStoredWithCompletionAndBoundedByTheDatabase(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tk := f.insert("t", "normal", nil)
	for name, sql := range map[string]string{
		"note on an open task": `UPDATE platform.tasks SET result_note = 'x' WHERE id = $1`,
		"empty note":           `UPDATE platform.tasks SET status = 'completed', completed_at = now(), result_note = '' WHERE id = $1`,
		"overlong note":        `UPDATE platform.tasks SET status = 'completed', completed_at = now(), result_note = repeat('x', 1001) WHERE id = $1`,
	} {
		if _, err := f.pool.Exec(ctx, sql, tk.ID); err == nil {
			t.Errorf("%s: database accepted it", name)
		}
	}
	note := "Erledigt, siehe Hersteller-Hinweis."
	done, err := f.repo.Change(ctx, f.caller(), tk.ID, func(cur application.Task) (application.Change, error) {
		n, now := cur, time.Now().UTC().Truncate(time.Microsecond)
		n.Status, n.CompletedAt, n.ResultNote = application.StatusCompleted, &now, &note
		return application.Change{Next: n, Action: "tasks.task.completed"}, nil
	})
	if err != nil || done.ResultNote == nil || *done.ResultNote != note {
		t.Fatalf("complete with note = %+v %v", done, err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE platform.tasks SET result_note_for_requester = true WHERE id = $1`, f.insert("no note", "normal", nil).ID); err == nil {
		t.Error("database accepted the requester flag without a note")
	}
	shared, err := f.repo.Change(ctx, f.caller(), tk.ID, func(cur application.Task) (application.Change, error) {
		n := cur
		n.ResultNoteForRequester = true
		return application.Change{Next: n, Action: "tasks.task.completed"}, nil
	})
	if err != nil || !shared.ResultNoteForRequester {
		t.Fatalf("flag = %+v %v", shared, err)
	}
	reopened, err := f.repo.Change(ctx, f.caller(), tk.ID, func(cur application.Task) (application.Change, error) {
		n := cur
		n.Status, n.CompletedAt, n.ResultNote, n.ResultNoteForRequester = application.StatusOpen, nil, nil, false
		return application.Change{Next: n, Action: "tasks.task.reopened"}, nil
	})
	if err != nil || reopened.ResultNote != nil || reopened.ResultNoteForRequester {
		t.Errorf("reopen keeps the note: %+v %v", reopened, err)
	}
}
