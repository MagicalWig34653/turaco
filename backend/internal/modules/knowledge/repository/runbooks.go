package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/application"
)

var _ application.RunbookStore = (*Repository)(nil)

const runbookCols = `id::text, reference, title, description, steps, active, created_by::text, version, created_at, updated_at`

func scanRunbook(row pgx.Row) (application.Runbook, error) {
	var r application.Runbook
	var steps []byte
	if err := row.Scan(&r.ID, &r.Reference, &r.Title, &r.Description, &steps, &r.Active, &r.CreatedBy, &r.Version, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return r, err
	}
	if err := json.Unmarshal(steps, &r.Steps); err != nil {
		return r, fmt.Errorf("decode runbook steps: %w", err)
	}
	return r, nil
}

func (r *Repository) InsertRunbookTx(ctx context.Context, tx pgx.Tx, rb application.Runbook) (application.Runbook, error) {
	steps, err := json.Marshal(rb.Steps)
	if err != nil {
		return application.Runbook{}, fmt.Errorf("encode steps: %w", err)
	}
	out, err := scanRunbook(tx.QueryRow(ctx, `
		INSERT INTO knowledge.runbooks(title, description, steps, active, created_by) VALUES ($1, $2, $3::jsonb, $4, $5::uuid) RETURNING `+runbookCols,
		rb.Title, rb.Description, steps, rb.Active, rb.CreatedBy))
	if err != nil {
		return application.Runbook{}, fmt.Errorf("insert runbook: %w", err)
	}
	return out, nil
}

func (r *Repository) LockRunbookTx(ctx context.Context, tx pgx.Tx, id string) (application.Runbook, error) {
	if !validUUID(id) {
		return application.Runbook{}, application.ErrNotFound
	}
	rb, err := scanRunbook(tx.QueryRow(ctx, `SELECT `+runbookCols+` FROM knowledge.runbooks WHERE id = $1::uuid FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Runbook{}, application.ErrNotFound
	}
	if err != nil {
		return application.Runbook{}, fmt.Errorf("lock runbook: %w", err)
	}
	return rb, nil
}

func (r *Repository) UpdateRunbookTx(ctx context.Context, tx pgx.Tx, rb application.Runbook) (application.Runbook, error) {
	steps, err := json.Marshal(rb.Steps)
	if err != nil {
		return application.Runbook{}, fmt.Errorf("encode steps: %w", err)
	}
	out, err := scanRunbook(tx.QueryRow(ctx, `
		UPDATE knowledge.runbooks SET title = $2, description = $3, steps = $4::jsonb, active = $5, version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+runbookCols, rb.ID, rb.Title, rb.Description, steps, rb.Active))
	if err != nil {
		return application.Runbook{}, fmt.Errorf("update runbook: %w", err)
	}
	return out, nil
}

func (r *Repository) GetRunbook(ctx context.Context, id string) (application.Runbook, error) {
	if !validUUID(id) {
		return application.Runbook{}, application.ErrNotFound
	}
	rb, err := scanRunbook(r.pool.QueryRow(ctx, `SELECT `+runbookCols+` FROM knowledge.runbooks WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Runbook{}, application.ErrNotFound
	}
	if err != nil {
		return application.Runbook{}, fmt.Errorf("get runbook: %w", err)
	}
	return rb, nil
}

func (r *Repository) ListRunbooks(ctx context.Context, activeOnly bool, page application.Page) (application.RunbookResult, error) {
	page = page.Normalize()
	args := []any{}
	cond := "TRUE"
	if activeOnly {
		cond = "active"
	}
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.RunbookResult{}, application.ErrInvalidCursor
		}
		args = append(args, page.Cursor)
		cond += fmt.Sprintf(" AND id < $%d::uuid", len(args))
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM knowledge.runbooks WHERE %s ORDER BY id DESC LIMIT $%d`, runbookCols, cond, len(args)), args...)
	if err != nil {
		return application.RunbookResult{}, fmt.Errorf("list runbooks: %w", err)
	}
	defer rows.Close()
	items := make([]application.Runbook, 0, page.Limit+1)
	for rows.Next() {
		rb, err := scanRunbook(rows)
		if err != nil {
			return application.RunbookResult{}, fmt.Errorf("list runbooks: scan: %w", err)
		}
		items = append(items, rb)
	}
	if err := rows.Err(); err != nil {
		return application.RunbookResult{}, fmt.Errorf("list runbooks: %w", err)
	}
	res := application.RunbookResult{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

const execCols = `e.id::text, e.runbook_id::text, e.runbook_title, e.steps, e.context_type, e.context_id::text, e.status, e.started_by::text,
	e.finished_at, e.version, e.created_at, e.updated_at,
	coalesce((SELECT array_agg(t.task_id::text ORDER BY t.step_index) FROM knowledge.runbook_execution_tasks t WHERE t.execution_id = e.id), '{}')`

func scanExecution(row pgx.Row) (application.Execution, error) {
	var e application.Execution
	var steps []byte
	if err := row.Scan(&e.ID, &e.RunbookID, &e.RunbookTitle, &steps, &e.ContextType, &e.ContextID, &e.Status, &e.StartedBy, &e.FinishedAt,
		&e.Version, &e.CreatedAt, &e.UpdatedAt, &e.TaskIDs); err != nil {
		return e, err
	}
	if err := json.Unmarshal(steps, &e.Steps); err != nil {
		return e, fmt.Errorf("decode execution steps: %w", err)
	}
	return e, nil
}

func (r *Repository) InsertExecutionTx(ctx context.Context, tx pgx.Tx, e application.Execution) (application.Execution, error) {
	steps, err := json.Marshal(e.Steps)
	if err != nil {
		return application.Execution{}, fmt.Errorf("encode steps: %w", err)
	}
	out, err := scanExecution(tx.QueryRow(ctx, `
		INSERT INTO knowledge.runbook_executions AS e (runbook_id, runbook_title, steps, context_type, context_id, status, started_by)
		VALUES ($1::uuid, $2, $3::jsonb, $4, $5::uuid, $6, $7::uuid) RETURNING `+execCols,
		e.RunbookID, e.RunbookTitle, steps, e.ContextType, e.ContextID, e.Status, e.StartedBy))
	if err != nil {
		return application.Execution{}, fmt.Errorf("insert execution: %w", err)
	}
	return out, nil
}

func (r *Repository) LockExecutionTx(ctx context.Context, tx pgx.Tx, id string) (application.Execution, error) {
	if !validUUID(id) {
		return application.Execution{}, application.ErrNotFound
	}
	e, err := scanExecution(tx.QueryRow(ctx, `SELECT `+execCols+` FROM knowledge.runbook_executions e WHERE e.id = $1::uuid FOR UPDATE OF e`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Execution{}, application.ErrNotFound
	}
	if err != nil {
		return application.Execution{}, fmt.Errorf("lock execution: %w", err)
	}
	return e, nil
}

func (r *Repository) UpdateExecutionTx(ctx context.Context, tx pgx.Tx, e application.Execution) (application.Execution, error) {
	out, err := scanExecution(tx.QueryRow(ctx, `
		UPDATE knowledge.runbook_executions AS e SET status = $2, finished_at = $3, version = e.version + 1, updated_at = now()
		WHERE e.id = $1::uuid RETURNING `+execCols, e.ID, e.Status, e.FinishedAt))
	if err != nil {
		return application.Execution{}, fmt.Errorf("update execution: %w", err)
	}
	return out, nil
}

func (r *Repository) AddExecutionTaskTx(ctx context.Context, tx pgx.Tx, executionID, taskID string, step int) error {
	if _, err := tx.Exec(ctx, `INSERT INTO knowledge.runbook_execution_tasks(execution_id, task_id, step_index) VALUES ($1::uuid, $2::uuid, $3)`, executionID, taskID, step); err != nil {
		return fmt.Errorf("insert execution task: %w", err)
	}
	return nil
}

func (r *Repository) ExecutionTasksTx(ctx context.Context, tx pgx.Tx, executionID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT task_id::text FROM knowledge.runbook_execution_tasks WHERE execution_id = $1::uuid ORDER BY step_index`, executionID)
	if err != nil {
		return nil, fmt.Errorf("list execution tasks: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("list execution tasks: scan: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (r *Repository) ExecutionOfTaskTx(ctx context.Context, tx pgx.Tx, taskID string) (string, error) {
	if !validUUID(taskID) {
		return "", nil
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT execution_id::text FROM knowledge.runbook_execution_tasks WHERE task_id = $1::uuid`, taskID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("execution of task: %w", err)
	}
	return id, nil
}

func (r *Repository) GetExecution(ctx context.Context, id string) (application.Execution, error) {
	if !validUUID(id) {
		return application.Execution{}, application.ErrNotFound
	}
	e, err := scanExecution(r.pool.QueryRow(ctx, `SELECT `+execCols+` FROM knowledge.runbook_executions e WHERE e.id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Execution{}, application.ErrNotFound
	}
	if err != nil {
		return application.Execution{}, fmt.Errorf("get execution: %w", err)
	}
	return e, nil
}

func (r *Repository) ListExecutions(ctx context.Context, runbookID, contextID string, page application.Page) (application.ExecutionResult, error) {
	page = page.Normalize()
	args := []any{}
	cond := "TRUE"
	for _, f := range []struct{ v, c string }{{runbookID, "e.runbook_id"}, {contextID, "e.context_id"}} {
		if f.v == "" {
			continue
		}
		if !validUUID(f.v) {
			return application.ExecutionResult{Items: []application.Execution{}}, nil
		}
		args = append(args, f.v)
		cond += fmt.Sprintf(" AND %s = $%d::uuid", f.c, len(args))
	}
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.ExecutionResult{}, application.ErrInvalidCursor
		}
		args = append(args, page.Cursor)
		cond += fmt.Sprintf(" AND e.id < $%d::uuid", len(args))
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM knowledge.runbook_executions e WHERE %s ORDER BY e.id DESC LIMIT $%d`, execCols, cond, len(args)), args...)
	if err != nil {
		return application.ExecutionResult{}, fmt.Errorf("list executions: %w", err)
	}
	defer rows.Close()
	items := make([]application.Execution, 0, page.Limit+1)
	for rows.Next() {
		e, err := scanExecution(rows)
		if err != nil {
			return application.ExecutionResult{}, fmt.Errorf("list executions: scan: %w", err)
		}
		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		return application.ExecutionResult{}, fmt.Errorf("list executions: %w", err)
	}
	res := application.ExecutionResult{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}
