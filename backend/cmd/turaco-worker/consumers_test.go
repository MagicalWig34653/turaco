package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	approvalsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/application"
	approvalsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/repository"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	tasksapp "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	tasksrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
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

// grants holds the task permission of each test User; a User not listed has none.
type grants map[string][]string

func (g grants) Permissions(_ context.Context, userID string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	for _, p := range g[userID] {
		out[p] = struct{}{}
	}
	return out, nil
}

// everyone lets all of the world's Users work tasks.
func (w *world) everyone() grants {
	g := grants{}
	for _, u := range []string{w.creator, w.assignee, w.member, w.outsider, w.inactive} {
		g[u] = []string{"tasks.work"}
	}
	return g
}

func (w *world) dispatch() { w.dispatchWith(false, w.everyone()) }

func (w *world) dispatchWith(email bool, perms grants) {
	w.t.Helper()
	d := events.NewDispatcher(w.pool, events.DispatcherOptions{
		PollInterval: 10 * time.Millisecond, MaxAttempts: 2, EventTypes: allTestEventTypes,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := registerConsumersWith(d, w.pool, testCategories(w.t), email, perms, nil); err != nil {
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

func TestAssignmentSchedulesEmailDeliveriesWhenEmailIsEnabled(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = w.pool.Exec(ctx, `DELETE FROM platform.jobs WHERE job_type = 'notifications.email.send' AND payload->>'deliveryId' IN (
			SELECT d.id::text FROM platform.notification_deliveries d JOIN platform.notifications n ON n.id = d.notification_id
			WHERE n.recipient_user_id = ANY($1::uuid[]))`, []string{w.assignee, w.member, w.creator, w.outsider, w.inactive})
	})
	repo := tasksrepository.New(w.pool)
	if _, err := repo.Insert(ctx, w.caller(w.creator), tasksapp.NewTask{
		Title: "Replace toner", Priority: "normal", CreatedBy: strp(w.creator), AssignedUserID: strp(w.assignee),
	}); err != nil {
		t.Fatal(err)
	}
	w.dispatchWith(true, w.everyone())

	var deliveries, jobs int
	if err := w.pool.QueryRow(ctx, `
		SELECT count(*), count(j.id) FROM platform.notification_deliveries d
		JOIN platform.notifications n ON n.id = d.notification_id
		LEFT JOIN platform.jobs j ON j.job_type = 'notifications.email.send' AND j.payload->>'deliveryId' = d.id::text AND j.status = 'pending'
		WHERE n.recipient_user_id = $1::uuid AND d.status = 'pending'`, w.assignee).Scan(&deliveries, &jobs); err != nil {
		t.Fatal(err)
	}
	if deliveries != 1 || jobs != 1 {
		t.Errorf("deliveries=%d jobs=%d, want one pending delivery with one pending job", deliveries, jobs)
	}
}

func TestOnlyUsersWithTaskPermissionsAreNotified(t *testing.T) {
	w := newWorld(t)
	repo := tasksrepository.New(w.pool)
	if _, err := repo.Insert(context.Background(), w.caller(w.creator), tasksapp.NewTask{
		Title: "Confidential", Priority: "normal", CreatedBy: strp(w.creator), AssignedUserID: strp(w.assignee), AssignedTeamID: strp(w.team),
	}); err != nil {
		t.Fatal(err)
	}
	// Only the assignee holds a task permission; the Team member does not.
	w.dispatchWith(false, grants{w.assignee: {"tasks.work"}})
	if w.notified(w.assignee, "task.assigned") != 1 {
		t.Error("a User with tasks.work must be notified")
	}
	if w.notified(w.member, "task.assigned") != 0 {
		t.Error("a Team member without a task permission must not receive the task title")
	}
}

func TestCompletionDoesNotNotifyACreatorWhoLostTaskPermissions(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	repo := tasksrepository.New(w.pool)
	task, _ := repo.Insert(ctx, w.caller(w.creator), tasksapp.NewTask{Title: "t", Priority: "normal", CreatedBy: strp(w.creator)})
	by := w.assignee
	if _, err := repo.Change(ctx, w.caller(by), task.ID, func(cur tasksapp.Task) (tasksapp.Change, error) {
		n := cur
		n.Status = tasksapp.StatusCompleted
		now := time.Now().UTC()
		n.CompletedAt, n.CompletedByUserID = &now, &by
		return tasksapp.Change{Next: n, Action: "tasks.task.completed", Events: []tasksapp.Event{
			{Type: "TaskCompleted", Payload: map[string]any{"taskId": cur.ID, "completedByUserId": by}},
		}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	w.dispatchWith(false, grants{}) // nobody holds a task permission any more
	if w.notified(w.creator, "task.completed") != 0 {
		t.Error("the creator no longer holds a task permission and must not be notified")
	}
}

func TestRepeatedAssignmentsDoNotFloodTheRecipient(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	repo := tasksrepository.New(w.pool)
	task, err := repo.Insert(ctx, w.caller(w.creator), tasksapp.NewTask{Title: "t", Priority: "normal", CreatedBy: strp(w.creator), AssignedUserID: strp(w.assignee)})
	if err != nil {
		t.Fatal(err)
	}
	reassign := func(to *string) {
		if _, err := repo.Change(ctx, w.caller(w.creator), task.ID, func(cur tasksapp.Task) (tasksapp.Change, error) {
			n := cur
			n.AssignedUserID = to
			return tasksapp.Change{Next: n, Action: "tasks.task.assigned", Events: []tasksapp.Event{
				{Type: "TaskAssigned", Payload: map[string]any{"taskId": cur.ID, "assignedUserId": to, "assignedTeamId": nil}},
			}}, nil
		}); err != nil {
			t.Fatal(err)
		}
		w.dispatch()
	}
	w.dispatch()
	for i := 0; i < 4; i++ { // outsider, assignee, outsider, assignee ...
		reassign(strp(w.outsider))
		reassign(strp(w.assignee))
	}
	if n := w.notified(w.assignee, "task.assigned"); n != 1 {
		t.Errorf("assignee received %d notifications for one task within the window, want 1", n)
	}
}

func testCategories(t *testing.T) *notifications.Registry {
	t.Helper()
	r, err := notifications.NewRegistry(allCategories()...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestApprovalRequestedNotifiesApproversExceptTheExcluded(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	var subject string
	if err := w.pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&subject); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = w.pool.Exec(ctx, `DELETE FROM approvals.approvals WHERE subject_id = $1::uuid`, subject)
	})
	svc := approvalsapp.NewService(approvalsrepository.New(w.pool), orgpublic.NewWorkDirectory(orgrepository.New(w.pool)), nil)
	request := func(step int, in approvalsapp.RequestInput) {
		in.SubjectType, in.SubjectID, in.SubjectLabel, in.StepIndex = "service_request", subject, "REQ-1 · Laptop", step
		err := pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
			_, err := svc.RequestInTx(ctx, tx, approvalsapp.Caller{Actor: audit.UserActor(w.creator), CorrelationID: w.corr}, in)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// A single approver for one subject and the Team (members: member, inactive, creator) for another;
	// the creator is excluded. A subject has only one pending step at a time.
	request(0, approvalsapp.RequestInput{ApproverUserID: &w.assignee, ExcludedUserIDs: []string{w.creator}})
	if err := w.pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&subject); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = w.pool.Exec(ctx, `DELETE FROM approvals.approvals WHERE subject_id = $1::uuid`, subject)
	})
	request(1, approvalsapp.RequestInput{ApproverTeamID: &w.team, ExcludedUserIDs: []string{w.creator}})
	w.dispatch()

	var title, linkType string
	if err := w.pool.QueryRow(ctx, `SELECT params->>'title', link_type FROM platform.notifications WHERE recipient_user_id = $1::uuid AND category = 'approval.requested'`, w.assignee).Scan(&title, &linkType); err != nil || title != "REQ-1 · Laptop" || linkType != "approval" {
		t.Errorf("approver notification = %q %q %v", title, linkType, err)
	}
	if w.notified(w.member, "approval.requested") != 1 {
		t.Error("the Team approver must be notified (no task permission needed)")
	}
	if w.notified(w.creator, "approval.requested") != 0 {
		t.Error("the excluded requester must not be notified about approving their own request")
	}
	if w.notified(w.inactive, "approval.requested") != 0 || w.notified(w.outsider, "approval.requested") != 0 {
		t.Error("inactive users and non-members must not be notified")
	}
	if w.pendingEvents() != 0 {
		t.Errorf("%d events left unprocessed", w.pendingEvents())
	}
}

// decideApproval decides an approval as user through the approvals module.
func decideApproval(w *world, user, approvalID, decision string) error {
	svc := approvalsapp.NewService(approvalsrepository.New(w.pool), orgpublic.NewWorkDirectory(orgrepository.New(w.pool)), nil)
	_, err := svc.Decide(context.Background(), approvalsapp.Caller{Actor: audit.UserActor(user), CorrelationID: w.corr}, approvalID, decision, "", nil)
	return err
}
