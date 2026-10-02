package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	tasksapp "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	tasksrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

type world struct {
	t                                   *testing.T
	pool                                *pgxpool.Pool
	creator, assignee, member, outsider string
	inactive, team                      string
	corr                                string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	pfx := "wk" + hex.EncodeToString(b)
	w := &world{t: t, pool: pool, corr: pfx}
	ctx := context.Background()
	user := func(name, status string) string {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO organization.users(display_name, status) VALUES ($1,$2) RETURNING id::text`, pfx+name, status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	w.creator, w.assignee, w.member = user(" creator", "active"), user(" assignee", "active"), user(" member", "active")
	w.outsider, w.inactive = user(" outsider", "active"), user(" inactive", "inactive")
	if err := pool.QueryRow(ctx, `INSERT INTO organization.teams(name) VALUES ($1) RETURNING id::text`, pfx+" team").Scan(&w.team); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{w.member, w.inactive, w.creator} {
		if _, err := pool.Exec(ctx, `INSERT INTO organization.team_memberships(team_id, user_id) VALUES ($1,$2)`, w.team, u); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		ids := []string{w.creator, w.assignee, w.member, w.outsider, w.inactive}
		_, _ = pool.Exec(ctx, `DELETE FROM platform.notifications WHERE recipient_user_id = ANY($1::uuid[])`, ids)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.tasks WHERE created_by_user_id = $1::uuid`, w.creator)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, w.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, w.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM organization.team_memberships WHERE team_id = $1`, w.team)
		_, _ = pool.Exec(ctx, `DELETE FROM organization.teams WHERE id = $1`, w.team)
		_, _ = pool.Exec(ctx, `DELETE FROM organization.users WHERE id = ANY($1::uuid[])`, ids)
	})
	return w
}

func (w *world) caller(user string) tasksapp.Caller {
	return tasksapp.Caller{Actor: audit.UserActor(user), CorrelationID: w.corr}
}

func (w *world) dispatch() {
	w.t.Helper()
	d := events.NewDispatcher(w.pool, events.DispatcherOptions{
		PollInterval: 10 * time.Millisecond, MaxAttempts: 2, EventTypes: []string{"TaskAssigned", "TaskCompleted"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := registerConsumers(d, w.pool); err != nil {
		w.t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		dispatched, err := d.DispatchOne(context.Background())
		if err != nil {
			w.t.Fatal(err)
		}
		if !dispatched {
			return
		}
	}
	w.t.Fatal("dispatcher did not drain")
}

func (w *world) notified(user, category string) int {
	w.t.Helper()
	var n int
	if err := w.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM platform.notifications WHERE recipient_user_id = $1::uuid AND category = $2`, user, category).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func (w *world) pendingEvents() int {
	var n int
	if err := w.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND status <> 'processed'`, w.corr).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func strp(s string) *string { return &s }

func TestAssignmentNotifiesAssigneeAndTeamMembersExceptActor(t *testing.T) {
	w := newWorld(t)
	repo := tasksrepository.New(w.pool)
	task, err := repo.Insert(context.Background(), w.caller(w.creator), tasksapp.NewTask{
		Title: "Replace toner", Priority: "normal", CreatedBy: strp(w.creator), AssignedUserID: strp(w.assignee), AssignedTeamID: strp(w.team),
	})
	if err != nil {
		t.Fatal(err)
	}
	w.dispatch()

	if w.pendingEvents() != 0 {
		t.Fatalf("%d events left unprocessed", w.pendingEvents())
	}
	if w.notified(w.assignee, "task.assigned") != 1 || w.notified(w.member, "task.assigned") != 1 {
		t.Error("the assignee and the current Team members must be notified")
	}
	if w.notified(w.creator, "task.assigned") != 0 {
		t.Error("the actor (also a Team member) must not be notified about their own action")
	}
	if w.notified(w.inactive, "task.assigned") != 0 || w.notified(w.outsider, "task.assigned") != 0 {
		t.Error("inactive users and non-members must not be notified")
	}
	var params, linkType, linkID string
	if err := w.pool.QueryRow(context.Background(), `
		SELECT params->>'title', link_type, link_id::text FROM platform.notifications
		WHERE recipient_user_id = $1::uuid AND category = 'task.assigned'`, w.assignee).Scan(&params, &linkType, &linkID); err != nil {
		t.Fatal(err)
	}
	if params != "Replace toner" || linkType != "task" || linkID != task.ID {
		t.Errorf("notification = title %q link %s:%s", params, linkType, linkID)
	}

	// Re-running the same events (retry after a crash) must not duplicate notifications.
	if _, err := w.pool.Exec(context.Background(),
		`UPDATE platform.outbox_events SET status = 'pending', available_at = now() WHERE correlation_id = $1`, w.corr); err != nil {
		t.Fatal(err)
	}
	w.dispatch()
	if w.notified(w.assignee, "task.assigned") != 1 || w.notified(w.member, "task.assigned") != 1 {
		t.Error("redelivery created duplicate notifications")
	}
}

func TestStaleAssignmentEventNotifiesNobody(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	repo := tasksrepository.New(w.pool)
	task, err := repo.Insert(ctx, w.caller(w.creator), tasksapp.NewTask{
		Title: "t", Priority: "normal", CreatedBy: strp(w.creator), AssignedUserID: strp(w.assignee),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Before the event is dispatched the task is reassigned.
	if _, err := repo.Change(ctx, w.caller(w.creator), task.ID, func(cur tasksapp.Task) (tasksapp.Change, error) {
		n := cur
		n.AssignedUserID = strp(w.outsider)
		return tasksapp.Change{Next: n, Action: "tasks.task.assigned", Events: []tasksapp.Event{
			{Type: "TaskAssigned", Payload: map[string]any{"taskId": cur.ID, "assignedUserId": w.outsider, "assignedTeamId": nil}},
		}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	w.dispatch()
	if w.notified(w.assignee, "task.assigned") != 0 {
		t.Error("the superseded assignment must not notify the previous assignee")
	}
	if w.notified(w.outsider, "task.assigned") != 1 {
		t.Error("the current assignee must be notified")
	}
}

func TestCompletionNotifiesCreatorUnlessSelf(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	repo := tasksrepository.New(w.pool)
	complete := func(by string) {
		task, err := repo.Insert(ctx, w.caller(w.creator), tasksapp.NewTask{Title: "t", Priority: "normal", CreatedBy: strp(w.creator)})
		if err != nil {
			t.Fatal(err)
		}
		_, err = repo.Change(ctx, w.caller(by), task.ID, func(cur tasksapp.Task) (tasksapp.Change, error) {
			n := cur
			n.Status = tasksapp.StatusCompleted
			now := time.Now().UTC()
			n.CompletedAt = &now
			n.CompletedByUserID = &by
			return tasksapp.Change{Next: n, Action: "tasks.task.completed", Events: []tasksapp.Event{
				{Type: "TaskCompleted", Payload: map[string]any{"taskId": cur.ID, "completedByUserId": by}},
			}}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	complete(w.creator)
	w.dispatch()
	if w.notified(w.creator, "task.completed") != 0 {
		t.Error("completing one's own task must not notify oneself")
	}
	complete(w.assignee)
	w.dispatch()
	if w.notified(w.creator, "task.completed") != 1 {
		t.Error("the creator must be notified when somebody else completes the task")
	}
}
