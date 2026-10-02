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
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
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

func scan(row pgx.Row) (application.Task, error) {
	var t application.Task
	err := row.Scan(&t.ID, &t.Title, &t.Description, &t.Status, &t.StatusReason, &t.Priority,
		&t.AssignedUserID, &t.AssignedTeamID, &t.ContextType, &t.ContextID,
		&t.DueAt, &t.CompletedAt, &t.CreatedByUserID, &t.CompletedByUserID,
		&t.RecurrenceDefinitionID, &t.ScheduledFor, &t.Version, &t.CreatedAt, &t.UpdatedAt)
	return t, err
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
		out, err = scan(tx.QueryRow(ctx, `
			INSERT INTO platform.tasks(title, description, priority, due_at, assigned_user_id, assigned_team_id, created_by_user_id)
			VALUES ($1, $2, $3, $4, $5::uuid, $6::uuid, $7::uuid)
			RETURNING `+columns,
			n.Title, n.Description, n.Priority, n.DueAt, n.AssignedUserID, n.AssignedTeamID, n.CreatedBy))
		if err != nil {
			return fmt.Errorf("insert task: %w", err)
		}
		if err := record(ctx, tx, c, "tasks.task.created", out.ID, nil, auditState(out), nil); err != nil {
			return err
		}
		if out.AssignedUserID != nil || out.AssignedTeamID != nil {
			return publish(ctx, tx, c, application.Event{Type: "TaskAssigned", Payload: map[string]any{
				"taskId": out.ID, "assignedUserId": out.AssignedUserID, "assignedTeamId": out.AssignedTeamID,
				"previousUserId": nil, "previousTeamId": nil,
			}})
		}
		return nil
	})
	if err != nil {
		return application.Task{}, err
	}
	return out, nil
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
