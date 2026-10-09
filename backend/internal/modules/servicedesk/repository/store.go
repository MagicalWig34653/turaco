// Package repository implements the Service Desk store on PostgreSQL.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

// Repository stores tickets and comments.
type Repository struct{ pool *pgxpool.Pool }

var _ application.Store = (*Repository)(nil)

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

const columns = `id::text, reference, kind, title, description, status, waiting_reason, status_reason, resolution, priority,
	reporter_user_id::text, affected_user_id::text, queue_team_id::text, assignee_user_id::text, asset_id::text, major_incident_id::text, device_snapshot,
	resolved_at, closed_at, version, created_at, updated_at`

// queryColumns is columns qualified with the query alias.
const queryColumns = `t.id::text, t.reference, t.kind, t.title, t.description, t.status, t.waiting_reason, t.status_reason, t.resolution, t.priority,
	t.reporter_user_id::text, t.affected_user_id::text, t.queue_team_id::text, t.assignee_user_id::text, t.asset_id::text, t.major_incident_id::text, t.device_snapshot,
	t.resolved_at, t.closed_at, t.version, t.created_at, t.updated_at`

func scan(row pgx.Row) (application.Ticket, error) { return scanWith(row) }

// scanWith scans a ticket row followed by extra targets (the query engine's sort keys).
func scanWith(row pgx.Row, extra ...any) (application.Ticket, error) {
	var t application.Ticket
	var snap []byte
	dest := append([]any{&t.ID, &t.Reference, &t.Kind, &t.Title, &t.Description, &t.Status, &t.WaitingReason, &t.StatusReason, &t.Resolution, &t.Priority,
		&t.ReporterID, &t.AffectedUserID, &t.QueueTeamID, &t.AssigneeID, &t.AssetID, &t.MajorIncidentID, &snap, &t.ResolvedAt, &t.ClosedAt, &t.Version, &t.CreatedAt, &t.UpdatedAt}, extra...)
	err := row.Scan(dest...)
	if err != nil {
		return t, err
	}
	if len(snap) > 0 {
		if err := json.Unmarshal(snap, &t.DeviceSnapshot); err != nil {
			return t, fmt.Errorf("decode device snapshot: %w", err)
		}
	}
	return t, nil
}

func (r *Repository) InTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, r.pool, fn)
}

func (r *Repository) InsertTx(ctx context.Context, tx pgx.Tx, t application.Ticket) (application.Ticket, error) {
	var snap []byte
	if t.DeviceSnapshot != nil {
		b, err := json.Marshal(t.DeviceSnapshot)
		if err != nil {
			return application.Ticket{}, fmt.Errorf("encode device snapshot: %w", err)
		}
		snap = b
	}
	out, err := scan(tx.QueryRow(ctx, `
		INSERT INTO servicedesk.tickets (kind, title, description, status, priority, reporter_user_id, affected_user_id, queue_team_id, asset_id, device_snapshot)
		VALUES ($1, $2, $3, $4, $5, $6::uuid, $7::uuid, $8::uuid, $9::uuid, $10::jsonb) RETURNING `+columns,
		t.Kind, t.Title, t.Description, t.Status, t.Priority, t.ReporterID, t.AffectedUserID, t.QueueTeamID, t.AssetID, snap))
	if err != nil {
		return application.Ticket{}, fmt.Errorf("insert ticket: %w", err)
	}
	return out, nil
}

func (r *Repository) LockTx(ctx context.Context, tx pgx.Tx, id string) (application.Ticket, error) {
	if !validUUID(id) {
		return application.Ticket{}, application.ErrNotFound
	}
	t, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM servicedesk.tickets WHERE id = $1::uuid FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Ticket{}, application.ErrNotFound
	}
	if err != nil {
		return application.Ticket{}, fmt.Errorf("lock ticket: %w", err)
	}
	return t, nil
}

func (r *Repository) UpdateTx(ctx context.Context, tx pgx.Tx, t application.Ticket) (application.Ticket, error) {
	out, err := scan(tx.QueryRow(ctx, `
		UPDATE servicedesk.tickets SET status = $2, waiting_reason = $3, status_reason = $4, resolution = $5, priority = $6,
			queue_team_id = $7::uuid, assignee_user_id = $8::uuid, resolved_at = $9, closed_at = $10, version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+columns,
		t.ID, t.Status, t.WaitingReason, t.StatusReason, t.Resolution, t.Priority, t.QueueTeamID, t.AssigneeID, t.ResolvedAt, t.ClosedAt))
	if err != nil {
		return application.Ticket{}, fmt.Errorf("update ticket: %w", err)
	}
	return out, nil
}

func (r *Repository) Get(ctx context.Context, id string) (application.Ticket, error) {
	if !validUUID(id) {
		return application.Ticket{}, application.ErrNotFound
	}
	t, err := scan(r.pool.QueryRow(ctx, `SELECT `+columns+` FROM servicedesk.tickets WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Ticket{}, application.ErrNotFound
	}
	if err != nil {
		return application.Ticket{}, fmt.Errorf("get ticket: %w", err)
	}
	return t, nil
}

func (r *Repository) List(ctx context.Context, f application.Filter) (application.Result, error) {
	page := f.Page.Normalize()
	var conds []string
	var args []any
	empty := application.Result{Items: []application.Ticket{}}
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.UserID != "" {
		if !validUUID(f.UserID) {
			return empty, nil
		}
		add("(reporter_user_id = $%[1]d::uuid OR affected_user_id = $%[1]d::uuid)", f.UserID)
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	for _, c := range []struct{ v, cond string }{{f.AssigneeID, "assignee_user_id = $%d::uuid"}, {f.QueueID, "queue_team_id = $%d::uuid"}} {
		if c.v == "" {
			continue
		}
		if !validUUID(c.v) {
			return empty, nil
		}
		add(c.cond, c.v)
	}
	if f.OpenOnly {
		conds = append(conds, "status IN ('new', 'open', 'in_progress', 'waiting')")
	}
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.Result{}, application.ErrInvalidCursor
		}
		add("id < $%d::uuid", page.Cursor)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM servicedesk.tickets%s ORDER BY id DESC LIMIT $%d`, columns, where, len(args)), args...)
	if err != nil {
		return application.Result{}, fmt.Errorf("list tickets: %w", err)
	}
	defer rows.Close()
	items := make([]application.Ticket, 0, page.Limit+1)
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return application.Result{}, fmt.Errorf("list tickets: scan: %w", err)
		}
		items = append(items, t)
	}
	if err := rows.Err(); err != nil {
		return application.Result{}, fmt.Errorf("list tickets: %w", err)
	}
	res := application.Result{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

// QueryTickets runs a compiled query plan (ADR-0033) in a read-only transaction.
func (r *Repository) QueryTickets(ctx context.Context, plan *query.Plan, visibility query.Fragment) (query.Page[application.Ticket], error) {
	return query.Run(ctx, r.pool, plan, query.Select{Columns: queryColumns, Visibility: visibility},
		func(rows pgx.Rows, extra []any) (application.Ticket, error) { return scanWith(rows, extra...) })
}

func (r *Repository) InsertCommentTx(ctx context.Context, tx pgx.Tx, c application.Comment) (application.Comment, error) {
	err := tx.QueryRow(ctx, `
		INSERT INTO servicedesk.ticket_comments(ticket_id, author_user_id, body, internal)
		SELECT $1::uuid, $2::uuid, $3, $4
		WHERE (SELECT count(*) FROM servicedesk.ticket_comments WHERE ticket_id = $1::uuid) < $5
		RETURNING id::text, created_at`, c.TicketID, c.AuthorID, c.Body, c.Internal, application.MaxComments).Scan(&c.ID, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Comment{}, application.ErrCommentLimit
	}
	if err != nil {
		return application.Comment{}, fmt.Errorf("insert comment: %w", err)
	}
	return c, nil
}

func (r *Repository) Comments(ctx context.Context, ticketID string, includeInternal bool) ([]application.Comment, error) {
	if !validUUID(ticketID) {
		return []application.Comment{}, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT * FROM (
			SELECT id::text, ticket_id::text, author_user_id::text, body, internal, created_at FROM servicedesk.ticket_comments
			WHERE ticket_id = $1::uuid AND ($2 OR NOT internal) ORDER BY id DESC LIMIT 500
		) newest ORDER BY created_at, id`, ticketID, includeInternal)
	if err != nil {
		return nil, fmt.Errorf("list comments: %w", err)
	}
	defer rows.Close()
	out := []application.Comment{}
	for rows.Next() {
		var c application.Comment
		if err := rows.Scan(&c.ID, &c.TicketID, &c.AuthorID, &c.Body, &c.Internal, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("list comments: scan: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
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
