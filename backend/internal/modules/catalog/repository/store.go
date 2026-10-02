// Package repository implements the catalog store on PostgreSQL.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

type Repository struct{ pool *pgxpool.Pool }

var _ application.Store = (*Repository)(nil)

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

const columns = `id::text, key, title, description, definition, active, version, created_at, updated_at`

func scan(row pgx.Row) (application.Item, error) {
	var it application.Item
	var def []byte
	if err := row.Scan(&it.ID, &it.Key, &it.Title, &it.Description, &def, &it.Active, &it.Version, &it.CreatedAt, &it.UpdatedAt); err != nil {
		return application.Item{}, err
	}
	// A stored definition was validated before it was written; decoding it
	// strictly again would make a future schema change fail old rows, so
	// unknown properties are tolerated here.
	if err := json.Unmarshal(def, &it.Definition); err != nil {
		return application.Item{}, fmt.Errorf("decode stored definition of %s: %w", it.Key, err)
	}
	return it, nil
}

// auditState holds neither title nor definition.
func auditState(it application.Item) map[string]any {
	return map[string]any{"key": it.Key, "active": it.Active, "version": it.Version, "fields": len(it.Definition.Fields),
		"approvalSteps": len(it.Definition.Approvals), "fulfillmentTasks": len(it.Definition.Fulfillment)}
}

func record(ctx context.Context, tx pgx.Tx, c application.Caller, action, id string, before, after any, meta map[string]any) error {
	if len(meta) == 0 {
		meta = nil
	}
	return audit.Record(ctx, tx, audit.Change{
		Action: action, TargetType: "catalog_item", TargetID: id, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: before, After: after, Metadata: meta,
	})
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

func (r *Repository) Insert(ctx context.Context, c application.Caller, n application.NewItem) (application.Item, error) {
	var out application.Item
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scan(tx.QueryRow(ctx, `INSERT INTO catalog.items(key, title, description, definition) VALUES ($1, $2, $3, $4::jsonb) RETURNING `+columns,
			n.Key, n.Title, n.Description, n.Definition))
		if err != nil {
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23505" {
				return application.ErrConflict
			}
			return fmt.Errorf("insert catalog item: %w", err)
		}
		return record(ctx, tx, c, "catalog.item.created", out.ID, nil, auditState(out), nil)
	})
	return out, err
}

func (r *Repository) Get(ctx context.Context, id string) (application.Item, error) {
	if !validUUID(id) {
		return application.Item{}, application.ErrNotFound
	}
	it, err := scan(r.pool.QueryRow(ctx, `SELECT `+columns+` FROM catalog.items WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Item{}, application.ErrNotFound
	}
	if err != nil {
		return application.Item{}, fmt.Errorf("get catalog item: %w", err)
	}
	return it, nil
}

func (r *Repository) Change(ctx context.Context, c application.Caller, id string, decide func(application.Item) (application.Change, error)) (application.Item, error) {
	if !validUUID(id) {
		return application.Item{}, application.ErrNotFound
	}
	var out application.Item
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		cur, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM catalog.items WHERE id = $1::uuid FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock catalog item: %w", err)
		}
		ch, err := decide(cur)
		if err != nil {
			return err
		}
		if ch.NoChange {
			out = cur
			return nil
		}
		def, err := ch.Next.Definition.Marshal()
		if err != nil {
			return err
		}
		out, err = scan(tx.QueryRow(ctx, `
			UPDATE catalog.items SET title = $2, description = $3, definition = $4::jsonb, active = $5, version = version + 1, updated_at = now()
			WHERE id = $1::uuid RETURNING `+columns, id, ch.Next.Title, ch.Next.Description, def, ch.Next.Active))
		if err != nil {
			return fmt.Errorf("update catalog item: %w", err)
		}
		return record(ctx, tx, c, ch.Action, id, auditState(cur), auditState(out), ch.Metadata)
	})
	if err != nil {
		return application.Item{}, err
	}
	return out, nil
}

func (r *Repository) List(ctx context.Context, q application.ListQuery) (application.Result, error) {
	page := q.Page.Normalize()
	var conds []string
	var args []any
	switch {
	case q.ActiveOnly || q.Status == "active":
		conds = append(conds, "active")
	case q.Status == "inactive":
		conds = append(conds, "NOT active")
	}
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.Result{}, application.ErrInvalidCursor
		}
		args = append(args, page.Cursor)
		conds = append(conds, fmt.Sprintf("id > $%d::uuid", len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM catalog.items%s ORDER BY id LIMIT $%d`, columns, where, len(args)), args...)
	if err != nil {
		return application.Result{}, fmt.Errorf("list catalog items: %w", err)
	}
	defer rows.Close()
	items := make([]application.Item, 0, page.Limit+1)
	for rows.Next() {
		it, err := scan(rows)
		if err != nil {
			return application.Result{}, fmt.Errorf("list catalog items: scan: %w", err)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return application.Result{}, fmt.Errorf("list catalog items: %w", err)
	}
	res := application.Result{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}
