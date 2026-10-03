// Package repository implements the Services store on PostgreSQL.
package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/services/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

// Repository stores Services.
type Repository struct{ pool *pgxpool.Pool }

var _ application.Store = (*Repository)(nil)

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) InTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, r.pool, fn)
}

// Q returns the pool for relationship reads outside a transaction.
func (r *Repository) Q() relationships.Querier { return r.pool }

func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
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

const cols = `id::text, reference, name, description, owner_user_id::text, owner_team_id::text, support_team_id::text,
	criticality, status, status_reason, retired_at, version, created_by::text, created_at, updated_at`

func scan(row pgx.Row) (application.Service, error) {
	var s application.Service
	err := row.Scan(&s.ID, &s.Reference, &s.Name, &s.Description, &s.OwnerUserID, &s.OwnerTeamID, &s.SupportTeamID,
		&s.Criticality, &s.Status, &s.StatusReason, &s.RetiredAt, &s.Version, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

func one(row pgx.Row, what string) (application.Service, error) {
	s, err := scan(row)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return application.Service{}, application.ErrNotFound
	case isUnique(err):
		return application.Service{}, application.ErrConflict
	case err != nil:
		return application.Service{}, fmt.Errorf("%s: %w", what, err)
	}
	return s, nil
}

func (r *Repository) InsertTx(ctx context.Context, tx pgx.Tx, s application.Service) (application.Service, error) {
	return one(tx.QueryRow(ctx, `
		INSERT INTO services.services (name, description, owner_user_id, owner_team_id, support_team_id, criticality, status, created_by)
		VALUES ($1, $2, $3::uuid, $4::uuid, $5::uuid, $6, $7, $8::uuid) RETURNING `+cols,
		s.Name, s.Description, s.OwnerUserID, s.OwnerTeamID, s.SupportTeamID, s.Criticality, s.Status, s.CreatedBy), "insert service")
}

func (r *Repository) LockTx(ctx context.Context, tx pgx.Tx, id string) (application.Service, error) {
	if !validUUID(id) {
		return application.Service{}, application.ErrNotFound
	}
	return one(tx.QueryRow(ctx, `SELECT `+cols+` FROM services.services WHERE id = $1::uuid FOR UPDATE`, id), "lock service")
}

func (r *Repository) UpdateTx(ctx context.Context, tx pgx.Tx, s application.Service) (application.Service, error) {
	return one(tx.QueryRow(ctx, `
		UPDATE services.services SET name = $2, description = $3, owner_user_id = $4::uuid, owner_team_id = $5::uuid, support_team_id = $6::uuid,
			criticality = $7, status = $8, status_reason = $9,
			retired_at = CASE WHEN $8 = 'retired' THEN coalesce(retired_at, now()) ELSE NULL END,
			version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+cols,
		s.ID, s.Name, s.Description, s.OwnerUserID, s.OwnerTeamID, s.SupportTeamID, s.Criticality, s.Status, s.StatusReason), "update service")
}

func (r *Repository) LockDependencyGraphTx(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('services.dependency-graph', 0))`); err != nil {
		return fmt.Errorf("lock dependency graph: %w", err)
	}
	return nil
}

func (r *Repository) LockVMLinkTx(ctx context.Context, tx pgx.Tx, vmID string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('services.vm-link:' || $1::text, 0))`, vmID); err != nil {
		return fmt.Errorf("lock vm link: %w", err)
	}
	return nil
}

func (r *Repository) Get(ctx context.Context, id string) (application.Service, error) {
	if !validUUID(id) {
		return application.Service{}, application.ErrNotFound
	}
	return one(r.pool.QueryRow(ctx, `SELECT `+cols+` FROM services.services WHERE id = $1::uuid`, id), "get service")
}

func (r *Repository) ByIDs(ctx context.Context, ids []string) ([]application.Service, error) {
	valid := make([]string, 0, len(ids))
	for _, id := range ids {
		if validUUID(id) {
			valid = append(valid, id)
		}
	}
	if len(valid) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+cols+` FROM services.services WHERE id = ANY($1::uuid[])`, valid)
	if err != nil {
		return nil, fmt.Errorf("services by id: %w", err)
	}
	defer rows.Close()
	var out []application.Service
	for rows.Next() {
		s, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("services by id: scan: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (r *Repository) List(ctx context.Context, f application.Filter) (application.Result[application.Service], error) {
	page := f.Page.Normalize()
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	} else if !f.IncludeRetired {
		conds = append(conds, "status <> 'retired'")
	}
	if f.Criticality != "" {
		add("criticality = $%d", f.Criticality)
	}
	if f.OwnerUserID != "" {
		add("owner_user_id = $%d::uuid", f.OwnerUserID)
	}
	if f.TeamID != "" {
		args = append(args, f.TeamID)
		conds = append(conds, fmt.Sprintf("(owner_team_id = $%d::uuid OR support_team_id = $%d::uuid)", len(args), len(args)))
	}
	if f.Query != "" {
		add(`(name ILIKE $%d ESCAPE '\' OR reference ILIKE $%[1]d ESCAPE '\')`, "%"+likeEscape(f.Query)+"%")
	}
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.Result[application.Service]{}, application.ErrInvalidCursor
		}
		add("id > $%d::uuid", page.Cursor)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM services.services%s ORDER BY id ASC LIMIT $%d`, cols, where, len(args)), args...)
	if err != nil {
		return application.Result[application.Service]{}, fmt.Errorf("list services: %w", err)
	}
	defer rows.Close()
	items := make([]application.Service, 0, page.Limit+1)
	for rows.Next() {
		s, err := scan(rows)
		if err != nil {
			return application.Result[application.Service]{}, fmt.Errorf("list services: scan: %w", err)
		}
		items = append(items, s)
	}
	if err := rows.Err(); err != nil {
		return application.Result[application.Service]{}, fmt.Errorf("list services: %w", err)
	}
	res := application.Result[application.Service]{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}
