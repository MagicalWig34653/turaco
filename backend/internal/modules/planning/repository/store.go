// Package repository implements the Planning store on PostgreSQL.
package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/planning/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

// Repository stores Initiatives, their transitions and Milestones.
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

const cols = `id::text, reference, title, goal, owner_user_id::text, status, status_reason, target_date, approval_id::text,
	proposed_by::text, editors::text[], created_by::text, approved_at, activated_at, closed_at, version, created_at, updated_at`

func scan(row pgx.Row) (application.Initiative, error) {
	var i application.Initiative
	err := row.Scan(&i.ID, &i.Reference, &i.Title, &i.Goal, &i.OwnerID, &i.Status, &i.StatusReason, &i.TargetDate, &i.ApprovalID,
		&i.ProposedBy, &i.Editors, &i.CreatedBy, &i.ApprovedAt, &i.ActivatedAt, &i.ClosedAt, &i.Version, &i.CreatedAt, &i.UpdatedAt)
	return i, err
}

func editors(i application.Initiative) []string {
	if i.Editors == nil {
		return []string{}
	}
	return i.Editors
}

func (r *Repository) InsertTx(ctx context.Context, tx pgx.Tx, i application.Initiative) (application.Initiative, error) {
	out, err := scan(tx.QueryRow(ctx, `
		INSERT INTO planning.initiatives(title, goal, owner_user_id, status, target_date, created_by, editors)
		VALUES ($1, $2, $3::uuid, $4, $5, $6::uuid, $7::uuid[]) RETURNING `+cols,
		i.Title, i.Goal, i.OwnerID, i.Status, i.TargetDate, i.CreatedBy, editors(i)))
	if err != nil {
		return application.Initiative{}, fmt.Errorf("insert initiative: %w", err)
	}
	return out, nil
}

func (r *Repository) LockTx(ctx context.Context, tx pgx.Tx, id string) (application.Initiative, error) {
	if !validUUID(id) {
		return application.Initiative{}, application.ErrNotFound
	}
	i, err := scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM planning.initiatives WHERE id = $1::uuid FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Initiative{}, application.ErrNotFound
	}
	if err != nil {
		return application.Initiative{}, fmt.Errorf("lock initiative: %w", err)
	}
	return i, nil
}

func (r *Repository) UpdateTx(ctx context.Context, tx pgx.Tx, i application.Initiative) (application.Initiative, error) {
	out, err := scan(tx.QueryRow(ctx, `
		UPDATE planning.initiatives SET title = $2, goal = $3, owner_user_id = $4::uuid, status = $5, status_reason = $6, target_date = $7,
			approval_id = $8::uuid, proposed_by = $9::uuid, editors = $10::uuid[], approved_at = $11, activated_at = $12, closed_at = $13,
			version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+cols,
		i.ID, i.Title, i.Goal, i.OwnerID, i.Status, i.StatusReason, i.TargetDate, i.ApprovalID, i.ProposedBy, editors(i),
		i.ApprovedAt, i.ActivatedAt, i.ClosedAt))
	if err != nil {
		return application.Initiative{}, fmt.Errorf("update initiative: %w", err)
	}
	return out, nil
}

func (r *Repository) Get(ctx context.Context, id string) (application.Initiative, error) {
	if !validUUID(id) {
		return application.Initiative{}, application.ErrNotFound
	}
	i, err := scan(r.pool.QueryRow(ctx, `SELECT `+cols+` FROM planning.initiatives WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Initiative{}, application.ErrNotFound
	}
	if err != nil {
		return application.Initiative{}, fmt.Errorf("get initiative: %w", err)
	}
	return i, nil
}

// ByIDs returns the Initiatives among ids; malformed and unknown ids are absent.
func (r *Repository) ByIDs(ctx context.Context, ids []string) ([]application.Initiative, error) {
	valid := make([]string, 0, len(ids))
	for _, id := range ids {
		if validUUID(id) {
			valid = append(valid, strings.ToLower(id))
		}
	}
	if len(valid) == 0 {
		return []application.Initiative{}, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+cols+` FROM planning.initiatives WHERE id = ANY($1::uuid[]) ORDER BY id`, valid)
	if err != nil {
		return nil, fmt.Errorf("list initiatives by id: %w", err)
	}
	defer rows.Close()
	out := []application.Initiative{}
	for rows.Next() {
		i, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("list initiatives by id: scan: %w", err)
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func likePattern(q string) string {
	q = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	return "%" + q + "%"
}

// List is keyset pagination over the UUIDv7 id, newest first.
func (r *Repository) List(ctx context.Context, f application.Filter) (application.Result[application.Initiative], error) {
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.OwnerID != "" {
		add("owner_user_id = $%d::uuid", f.OwnerID)
	}
	if f.OnlyOwnerID != "" {
		add("owner_user_id = $%d::uuid", f.OnlyOwnerID)
	}
	if f.Query != "" {
		add(`(title ILIKE $%[1]d OR reference ILIKE $%[1]d)`, likePattern(f.Query))
	}
	if f.TargetFrom != nil {
		add("target_date >= $%d::date", *f.TargetFrom)
	}
	if f.TargetTo != nil {
		add("target_date <= $%d::date", *f.TargetTo)
	}
	page := f.Page.Normalize()
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.Result[application.Initiative]{}, application.ErrInvalidCursor
		}
		add("id < $%d::uuid", page.Cursor)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM planning.initiatives%s ORDER BY id DESC LIMIT $%d`, cols, where, len(args)), args...)
	if err != nil {
		return application.Result[application.Initiative]{}, fmt.Errorf("list initiatives: %w", err)
	}
	defer rows.Close()
	items := make([]application.Initiative, 0, page.Limit+1)
	for rows.Next() {
		i, err := scan(rows)
		if err != nil {
			return application.Result[application.Initiative]{}, fmt.Errorf("list initiatives: scan: %w", err)
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return application.Result[application.Initiative]{}, fmt.Errorf("list initiatives: %w", err)
	}
	res := application.Result[application.Initiative]{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

// ---- transitions ----

func (r *Repository) InsertTransitionTx(ctx context.Context, tx pgx.Tx, t application.Transition) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO planning.initiative_transitions(initiative_id, from_status, to_status, operation, reason, actor_user_id, actor_system, correlation_id)
		VALUES ($1::uuid, $2, $3, $4, $5, $6::uuid, $7, $8)`,
		t.InitiativeID, t.FromStatus, t.ToStatus, t.Operation, t.Reason, t.ActorUserID, t.ActorSystem, t.CorrelationID)
	if err != nil {
		return fmt.Errorf("insert initiative transition: %w", err)
	}
	return nil
}

func (r *Repository) Transitions(ctx context.Context, initiativeID string, page application.Page) (application.Result[application.Transition], error) {
	page = page.Normalize()
	if !validUUID(initiativeID) {
		return application.Result[application.Transition]{Items: []application.Transition{}}, nil
	}
	args := []any{initiativeID}
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
		SELECT id::text, initiative_id::text, from_status, to_status, operation, reason, actor_user_id::text, actor_system, correlation_id, created_at
		FROM planning.initiative_transitions WHERE initiative_id = $1::uuid%s ORDER BY id LIMIT $%d`, cond, len(args)), args...)
	if err != nil {
		return application.Result[application.Transition]{}, fmt.Errorf("list initiative transitions: %w", err)
	}
	defer rows.Close()
	items := []application.Transition{}
	for rows.Next() {
		var t application.Transition
		if err := rows.Scan(&t.ID, &t.InitiativeID, &t.FromStatus, &t.ToStatus, &t.Operation, &t.Reason, &t.ActorUserID, &t.ActorSystem, &t.CorrelationID, &t.CreatedAt); err != nil {
			return application.Result[application.Transition]{}, fmt.Errorf("list initiative transitions: scan: %w", err)
		}
		items = append(items, t)
	}
	if err := rows.Err(); err != nil {
		return application.Result[application.Transition]{}, fmt.Errorf("list initiative transitions: %w", err)
	}
	res := application.Result[application.Transition]{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

// ---- milestones ----

const milestoneCols = `id::text, initiative_id::text, title, due_date, position, done_at, done_by::text, removed_at, remove_reason,
	removed_by::text, created_by::text, version, created_at, updated_at`

func scanMilestone(row pgx.Row) (application.Milestone, error) {
	var m application.Milestone
	err := row.Scan(&m.ID, &m.InitiativeID, &m.Title, &m.DueDate, &m.Position, &m.DoneAt, &m.DoneBy, &m.RemovedAt, &m.RemoveReason,
		&m.RemovedBy, &m.CreatedBy, &m.Version, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}

func collectMilestones(rows pgx.Rows, what string) ([]application.Milestone, error) {
	defer rows.Close()
	out := []application.Milestone{}
	for rows.Next() {
		m, err := scanMilestone(rows)
		if err != nil {
			return nil, fmt.Errorf("%s: scan: %w", what, err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return out, nil
}

func (r *Repository) InsertMilestoneTx(ctx context.Context, tx pgx.Tx, m application.Milestone) (application.Milestone, error) {
	out, err := scanMilestone(tx.QueryRow(ctx, `
		INSERT INTO planning.milestones(initiative_id, title, due_date, position, created_by)
		VALUES ($1::uuid, $2, $3::date, $4, $5::uuid) RETURNING `+milestoneCols,
		m.InitiativeID, m.Title, m.DueDate, m.Position, m.CreatedBy))
	if err != nil {
		return application.Milestone{}, fmt.Errorf("insert milestone: %w", err)
	}
	return out, nil
}

func (r *Repository) LockMilestoneTx(ctx context.Context, tx pgx.Tx, initiativeID, id string) (application.Milestone, error) {
	if !validUUID(id) {
		return application.Milestone{}, application.ErrNotFound
	}
	m, err := scanMilestone(tx.QueryRow(ctx, `SELECT `+milestoneCols+` FROM planning.milestones WHERE id = $1::uuid AND initiative_id = $2::uuid FOR UPDATE`, id, initiativeID))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Milestone{}, application.ErrNotFound
	}
	if err != nil {
		return application.Milestone{}, fmt.Errorf("lock milestone: %w", err)
	}
	return m, nil
}

func (r *Repository) UpdateMilestoneTx(ctx context.Context, tx pgx.Tx, m application.Milestone) (application.Milestone, error) {
	out, err := scanMilestone(tx.QueryRow(ctx, `
		UPDATE planning.milestones SET title = $2, due_date = $3::date, position = $4, done_at = $5, done_by = $6::uuid,
			removed_at = $7, remove_reason = $8, removed_by = $9::uuid, version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+milestoneCols,
		m.ID, m.Title, m.DueDate, m.Position, m.DoneAt, m.DoneBy, m.RemovedAt, m.RemoveReason, m.RemovedBy))
	if err != nil {
		return application.Milestone{}, fmt.Errorf("update milestone: %w", err)
	}
	return out, nil
}

func (r *Repository) LiveMilestonesTx(ctx context.Context, tx pgx.Tx, initiativeID string) (int, int, error) {
	var count, maxPos int
	err := tx.QueryRow(ctx, `SELECT count(*), coalesce(max(position), 0) FROM planning.milestones WHERE initiative_id = $1::uuid AND removed_at IS NULL`,
		initiativeID).Scan(&count, &maxPos)
	if err != nil {
		return 0, 0, fmt.Errorf("count milestones: %w", err)
	}
	return count, maxPos, nil
}

func (r *Repository) Milestones(ctx context.Context, initiativeID string) ([]application.Milestone, error) {
	if !validUUID(initiativeID) {
		return []application.Milestone{}, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+milestoneCols+` FROM planning.milestones
		WHERE initiative_id = $1::uuid AND removed_at IS NULL ORDER BY position, due_date, id`, initiativeID)
	if err != nil {
		return nil, fmt.Errorf("list milestones: %w", err)
	}
	return collectMilestones(rows, "list milestones")
}

func (r *Repository) DueMilestones(ctx context.Context, from, to time.Time, statuses []string, ownerID string, limit int) ([]application.Milestone, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+prefixed("m.", milestoneCols)+` FROM planning.milestones m JOIN planning.initiatives i ON i.id = m.initiative_id
		WHERE m.removed_at IS NULL AND m.done_at IS NULL AND m.due_date BETWEEN $1::date AND $2::date
		  AND i.status = ANY($3::text[]) AND ($4 = '' OR i.owner_user_id::text = $4)
		ORDER BY m.due_date, m.id LIMIT $5`, from, to, statuses, ownerID, limit)
	if err != nil {
		return nil, fmt.Errorf("list due milestones: %w", err)
	}
	return collectMilestones(rows, "list due milestones")
}

// prefixed qualifies every column of a column list with the table alias.
func prefixed(alias, list string) string {
	parts := strings.Split(list, ",")
	for i, p := range parts {
		parts[i] = alias + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}
