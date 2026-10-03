package repository_test

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/jackc/pgx/v5"

	catalogpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/requests/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/requests/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

func newRepo(t *testing.T) (*repository.Repository, func(sql string, args ...any) int) {
	t.Helper()
	pool := dbtest.Pool(t)
	count := func(sql string, args ...any) int {
		var n int
		if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	return repository.New(pool), count
}

func uuid(t *testing.T, r *repository.Repository) string {
	t.Helper()
	var id string
	err := r.InTx(context.Background(), func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newReq(t *testing.T, r *repository.Repository, status string, step *int) application.NewRequest {
	return application.NewRequest{
		CatalogItemID: uuid(t, r), CatalogItemKey: "laptop", CatalogItemTitle: "Laptop",
		Snapshot: []byte(`{"fields":[],"approvals":[],"fulfillment":[]}`), Answers: map[string]any{"reason": "x"},
		RequesterID: uuid(t, r), RequestedForID: uuid(t, r), Status: status, CurrentStep: step,
	}
}

func cleanup(t *testing.T, r *repository.Repository, ids ...string) {
	t.Cleanup(func() {
		_ = r.InTx(context.Background(), func(tx pgx.Tx) error {
			for _, id := range ids {
				_, _ = tx.Exec(context.Background(), `DELETE FROM requests.service_requests WHERE id = $1::uuid`, id)
			}
			return nil
		})
	})
}

func TestInsertGetListAndReferences(t *testing.T) {
	r, count := newRepo(t)
	ctx := context.Background()
	zero := 0
	n := newReq(t, r, application.StatusPendingApproval, &zero)
	var got application.Request
	if err := r.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		got, err = r.InsertTx(ctx, tx, n)
		if err != nil {
			return err
		}
		return r.AddReferencesTx(ctx, tx, got.ID, []catalogpublic.Reference{{FieldKey: "device", Type: "product", ID: uuid(t, r)}, {FieldKey: "forUser", Type: "user", ID: n.RequestedForID}})
	}); err != nil {
		t.Fatal(err)
	}
	cleanup(t, r, got.ID)
	if !regexp.MustCompile(`^REQ-\d{4}-\d{6}$`).MatchString(got.Reference) || got.Version != 1 || got.Status != "pending_approval" || got.CurrentStep == nil {
		t.Fatalf("request = %+v", got)
	}
	again, err := r.Get(ctx, got.ID)
	if err != nil || again.Answers["reason"] != "x" || again.CatalogItemKey != "laptop" {
		t.Fatalf("get = %+v %v", again, err)
	}
	refs, err := r.References(ctx, got.ID)
	if err != nil || len(refs) != 2 || refs[0].FieldKey != "device" || refs[1].Type != "user" {
		t.Errorf("references = %+v %v", refs, err)
	}
	if _, err := r.Get(ctx, "garbage"); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("malformed id: %v", err)
	}
	other := newReq(t, r, application.StatusInFulfillment, nil)
	var second application.Request
	if err := r.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		second, err = r.InsertTx(ctx, tx, other)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	cleanup(t, r, second.ID)
	if second.Reference == got.Reference {
		t.Error("references must be unique")
	}
	mine, err := r.List(ctx, application.ListQuery{UserID: n.RequesterID, Page: application.Page{Limit: 10}})
	if err != nil || len(mine.Items) != 1 || mine.Items[0].ID != got.ID {
		t.Errorf("requester list = %+v %v", mine.Items, err)
	}
	forMe, _ := r.List(ctx, application.ListQuery{UserID: n.RequestedForID, Page: application.Page{Limit: 10}})
	if len(forMe.Items) != 1 || forMe.Items[0].ID != got.ID {
		t.Errorf("requested-for list = %+v", forMe.Items)
	}
	stranger, _ := r.List(ctx, application.ListQuery{UserID: uuid(t, r), Page: application.Page{Limit: 10}})
	if len(stranger.Items) != 0 {
		t.Error("a stranger must see none")
	}
	none, _ := r.List(ctx, application.ListQuery{UserID: "garbage"})
	if len(none.Items) != 0 {
		t.Error("a malformed user id lists nothing")
	}
	all, err := r.List(ctx, application.ListQuery{All: true, Status: "in_fulfillment", Page: application.Page{Limit: 200}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range all.Items {
		found = found || it.ID == second.ID
		if it.Status != "in_fulfillment" {
			t.Errorf("status filter leaked %s", it.Status)
		}
	}
	if !found {
		t.Error("the status filter must include the fulfillment request")
	}
	if _, err := r.List(ctx, application.ListQuery{All: true, Page: application.Page{Cursor: "%%"}}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Errorf("cursor: %v", err)
	}
	if count(`SELECT count(*) FROM requests.request_references WHERE request_id = $1::uuid`, got.ID) != 2 {
		t.Error("references stored")
	}
}

func TestUpdateTasksAndCascade(t *testing.T) {
	r, count := newRepo(t)
	ctx := context.Background()
	var q application.Request
	if err := r.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		q, err = r.InsertTx(ctx, tx, newReq(t, r, application.StatusInFulfillment, nil))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	cleanup(t, r, q.ID)
	t1, t2 := uuid(t, r), uuid(t, r)
	if err := r.InTx(ctx, func(tx pgx.Tx) error {
		locked, err := r.LockTx(ctx, tx, q.ID)
		if err != nil || locked.ID != q.ID {
			return errors.New("lock failed")
		}
		if err := r.AddTaskTx(ctx, tx, q.ID, application.RequestTask{TaskID: t1, TemplateIndex: 1, Mandatory: true}); err != nil {
			return err
		}
		if err := r.AddTaskTx(ctx, tx, q.ID, application.RequestTask{TaskID: t2, TemplateIndex: 0, Mandatory: false}); err != nil {
			return err
		}
		reason := "stock"
		q.Status, q.WaitingReason = application.StatusWaiting, &reason
		up, err := r.UpdateTx(ctx, tx, q)
		if err != nil || up.Version != 2 || up.Status != "waiting" {
			return errors.New("update failed")
		}
		if id, err := r.RequestOfTaskTx(ctx, tx, t1); err != nil || id != q.ID {
			return errors.New("request of task failed")
		}
		if id, _ := r.RequestOfTaskTx(ctx, tx, uuid(t, r)); id != "" {
			return errors.New("an unrelated task has no request")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	tasks, err := r.Tasks(ctx, q.ID)
	if err != nil || len(tasks) != 2 || tasks[0].TaskID != t2 || tasks[0].Mandatory || !tasks[1].Mandatory {
		t.Errorf("tasks = %+v %v (ordered by template index)", tasks, err)
	}
	// A task belongs to at most one request.
	var other application.Request
	_ = r.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		other, err = r.InsertTx(ctx, tx, newReq(t, r, application.StatusInFulfillment, nil))
		return err
	})
	cleanup(t, r, other.ID)
	if err := r.InTx(ctx, func(tx pgx.Tx) error {
		return r.AddTaskTx(ctx, tx, other.ID, application.RequestTask{TaskID: t1})
	}); err == nil {
		t.Error("a task linked to a second request must be refused")
	}
	if _, err := r.LockTx(context.Background(), nil, "garbage"); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("lock malformed id: %v", err)
	}
	_ = count
}

func TestDatabaseInvariants(t *testing.T) {
	r, _ := newRepo(t)
	ctx := context.Background()
	var q application.Request
	_ = r.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		q, err = r.InsertTx(ctx, tx, newReq(t, r, application.StatusInFulfillment, nil))
		return err
	})
	cleanup(t, r, q.ID)
	for name, sql := range map[string]string{
		"waiting without reason":    `UPDATE requests.service_requests SET status = 'waiting' WHERE id = $1::uuid`,
		"reason while not waiting":  `UPDATE requests.service_requests SET waiting_reason = 'stock' WHERE id = $1::uuid`,
		"pending without step":      `UPDATE requests.service_requests SET status = 'pending_approval' WHERE id = $1::uuid`,
		"step while in fulfillment": `UPDATE requests.service_requests SET current_approval_step = 0 WHERE id = $1::uuid`,
		"completed without time":    `UPDATE requests.service_requests SET status = 'completed' WHERE id = $1::uuid`,
		"unknown status":            `UPDATE requests.service_requests SET status = 'approved' WHERE id = $1::uuid`,
		"unknown waiting reason":    `UPDATE requests.service_requests SET status = 'waiting', waiting_reason = 'lunch' WHERE id = $1::uuid`,
		"array answers":             `UPDATE requests.service_requests SET answers = '[]'::jsonb WHERE id = $1::uuid`,
	} {
		err := r.InTx(ctx, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql, q.ID)
			return err
		})
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
