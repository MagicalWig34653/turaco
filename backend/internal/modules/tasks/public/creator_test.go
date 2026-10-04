package public_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

type dir struct{ activeUser, activeTeam string }

func (d dir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, i := range ids {
		out[i] = i == d.activeUser
	}
	return out, nil
}
func (d dir) ActiveTeams(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, i := range ids {
		out[i] = i == d.activeTeam
	}
	return out, nil
}
func (dir) UserNames(context.Context, []string) (map[string]string, error) { return nil, nil }
func (dir) TeamNames(context.Context, []string) (map[string]string, error) { return nil, nil }
func (dir) CurrentTeamIDs(context.Context, string) ([]string, error)       { return nil, nil }
func (dir) CurrentMemberIDs(context.Context, string) ([]string, error)     { return nil, nil }

type env struct {
	t       *testing.T
	pool    *pgxpool.Pool
	creator *public.Creator
	corr    string
	ctxID   string
	user    string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	e := &env{t: t, pool: pool, corr: "tasks-public-" + hex.EncodeToString(b)}
	for _, dst := range []*string{&e.ctxID, &e.user} {
		if err := pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	e.creator = public.NewCreator(repository.New(pool), dir{activeUser: e.user})
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, e.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, e.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.tasks WHERE context_id = $1::uuid`, e.ctxID)
	})
	return e
}

func (e *env) caller() public.Caller {
	return public.Caller{Actor: audit.SystemActor("test"), CorrelationID: e.corr}
}

func (e *env) inTx(fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(context.Background(), e.pool, fn)
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) create(title string, assigned bool) string {
	e.t.Helper()
	var id string
	err := e.inTx(func(tx pgx.Tx) error {
		in := public.CreateInput{Title: title, ContextType: "service_request", ContextID: e.ctxID, Priority: "high"}
		if assigned {
			in.AssignedUserID = &e.user
		}
		var err error
		id, err = e.creator.CreateInTx(context.Background(), tx, e.caller(), in)
		return err
	})
	if err != nil {
		e.t.Fatalf("create: %v", err)
	}
	return id
}

func TestCreateInTxCommitsWithTheCallersTransaction(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.create("Prepare notebook", true)
	ts, err := e.creator.Tasks(ctx, []string{id, "garbage"})
	if err != nil || len(ts) != 1 || ts[0].Title != "Prepare notebook" || ts[0].Status != public.StatusOpen || ts[0].Priority != "high" {
		t.Fatalf("tasks = %+v %v", ts, err)
	}
	var ctype, cid string
	if err := e.pool.QueryRow(ctx, `SELECT context_type, context_id::text FROM platform.tasks WHERE id = $1::uuid`, id).Scan(&ctype, &cid); err != nil || ctype != "service_request" || cid != e.ctxID {
		t.Errorf("context = %q %q %v", ctype, cid, err)
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'tasks.task.created' AND target_id = $2`, e.corr, id) != 1 {
		t.Error("task creation must be audited")
	}
	if e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'TaskAssigned'`, e.corr) != 1 {
		t.Error("an assigned task emits TaskAssigned")
	}

	// A rolled back caller transaction leaves no task, audit or event behind.
	boom := errors.New("boom")
	var rolledBack string
	err = e.inTx(func(tx pgx.Tx) error {
		var err error
		rolledBack, err = e.creator.CreateInTx(ctx, tx, e.caller(), public.CreateInput{Title: "Rolled back", ContextType: "service_request", ContextID: e.ctxID})
		if err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if got, _ := e.creator.Tasks(ctx, []string{rolledBack}); len(got) != 0 {
		t.Error("a rolled back transaction left a task behind")
	}
}

func TestCreateInTxValidates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	other := "00000000-0000-7000-8000-0000000000ee"
	cases := map[string]public.CreateInput{
		"blank title":      {Title: " ", ContextType: "service_request", ContextID: e.ctxID},
		"override title":   {Title: "x‮", ContextType: "service_request", ContextID: e.ctxID},
		"bad priority":     {Title: "x", Priority: "asap", ContextType: "service_request", ContextID: e.ctxID},
		"no context":       {Title: "x"},
		"bad context type": {Title: "x", ContextType: "Service Request", ContextID: e.ctxID},
		"bad context id":   {Title: "x", ContextType: "service_request", ContextID: "nope"},
		"inactive user":    {Title: "x", ContextType: "service_request", ContextID: e.ctxID, AssignedUserID: &other},
	}
	for name, in := range cases {
		err := e.inTx(func(tx pgx.Tx) error {
			_, err := e.creator.CreateInTx(ctx, tx, e.caller(), in)
			return err
		})
		if err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if err := e.inTx(func(tx pgx.Tx) error {
		_, err := e.creator.CreateInTx(ctx, tx, public.Caller{CorrelationID: e.corr}, public.CreateInput{Title: "x", ContextType: "service_request", ContextID: e.ctxID})
		return err
	}); err == nil {
		t.Error("an invalid actor must be rejected")
	}
	if e.count(`SELECT count(*) FROM platform.tasks WHERE context_id = $1::uuid`, e.ctxID) != 0 {
		t.Error("rejected input created tasks")
	}
}

func TestCancelByContextCancelsOnlyUnfinishedTasks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	open := e.create("open", false)
	done := e.create("done", false)
	cancelled := e.create("cancelled", false)
	if _, err := e.pool.Exec(ctx, `UPDATE platform.tasks SET status = 'completed', completed_at = now() WHERE id = $1::uuid`, done); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE platform.tasks SET status = 'cancelled', status_reason = 'x' WHERE id = $1::uuid`, cancelled); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.inTx(func(tx pgx.Tx) error {
		var err error
		n, err = e.creator.CancelByContextInTx(ctx, tx, e.caller(), "service_request", e.ctxID, "request cancelled")
		return err
	}); err != nil || n != 1 {
		t.Fatalf("cancelled %d, %v; want 1", n, err)
	}
	st := map[string]string{}
	_ = e.inTx(func(tx pgx.Tx) error {
		var err error
		st, err = e.creator.StatusesInTx(ctx, tx, []string{open, done, cancelled, "garbage"})
		return err
	})
	if st[open] != public.StatusCancelled || st[done] != public.StatusCompleted || len(st) != 3 {
		t.Errorf("statuses = %v", st)
	}
	var reason string
	_ = e.pool.QueryRow(ctx, `SELECT status_reason FROM platform.tasks WHERE id = $1::uuid`, open).Scan(&reason)
	if reason != "request cancelled" {
		t.Errorf("reason = %q", reason)
	}
	if e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'TaskCancelled'`, e.corr) != 1 {
		t.Error("exactly one TaskCancelled event is expected")
	}
	// Idempotent: a second call finds nothing to cancel.
	_ = e.inTx(func(tx pgx.Tx) error {
		n, _ = e.creator.CancelByContextInTx(ctx, tx, e.caller(), "service_request", e.ctxID, "again")
		return nil
	})
	if n != 0 {
		t.Errorf("second cancel = %d, want 0", n)
	}
	if n, _ := func() (int, error) {
		var c int
		err := e.inTx(func(tx pgx.Tx) error {
			var err error
			c, err = e.creator.CancelByContextInTx(ctx, tx, e.caller(), "service_request", "garbage", "x")
			return err
		})
		return c, err
	}(); n != 0 {
		t.Error("a malformed context id cancels nothing")
	}
}

func TestManualCancelEmitsTaskCancelled(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.create("t", false)
	repo := repository.New(e.pool)
	svc := application.NewService(repo, dir{}, func() time.Time { return time.Now() })
	pm := application.Principal{UserID: e.user, Manage: true}
	if _, err := svc.Transition(ctx, application.Caller{Actor: audit.UserActor(e.user), CorrelationID: e.corr}, pm, id, nil, application.OpCancel, "not needed"); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'TaskCancelled'`, e.corr) != 1 {
		t.Error("a manual cancel must emit TaskCancelled")
	}
}

func TestContextReadAndSummary(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.create("Context task", true)
	tasks, err := e.creator.ByContext(ctx, "service_request", e.ctxID, 51)
	if err != nil || len(tasks) != 1 || tasks[0].ID != id {
		t.Fatalf("context tasks: %+v %v", tasks, err)
	}
	summary, err := e.creator.SummaryByContexts(ctx, "service_request", []string{e.ctxID})
	if err != nil || summary.Open != 1 || summary.Done != 0 {
		t.Fatalf("context summary: %+v %v", summary, err)
	}
	byType, err := e.creator.SummaryByType(ctx, "service_request")
	if err != nil || byType.Open < 1 {
		t.Fatalf("type summary: %+v %v", byType, err)
	}
}
