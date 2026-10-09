// Package repository implements the Task store on PostgreSQL with explicit SQL.
package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	taskspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

// Repository stores tasks in platform.tasks.
type Repository struct{ pool *pgxpool.Pool }

var (
	_ application.Store    = (*Repository)(nil)
	_ application.TxReader = (*Repository)(nil)
)

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

const taskTarget = "task"

const columns = `id::text, title, description, status, status_reason, priority,
	assigned_user_id::text, assigned_team_id::text, context_type, context_id::text,
	due_at, completed_at, created_by_user_id::text, completed_by_user_id::text,
	recurrence_definition_id::text, scheduled_for, version, created_at, updated_at`

func scan(row pgx.Row) (application.Task, error) { return scanWith(row) }

// scanWith scans a task row followed by extra targets (the query engine's sort keys).
func scanWith(row pgx.Row, extra ...any) (application.Task, error) {
	var t application.Task
	dest := append([]any{&t.ID, &t.Title, &t.Description, &t.Status, &t.StatusReason, &t.Priority,
		&t.AssignedUserID, &t.AssignedTeamID, &t.ContextType, &t.ContextID,
		&t.DueAt, &t.CompletedAt, &t.CreatedByUserID, &t.CompletedByUserID,
		&t.RecurrenceDefinitionID, &t.ScheduledFor, &t.Version, &t.CreatedAt, &t.UpdatedAt}, extra...)
	err := row.Scan(dest...)
	return t, err
}

// queryColumns is columns qualified with the query alias.
const queryColumns = `t.id::text, t.title, t.description, t.status, t.status_reason, t.priority,
	t.assigned_user_id::text, t.assigned_team_id::text, t.context_type, t.context_id::text,
	t.due_at, t.completed_at, t.created_by_user_id::text, t.completed_by_user_id::text,
	t.recurrence_definition_id::text, t.scheduled_for, t.version, t.created_at, t.updated_at`

// QueryTasks runs a compiled query plan (ADR-0033) in a read-only transaction.
func (r *Repository) QueryTasks(ctx context.Context, plan *query.Plan, visibility query.Fragment) (query.Page[application.Task], error) {
	return query.Run(ctx, r.pool, plan, query.Select{Columns: queryColumns, Visibility: visibility},
		func(rows pgx.Rows, extra []any) (application.Task, error) { return scanWith(rows, extra...) })
}

// auditState is the audited view of a task: no title or description.
func auditState(t application.Task) map[string]any {
	return map[string]any{
		"status": t.Status, "priority": t.Priority,
		"assignedUserId": t.AssignedUserID, "assignedTeamId": t.AssignedTeamID,
		"dueAt": t.DueAt, "version": t.Version,
	}
}

func actorID(c application.Caller) *string {
	if c.Actor.UserID == "" {
		return nil
	}
	id := c.Actor.UserID
	return &id
}

func (r *Repository) Get(ctx context.Context, id string) (application.Task, error) {
	if !validUUID(id) {
		return application.Task{}, application.ErrNotFound
	}
	t, err := scan(r.pool.QueryRow(ctx, `SELECT `+columns+` FROM platform.tasks WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Task{}, application.ErrNotFound
	}
	if err != nil {
		return application.Task{}, fmt.Errorf("get task: %w", err)
	}
	return t, nil
}

// GetTx reads a task inside the caller's transaction (outbox consumers).
func (r *Repository) GetTx(ctx context.Context, tx pgx.Tx, id string) (application.Task, error) {
	if !validUUID(id) {
		return application.Task{}, application.ErrNotFound
	}
	t, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM platform.tasks WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Task{}, application.ErrNotFound
	}
	if err != nil {
		return application.Task{}, fmt.Errorf("get task: %w", err)
	}
	return t, nil
}

func (r *Repository) Insert(ctx context.Context, c application.Caller, n application.NewTask) (application.Task, error) {
	var out application.Task
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var err error
		out, err = r.InsertTx(ctx, tx, c, n)
		return err
	})
	if err != nil {
		return application.Task{}, err
	}
	return out, nil
}

func (r *Repository) InsertTx(ctx context.Context, tx pgx.Tx, c application.Caller, n application.NewTask) (application.Task, error) {
	out, err := scan(tx.QueryRow(ctx, `
		INSERT INTO platform.tasks(title, description, priority, due_at, assigned_user_id, assigned_team_id, created_by_user_id,
		                           context_type, context_id)
		VALUES ($1, $2, $3, $4, $5::uuid, $6::uuid, $7::uuid, $8, $9::uuid)
		RETURNING `+columns,
		n.Title, n.Description, n.Priority, n.DueAt, n.AssignedUserID, n.AssignedTeamID, n.CreatedBy, n.ContextType, n.ContextID))
	if err != nil {
		return application.Task{}, fmt.Errorf("insert task: %w", err)
	}
	var meta map[string]any
	if n.ContextType != nil {
		meta = map[string]any{"contextType": *n.ContextType, "contextId": *n.ContextID}
	}
	if err := record(ctx, tx, c, "tasks.task.created", out.ID, nil, auditState(out), meta); err != nil {
		return application.Task{}, err
	}
	if out.AssignedUserID != nil || out.AssignedTeamID != nil {
		if err := publish(ctx, tx, c, application.Event{Type: "TaskAssigned", Payload: map[string]any{
			"taskId": out.ID, "assignedUserId": out.AssignedUserID, "assignedTeamId": out.AssignedTeamID,
			"previousUserId": nil, "previousTeamId": nil,
		}}); err != nil {
			return application.Task{}, err
		}
	}
	return out, nil
}

func (r *Repository) CancelByContextTx(ctx context.Context, tx pgx.Tx, c application.Caller, contextType, contextID, reason string) (int, error) {
	if !validUUID(contextID) {
		return 0, nil
	}
	rows, err := tx.Query(ctx, `SELECT `+columns+` FROM platform.tasks
		WHERE context_type = $1 AND context_id = $2::uuid AND status NOT IN ('completed', 'cancelled')
		ORDER BY id FOR UPDATE`, contextType, contextID)
	if err != nil {
		return 0, fmt.Errorf("select tasks of context: %w", err)
	}
	var open []application.Task
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			rows.Close()
			return 0, fmt.Errorf("select tasks of context: scan: %w", err)
		}
		open = append(open, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("select tasks of context: %w", err)
	}
	for _, cur := range open {
		out, err := scan(tx.QueryRow(ctx, `
			UPDATE platform.tasks SET status = 'cancelled', status_reason = $2, completed_at = NULL, completed_by_user_id = NULL,
				version = version + 1, updated_at = now()
			WHERE id = $1::uuid RETURNING `+columns, cur.ID, reason))
		if err != nil {
			return 0, fmt.Errorf("cancel task of context: %w", err)
		}
		if err := record(ctx, tx, c, "tasks.task.cancelled", cur.ID, auditState(cur), auditState(out),
			map[string]any{"reason": reason, "cause": "context_cancelled"}); err != nil {
			return 0, err
		}
		if err := publish(ctx, tx, c, application.Event{Type: "TaskCancelled", Payload: map[string]any{"taskId": cur.ID}}); err != nil {
			return 0, err
		}
	}
	return len(open), nil
}

func (r *Repository) ListByIDs(ctx context.Context, ids []string) ([]application.Task, error) {
	valid := make([]string, 0, len(ids))
	for _, id := range ids {
		if validUUID(id) {
			valid = append(valid, id)
		}
	}
	if len(valid) == 0 {
		return []application.Task{}, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+columns+` FROM platform.tasks WHERE id = ANY($1::text[]::uuid[]) ORDER BY id`, valid)
	if err != nil {
		return nil, fmt.Errorf("list tasks by ids: %w", err)
	}
	defer rows.Close()
	out := []application.Task{}
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("list tasks by ids: scan: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListByContext is a bounded read for modules that own a typed Task context.
func (r *Repository) ListByContext(ctx context.Context, contextType, contextID string, limit int) ([]application.Task, error) {
	return listByContext(ctx, r.pool, contextType, contextID, limit)
}

func (r *Repository) ListByContextTx(ctx context.Context, tx pgx.Tx, contextType, contextID string, limit int) ([]application.Task, error) {
	return listByContext(ctx, tx, contextType, contextID, limit)
}

func listByContext(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, contextType, contextID string, limit int) ([]application.Task, error) {
	rows, err := q.Query(ctx, `SELECT `+columns+` FROM platform.tasks WHERE context_type = $1 AND context_id = $2::uuid ORDER BY id LIMIT $3`, contextType, contextID, limit)
	if err != nil {
		return nil, fmt.Errorf("list tasks by context: %w", err)
	}
	defer rows.Close()
	out := []application.Task{}
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("list tasks by context: scan: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *Repository) SummaryByContexts(ctx context.Context, typ string, ids []string) (taskspublic.ContextSummary, error) {
	var out taskspublic.ContextSummary
	err := r.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status NOT IN ('completed','cancelled')),
		count(*) FILTER (WHERE status = 'completed'),
		count(*) FILTER (WHERE status = 'cancelled'),
		count(*) FILTER (WHERE status NOT IN ('completed','cancelled') AND due_at < now())
		FROM platform.tasks WHERE context_type = $1 AND context_id = ANY($2::text[]::uuid[])`, typ, ids).Scan(&out.Open, &out.Done, &out.Cancelled, &out.Overdue)
	return out, err
}

func (r *Repository) SummaryByType(ctx context.Context, typ string) (taskspublic.ContextSummary, error) {
	var out taskspublic.ContextSummary
	err := r.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status NOT IN ('completed','cancelled')),
		count(*) FILTER (WHERE status = 'completed'),
		count(*) FILTER (WHERE status = 'cancelled'),
		count(*) FILTER (WHERE status NOT IN ('completed','cancelled') AND due_at < now())
		FROM platform.tasks WHERE context_type=$1 AND context_id IS NOT NULL`, typ).Scan(&out.Open, &out.Done, &out.Cancelled, &out.Overdue)
	return out, err
}

func (r *Repository) StatusesTx(ctx context.Context, tx pgx.Tx, ids []string) (map[string]string, error) {
	out := map[string]string{}
	valid := make([]string, 0, len(ids))
	for _, id := range ids {
		if validUUID(id) {
			valid = append(valid, id)
		}
	}
	if len(valid) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text, status FROM platform.tasks WHERE id = ANY($1::text[]::uuid[]) ORDER BY id FOR SHARE`, valid)
	if err != nil {
		return nil, fmt.Errorf("task statuses: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			return nil, fmt.Errorf("task statuses: scan: %w", err)
		}
		out[id] = status
	}
	return out, rows.Err()
}

func (r *Repository) Change(ctx context.Context, c application.Caller, id string, decide func(application.Task) (application.Change, error)) (application.Task, error) {
	if !validUUID(id) {
		return application.Task{}, application.ErrNotFound
	}
	var out application.Task
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		cur, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM platform.tasks WHERE id = $1::uuid FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock task: %w", err)
		}
		ch, err := decide(cur)
		if err != nil {
			return err
		}
		if ch.NoChange {
			out = cur
			return nil
		}
		n := ch.Next
		out, err = scan(tx.QueryRow(ctx, `
			UPDATE platform.tasks SET
				title = $2, description = $3, status = $4, status_reason = $5, priority = $6,
				assigned_user_id = $7::uuid, assigned_team_id = $8::uuid, due_at = $9,
				completed_at = $10, completed_by_user_id = $11::uuid,
				version = version + 1, updated_at = now()
			WHERE id = $1::uuid
			RETURNING `+columns,
			id, n.Title, n.Description, n.Status, n.StatusReason, n.Priority,
			n.AssignedUserID, n.AssignedTeamID, n.DueAt, n.CompletedAt, n.CompletedByUserID))
		if err != nil {
			return fmt.Errorf("update task: %w", err)
		}
		if err := record(ctx, tx, c, ch.Action, id, auditState(cur), auditState(out), ch.Metadata); err != nil {
			return err
		}
		for _, e := range ch.Events {
			if err := publish(ctx, tx, c, e); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return application.Task{}, err
	}
	return out, nil
}

func record(ctx context.Context, tx pgx.Tx, c application.Caller, action, id string, before, after any, meta map[string]any) error {
	if len(meta) == 0 {
		meta = nil
	}
	return audit.Record(ctx, tx, audit.Change{
		Action: action, TargetType: taskTarget, TargetID: id, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: before, After: after, Metadata: meta,
	})
}

func publish(ctx context.Context, tx pgx.Tx, c application.Caller, e application.Event) error {
	return events.Publish(ctx, tx, events.Publication{
		Type: e.Type, ActorID: actorID(c), CorrelationID: c.CorrelationID, Payload: e.Payload,
	})
}

// ---- listing ----

// Tasks are ordered by due date (none last), then priority (urgent first),
// then id. The cursor carries the sort key of the last returned task.
const (
	dueKey  = `coalesce(due_at, 'infinity'::timestamptz)`
	rankKey = `(CASE priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END)`
)

type cursor struct {
	Due  string `json:"d"`
	Rank int    `json:"p"`
	ID   string `json:"i"`
}

func encodeCursor(t application.Task) string {
	c := cursor{Due: "infinity", ID: t.ID, Rank: rank(t.Priority)}
	if t.DueAt != nil {
		c.Due = t.DueAt.UTC().Format(time.RFC3339Nano)
	}
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func rank(priority string) int {
	switch priority {
	case application.PriorityUrgent:
		return 0
	case application.PriorityHigh:
		return 1
	case application.PriorityNormal:
		return 2
	}
	return 3
}

func decodeCursor(s string) (cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor{}, application.ErrInvalidCursor
	}
	var c cursor
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil || c.Rank < 0 || c.Rank > 3 || !validUUID(c.ID) {
		return cursor{}, application.ErrInvalidCursor
	}
	if c.Due != "infinity" {
		if _, err := time.Parse(time.RFC3339Nano, c.Due); err != nil {
			return cursor{}, application.ErrInvalidCursor
		}
	}
	return c, nil
}

func prefixPattern(q string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
}

func (r *Repository) List(ctx context.Context, q application.ListQuery) (application.Result[application.Task], error) {
	page := q.Page.Normalize()
	var (
		conds []string
		args  []any
	)
	add := func(cond string, arg any) {
		args = append(args, arg)
		conds = append(conds, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if len(q.Statuses) > 0 {
		add(`status = ANY(?::text[])`, q.Statuses)
	}
	if q.Priority != "" {
		add(`priority = ?`, q.Priority)
	}
	if q.AssignedUserID != "" {
		if !validUUID(q.AssignedUserID) {
			return application.Result[application.Task]{Items: []application.Task{}}, nil
		}
		add(`assigned_user_id = ?::uuid`, q.AssignedUserID)
	}
	if q.AssignedTeamID != "" {
		if !validUUID(q.AssignedTeamID) {
			return application.Result[application.Task]{Items: []application.Task{}}, nil
		}
		add(`assigned_team_id = ?::uuid`, q.AssignedTeamID)
	}
	if q.Overdue {
		conds = append(conds, `due_at < now() AND status NOT IN ('completed', 'cancelled')`)
	}
	if q.TitlePrefix != "" {
		add(`lower(title) LIKE lower(?) || '%'`, prefixPattern(q.TitlePrefix))
	}
	if q.Mine != nil {
		args = append(args, q.Mine.UserID, q.Mine.TeamIDs)
		conds = append(conds, fmt.Sprintf(`(assigned_user_id = $%d::uuid OR assigned_team_id = ANY($%d::text[]::uuid[]))`, len(args)-1, len(args)))
	}
	if page.Cursor != "" {
		c, err := decodeCursor(page.Cursor)
		if err != nil {
			return application.Result[application.Task]{}, err
		}
		args = append(args, c.Due, c.Rank, c.ID)
		n := len(args)
		conds = append(conds, fmt.Sprintf(`(%s, %s, id) > ($%d::timestamptz, $%d::int, $%d::uuid)`, dueKey, rankKey, n-2, n-1, n))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, `SELECT `+columns+` FROM platform.tasks`+where+
		fmt.Sprintf(` ORDER BY %s, %s, id LIMIT $%d`, dueKey, rankKey, len(args)), args...)
	if err != nil {
		return application.Result[application.Task]{}, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	items := make([]application.Task, 0, page.Limit+1)
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return application.Result[application.Task]{}, fmt.Errorf("list tasks: scan: %w", err)
		}
		items = append(items, t)
	}
	if err := rows.Err(); err != nil {
		return application.Result[application.Task]{}, fmt.Errorf("list tasks: %w", err)
	}
	res := application.Result[application.Task]{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = encodeCursor(res.Items[page.Limit-1])
	}
	return res, nil
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

func (r *Repository) OverdueByTwoTypes(ctx context.Context, typeA string, idsA []string, typeB string, idsB []string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM platform.tasks WHERE context_type IN ($1,$3) AND context_id IS NOT NULL AND due_at IS NOT NULL AND due_at<now() AND status NOT IN ('completed','cancelled') AND ((context_type=$1 AND context_id=ANY($2::text[]::uuid[])) OR (context_type=$3 AND context_id=ANY($4::text[]::uuid[])))`, typeA, idsA, typeB, idsB).Scan(&count)
	return count, err
}
