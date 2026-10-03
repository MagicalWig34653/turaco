// Package repository implements the requests store on PostgreSQL.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	catalogpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/requests/application"
)

type Repository struct{ pool *pgxpool.Pool }

var _ application.Store = (*Repository)(nil)

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

const columns = `id::text, reference, catalog_item_id::text, catalog_item_key, catalog_item_title, definition, answers,
	requester_user_id::text, requested_for_user_id::text, status, waiting_reason, status_reason, current_approval_step,
	submitted_at, completed_at, version, updated_at`

func scan(row pgx.Row) (application.Request, error) {
	var r application.Request
	var def, ans []byte
	if err := row.Scan(&r.ID, &r.Reference, &r.CatalogItemID, &r.CatalogItemKey, &r.CatalogItemTitle, &def, &ans,
		&r.RequesterID, &r.RequestedForID, &r.Status, &r.WaitingReason, &r.StatusReason, &r.CurrentStep,
		&r.SubmittedAt, &r.CompletedAt, &r.Version, &r.UpdatedAt); err != nil {
		return application.Request{}, err
	}
	d, err := catalogpublic.ParseSnapshot(def)
	if err != nil {
		return application.Request{}, fmt.Errorf("decode definition snapshot of %s: %w", r.Reference, err)
	}
	r.Definition = d
	if err := json.Unmarshal(ans, &r.Answers); err != nil {
		return application.Request{}, fmt.Errorf("decode answers of %s: %w", r.Reference, err)
	}
	return r, nil
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if r != '-' {
				return false
			}
		case !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F'):
			return false
		}
	}
	return true
}

func (r *Repository) InTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, r.pool, fn)
}

func (r *Repository) InsertTx(ctx context.Context, tx pgx.Tx, n application.NewRequest) (application.Request, error) {
	ans, err := json.Marshal(n.Answers)
	if err != nil {
		return application.Request{}, fmt.Errorf("marshal answers: %w", err)
	}
	out, err := scan(tx.QueryRow(ctx, `
		INSERT INTO requests.service_requests(catalog_item_id, catalog_item_key, catalog_item_title, definition, answers,
		                                      requester_user_id, requested_for_user_id, status, current_approval_step)
		VALUES ($1::uuid, $2, $3, $4::jsonb, $5::jsonb, $6::uuid, $7::uuid, $8, $9)
		RETURNING `+columns, n.CatalogItemID, n.CatalogItemKey, n.CatalogItemTitle, n.Snapshot, ans, n.RequesterID, n.RequestedForID, n.Status, n.CurrentStep))
	if err != nil {
		return application.Request{}, fmt.Errorf("insert service request: %w", err)
	}
	return out, nil
}

func (r *Repository) AddReferencesTx(ctx context.Context, tx pgx.Tx, requestID string, refs []catalogpublic.Reference) error {
	for _, ref := range refs {
		if _, err := tx.Exec(ctx, `INSERT INTO requests.request_references(request_id, field_key, ref_type, ref_id) VALUES ($1::uuid, $2, $3, $4::uuid)`,
			requestID, ref.FieldKey, ref.Type, ref.ID); err != nil {
			return fmt.Errorf("insert request reference: %w", err)
		}
	}
	return nil
}

func (r *Repository) LockTx(ctx context.Context, tx pgx.Tx, id string) (application.Request, error) {
	if !validUUID(id) {
		return application.Request{}, application.ErrNotFound
	}
	out, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM requests.service_requests WHERE id = $1::uuid FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Request{}, application.ErrNotFound
	}
	if err != nil {
		return application.Request{}, fmt.Errorf("lock service request: %w", err)
	}
	return out, nil
}

func (r *Repository) UpdateTx(ctx context.Context, tx pgx.Tx, q application.Request) (application.Request, error) {
	out, err := scan(tx.QueryRow(ctx, `
		UPDATE requests.service_requests SET status = $2, waiting_reason = $3, status_reason = $4, current_approval_step = $5,
			completed_at = $6, version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+columns, q.ID, q.Status, q.WaitingReason, q.StatusReason, q.CurrentStep, q.CompletedAt))
	if err != nil {
		return application.Request{}, fmt.Errorf("update service request: %w", err)
	}
	return out, nil
}

func (r *Repository) AddTaskTx(ctx context.Context, tx pgx.Tx, requestID string, t application.RequestTask) error {
	if _, err := tx.Exec(ctx, `INSERT INTO requests.request_tasks(request_id, task_id, template_index, mandatory) VALUES ($1::uuid, $2::uuid, $3, $4)`,
		requestID, t.TaskID, t.TemplateIndex, t.Mandatory); err != nil {
		return fmt.Errorf("insert request task: %w", err)
	}
	return nil
}

func scanTasks(rows pgx.Rows) ([]application.RequestTask, error) {
	defer rows.Close()
	out := []application.RequestTask{}
	for rows.Next() {
		var t application.RequestTask
		if err := rows.Scan(&t.TaskID, &t.TemplateIndex, &t.Mandatory); err != nil {
			return nil, fmt.Errorf("scan request task: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *Repository) TasksTx(ctx context.Context, tx pgx.Tx, requestID string) ([]application.RequestTask, error) {
	rows, err := tx.Query(ctx, `SELECT task_id::text, template_index, mandatory FROM requests.request_tasks WHERE request_id = $1::uuid ORDER BY template_index, task_id`, requestID)
	if err != nil {
		return nil, fmt.Errorf("list request tasks: %w", err)
	}
	return scanTasks(rows)
}

func (r *Repository) Tasks(ctx context.Context, requestID string) ([]application.RequestTask, error) {
	if !validUUID(requestID) {
		return []application.RequestTask{}, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT task_id::text, template_index, mandatory FROM requests.request_tasks WHERE request_id = $1::uuid ORDER BY template_index, task_id`, requestID)
	if err != nil {
		return nil, fmt.Errorf("list request tasks: %w", err)
	}
	return scanTasks(rows)
}

func (r *Repository) RequestOfTaskTx(ctx context.Context, tx pgx.Tx, taskID string) (string, error) {
	if !validUUID(taskID) {
		return "", nil
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT request_id::text FROM requests.request_tasks WHERE task_id = $1::uuid`, taskID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("request of task: %w", err)
	}
	return id, nil
}

func (r *Repository) Get(ctx context.Context, id string) (application.Request, error) {
	if !validUUID(id) {
		return application.Request{}, application.ErrNotFound
	}
	out, err := scan(r.pool.QueryRow(ctx, `SELECT `+columns+` FROM requests.service_requests WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Request{}, application.ErrNotFound
	}
	if err != nil {
		return application.Request{}, fmt.Errorf("get service request: %w", err)
	}
	return out, nil
}

func (r *Repository) References(ctx context.Context, requestID string) ([]catalogpublic.Reference, error) {
	if !validUUID(requestID) {
		return []catalogpublic.Reference{}, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT field_key, ref_type, ref_id::text FROM requests.request_references WHERE request_id = $1::uuid ORDER BY field_key`, requestID)
	if err != nil {
		return nil, fmt.Errorf("list request references: %w", err)
	}
	defer rows.Close()
	out := []catalogpublic.Reference{}
	for rows.Next() {
		var ref catalogpublic.Reference
		if err := rows.Scan(&ref.FieldKey, &ref.Type, &ref.ID); err != nil {
			return nil, fmt.Errorf("scan request reference: %w", err)
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

func (r *Repository) List(ctx context.Context, q application.ListQuery) (application.Result, error) {
	page := q.Page.Normalize()
	var conds []string
	var args []any
	add := func(cond string, arg any) {
		args = append(args, arg)
		conds = append(conds, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if !q.All {
		if !validUUID(q.UserID) {
			return application.Result{Items: []application.Request{}}, nil
		}
		add("(requester_user_id = ?::uuid OR requested_for_user_id = ?::uuid)", q.UserID)
		conds[len(conds)-1] = strings.Replace(conds[len(conds)-1], "?::uuid", "$1::uuid", -1)
	}
	if q.Status != "" {
		add("status = ?", q.Status)
	}
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.Result{}, application.ErrInvalidCursor
		}
		add("id < ?::uuid", page.Cursor)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM requests.service_requests%s ORDER BY id DESC LIMIT $%d`, columns, where, len(args)), args...)
	if err != nil {
		return application.Result{}, fmt.Errorf("list service requests: %w", err)
	}
	defer rows.Close()
	items := make([]application.Request, 0, page.Limit+1)
	for rows.Next() {
		it, err := scan(rows)
		if err != nil {
			return application.Result{}, fmt.Errorf("list service requests: scan: %w", err)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return application.Result{}, fmt.Errorf("list service requests: %w", err)
	}
	res := application.Result{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}
