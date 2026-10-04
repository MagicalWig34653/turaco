// Package repository implements the Changes store on PostgreSQL.
package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/changes/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

// Repository stores Changes, their transitions and their execution Task links.
type Repository struct{ pool *pgxpool.Pool }

var _ application.Store = (*Repository)(nil)

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) InTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, r.pool, fn)
}

// Q reads relationships outside a transaction.
func (r *Repository) Q() relationships.Querier { return r.pool }

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, ch := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if ch != '-' {
				return false
			}
		case !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f' || ch >= 'A' && ch <= 'F'):
			return false
		}
	}
	return true
}

const cols = `id::text, reference, title, description, kind, risk, status, status_reason, requester_user_id::text, owner_user_id::text,
	rollback_plan, emergency_justification, window_start, window_end, outcome_note, rollback_done, approval_id::text,
	approved_window_start, approved_window_end, emergency_approved_by::text,
	editors::text[], reminded_for, started_at, completed_at, closed_at, version, created_at, updated_at`

func scan(row pgx.Row) (application.Change, error) {
	var c application.Change
	err := row.Scan(&c.ID, &c.Reference, &c.Title, &c.Description, &c.Kind, &c.Risk, &c.Status, &c.StatusReason, &c.RequesterID, &c.OwnerID,
		&c.RollbackPlan, &c.EmergencyJustification, &c.WindowStart, &c.WindowEnd, &c.OutcomeNote, &c.RollbackDone, &c.ApprovalID,
		&c.ApprovedWindowStart, &c.ApprovedWindowEnd, &c.EmergencyApprovedBy, &c.Editors, &c.RemindedFor, &c.StartedAt, &c.CompletedAt, &c.ClosedAt, &c.Version, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func editors(c application.Change) []string {
	if c.Editors == nil {
		return []string{}
	}
	return c.Editors
}

func (r *Repository) InsertTx(ctx context.Context, tx pgx.Tx, c application.Change) (application.Change, error) {
	out, err := scan(tx.QueryRow(ctx, `
		INSERT INTO changes.changes(title, description, kind, risk, status, requester_user_id, owner_user_id, rollback_plan, window_start, window_end, editors)
		VALUES ($1, $2, $3, $4, $5, $6::uuid, $7::uuid, $8, $9, $10, $11::uuid[]) RETURNING `+cols,
		c.Title, c.Description, c.Kind, c.Risk, c.Status, c.RequesterID, c.OwnerID, c.RollbackPlan, c.WindowStart, c.WindowEnd, editors(c)))
	if err != nil {
		return application.Change{}, fmt.Errorf("insert change: %w", err)
	}
	return out, nil
}

func (r *Repository) LockTx(ctx context.Context, tx pgx.Tx, id string) (application.Change, error) {
	if !validUUID(id) {
		return application.Change{}, application.ErrNotFound
	}
	c, err := scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM changes.changes WHERE id = $1::uuid FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Change{}, application.ErrNotFound
	}
	if err != nil {
		return application.Change{}, fmt.Errorf("lock change: %w", err)
	}
	return c, nil
}

func (r *Repository) UpdateTx(ctx context.Context, tx pgx.Tx, c application.Change) (application.Change, error) {
	out, err := scan(tx.QueryRow(ctx, `
		UPDATE changes.changes SET title = $2, description = $3, kind = $4, risk = $5, status = $6, status_reason = $7,
			owner_user_id = $8::uuid, rollback_plan = $9, emergency_justification = $10, window_start = $11, window_end = $12,
			outcome_note = $13, rollback_done = $14, approval_id = $15::uuid, editors = $16::uuid[], reminded_for = $17,
			started_at = $18, completed_at = $19, closed_at = $20, approved_window_start = $21, approved_window_end = $22,
			emergency_approved_by = $23::uuid, version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+cols,
		c.ID, c.Title, c.Description, c.Kind, c.Risk, c.Status, c.StatusReason, c.OwnerID, c.RollbackPlan, c.EmergencyJustification,
		c.WindowStart, c.WindowEnd, c.OutcomeNote, c.RollbackDone, c.ApprovalID, editors(c), c.RemindedFor, c.StartedAt, c.CompletedAt, c.ClosedAt,
		c.ApprovedWindowStart, c.ApprovedWindowEnd, c.EmergencyApprovedBy))
	if err != nil {
		return application.Change{}, fmt.Errorf("update change: %w", err)
	}
	return out, nil
}

func (r *Repository) GetTx(ctx context.Context, tx pgx.Tx, id string) (application.Change, error) {
	if !validUUID(id) {
		return application.Change{}, application.ErrNotFound
	}
	c, err := scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM changes.changes WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Change{}, application.ErrNotFound
	}
	if err != nil {
		return application.Change{}, fmt.Errorf("get change: %w", err)
	}
	return c, nil
}

func (r *Repository) Get(ctx context.Context, id string) (application.Change, error) {
	if !validUUID(id) {
		return application.Change{}, application.ErrNotFound
	}
	c, err := scan(r.pool.QueryRow(ctx, `SELECT `+cols+` FROM changes.changes WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Change{}, application.ErrNotFound
	}
	if err != nil {
		return application.Change{}, fmt.Errorf("get change: %w", err)
	}
	return c, nil
}

// List is keyset pagination over the UUIDv7 id, newest first. The affected
// resource filter is the id set the service read through platform/relationships
// (no query on another module's tables). The "own Changes" scope is a UNION ALL
// of the requester and owner indexes instead of an OR.
func (r *Repository) List(ctx context.Context, f application.Filter) (application.Result[application.Change], error) {
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.Risk != "" {
		add("risk = $%d", f.Risk)
	}
	if f.Kind != "" {
		add("kind = $%d", f.Kind)
	}
	if f.OwnerID != "" {
		add("owner_user_id = $%d::uuid", f.OwnerID)
	}
	if f.RequesterID != "" {
		add("requester_user_id = $%d::uuid", f.RequesterID)
	}
	if f.OnlyUserID != "" {
		add(`id IN (SELECT id FROM changes.changes WHERE requester_user_id = $%[1]d::uuid
			UNION ALL SELECT id FROM changes.changes WHERE owner_user_id = $%[1]d::uuid)`, f.OnlyUserID)
	}
	if f.AffectedType != "" {
		ids := f.AffectedChangeIDs
		if ids == nil {
			ids = []string{}
		}
		add("id = ANY($%d::uuid[])", ids)
	}
	if f.WindowFrom != nil {
		// A window lasts at most 30 days (changes_window_max), so the lower bound
		// on window_start is exact and lets the window_start index narrow the scan.
		add("(window_end >= $%[1]d::timestamptz AND window_start >= $%[1]d::timestamptz - interval '30 days')", *f.WindowFrom)
	}
	if f.WindowTo != nil {
		add("window_start <= $%d", *f.WindowTo)
	}
	page := f.Page.Normalize()
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.Result[application.Change]{}, application.ErrInvalidCursor
		}
		add("id < $%d::uuid", page.Cursor)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM changes.changes c%s ORDER BY id DESC LIMIT $%d`, cols, where, len(args)), args...)
	if err != nil {
		return application.Result[application.Change]{}, fmt.Errorf("list changes: %w", err)
	}
	defer rows.Close()
	items := make([]application.Change, 0, page.Limit+1)
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return application.Result[application.Change]{}, fmt.Errorf("list changes: scan: %w", err)
		}
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		return application.Result[application.Change]{}, fmt.Errorf("list changes: %w", err)
	}
	res := application.Result[application.Change]{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

// ByIDs returns the Changes among ids; malformed and unknown ids are absent.
func (r *Repository) ByIDs(ctx context.Context, ids []string) ([]application.Change, error) {
	valid := make([]string, 0, len(ids))
	for _, id := range ids {
		if validUUID(id) {
			valid = append(valid, id)
		}
	}
	if len(valid) == 0 {
		return []application.Change{}, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+cols+` FROM changes.changes WHERE id = ANY($1::uuid[]) ORDER BY id`, valid)
	if err != nil {
		return nil, fmt.Errorf("list changes by id: %w", err)
	}
	return collectChanges(rows, "list changes by id")
}

// InWindow lists the Changes in the statuses whose window overlaps [from, to)
// (window_end > from AND window_start < to). A window lasts at most 30 days
// (changes_window_max), so window_start > from - 30 days is implied and lets
// the window_start index bound the scan.
func (r *Repository) InWindow(ctx context.Context, statuses []string, from, to time.Time, limit int) ([]application.Change, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+cols+` FROM changes.changes
		WHERE status = ANY($1::text[]) AND window_start IS NOT NULL
		  AND window_end > $2 AND window_start < $3 AND window_start > $2::timestamptz - interval '30 days'
		ORDER BY window_start, id LIMIT $4`, statuses, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("list changes in window: %w", err)
	}
	return collectChanges(rows, "list changes in window")
}

func collectChanges(rows pgx.Rows, what string) ([]application.Change, error) {
	defer rows.Close()
	out := []application.Change{}
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("%s: scan: %w", what, err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return out, nil
}

// ---- transitions ----

func (r *Repository) InsertTransitionTx(ctx context.Context, tx pgx.Tx, t application.Transition) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO changes.change_transitions(change_id, from_status, to_status, operation, reason, actor_user_id, actor_system, correlation_id)
		VALUES ($1::uuid, $2, $3, $4, $5, $6::uuid, $7, $8)`,
		t.ChangeID, t.FromStatus, t.ToStatus, t.Operation, t.Reason, t.ActorUserID, t.ActorSystem, t.CorrelationID)
	if err != nil {
		return fmt.Errorf("insert change transition: %w", err)
	}
	return nil
}

func (r *Repository) Transitions(ctx context.Context, changeID string, page application.Page) (application.Result[application.Transition], error) {
	page = page.Normalize()
	if !validUUID(changeID) {
		return application.Result[application.Transition]{Items: []application.Transition{}}, nil
	}
	args := []any{changeID}
	cond := ""
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.Result[application.Transition]{}, application.ErrInvalidCursor
		}
		args = append(args, page.Cursor)
		cond = " AND id > $2::uuid"
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`
		SELECT id::text, change_id::text, from_status, to_status, operation, reason, actor_user_id::text, actor_system, correlation_id, created_at
		FROM changes.change_transitions WHERE change_id = $1::uuid%s ORDER BY id LIMIT $%d`, cond, len(args)), args...)
	if err != nil {
		return application.Result[application.Transition]{}, fmt.Errorf("list change transitions: %w", err)
	}
	defer rows.Close()
	items := []application.Transition{}
	for rows.Next() {
		var t application.Transition
		if err := rows.Scan(&t.ID, &t.ChangeID, &t.FromStatus, &t.ToStatus, &t.Operation, &t.Reason, &t.ActorUserID, &t.ActorSystem, &t.CorrelationID, &t.CreatedAt); err != nil {
			return application.Result[application.Transition]{}, fmt.Errorf("list change transitions: scan: %w", err)
		}
		items = append(items, t)
	}
	if err := rows.Err(); err != nil {
		return application.Result[application.Transition]{}, fmt.Errorf("list change transitions: %w", err)
	}
	res := application.Result[application.Transition]{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

// ---- tasks ----

func (r *Repository) AddTaskTx(ctx context.Context, tx pgx.Tx, changeID, taskID string, createdBy *string) error {
	_, err := tx.Exec(ctx, `INSERT INTO changes.change_tasks(change_id, task_id, created_by) VALUES ($1::uuid, $2::uuid, $3::uuid)`, changeID, taskID, createdBy)
	if err != nil {
		return fmt.Errorf("link change task: %w", err)
	}
	return nil
}

func collectIDs(rows pgx.Rows, what string) ([]string, error) {
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("%s: scan: %w", what, err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (r *Repository) TaskIDsTx(ctx context.Context, tx pgx.Tx, changeID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT task_id::text FROM changes.change_tasks WHERE change_id = $1::uuid ORDER BY task_id`, changeID)
	if err != nil {
		return nil, fmt.Errorf("list change tasks: %w", err)
	}
	return collectIDs(rows, "list change tasks")
}

func (r *Repository) TaskIDs(ctx context.Context, changeID string) ([]string, error) {
	if !validUUID(changeID) {
		return []string{}, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT task_id::text FROM changes.change_tasks WHERE change_id = $1::uuid ORDER BY task_id`, changeID)
	if err != nil {
		return nil, fmt.Errorf("list change tasks: %w", err)
	}
	return collectIDs(rows, "list change tasks")
}

// ---- reminders ----

func (r *Repository) DueReminderIDs(ctx context.Context, from, until time.Time, afterID string, limit int) ([]string, error) {
	if afterID == "" {
		afterID = "00000000-0000-0000-0000-000000000000"
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id::text FROM changes.changes
		WHERE status = 'scheduled' AND window_start > $1 AND window_start <= $2
		  AND reminded_for IS DISTINCT FROM window_start AND id > $3::uuid
		ORDER BY id LIMIT $4`, from, until, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list due reminders: %w", err)
	}
	return collectIDs(rows, "list due reminders")
}

func (r *Repository) MarkRemindedTx(ctx context.Context, tx pgx.Tx, id string, windowStart time.Time) error {
	if _, err := tx.Exec(ctx, `UPDATE changes.changes SET reminded_for = $2 WHERE id = $1::uuid`, id, windowStart); err != nil {
		return fmt.Errorf("mark change reminded: %w", err)
	}
	return nil
}
