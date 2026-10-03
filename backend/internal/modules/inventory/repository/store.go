// Package repository implements the Inventory store on PostgreSQL.
package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/inventory/application"
)

// Repository stores warehouses, the ledger, balances and reservations.
type Repository struct{ pool *pgxpool.Pool }

var _ application.Store = (*Repository)(nil)

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func pgCode(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code
	}
	return ""
}

func (r *Repository) InTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, r.pool, fn)
}

// ---- warehouses ----

const warehouseCols = `id::text, name, location_id::text, active, version, created_at, updated_at`

func scanWarehouse(row pgx.Row) (application.Warehouse, error) {
	var w application.Warehouse
	err := row.Scan(&w.ID, &w.Name, &w.LocationID, &w.Active, &w.Version, &w.CreatedAt, &w.UpdatedAt)
	return w, err
}

func (r *Repository) InsertWarehouseTx(ctx context.Context, tx pgx.Tx, name string, locationID *string) (application.Warehouse, error) {
	w, err := scanWarehouse(tx.QueryRow(ctx, `INSERT INTO inventory.warehouses(name, location_id) VALUES ($1, $2::uuid) RETURNING `+warehouseCols, name, locationID))
	if pgCode(err) == "23505" {
		return application.Warehouse{}, application.ErrConflict
	}
	if err != nil {
		return application.Warehouse{}, fmt.Errorf("insert warehouse: %w", err)
	}
	return w, nil
}

func (r *Repository) LockWarehouseTx(ctx context.Context, tx pgx.Tx, id string) (application.Warehouse, error) {
	if !validUUID(id) {
		return application.Warehouse{}, application.ErrNotFound
	}
	w, err := scanWarehouse(tx.QueryRow(ctx, `SELECT `+warehouseCols+` FROM inventory.warehouses WHERE id = $1::uuid FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Warehouse{}, application.ErrNotFound
	}
	if err != nil {
		return application.Warehouse{}, fmt.Errorf("lock warehouse: %w", err)
	}
	return w, nil
}

func (r *Repository) UpdateWarehouseTx(ctx context.Context, tx pgx.Tx, w application.Warehouse) (application.Warehouse, error) {
	out, err := scanWarehouse(tx.QueryRow(ctx, `
		UPDATE inventory.warehouses SET name = $2, location_id = $3::uuid, active = $4, version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+warehouseCols, w.ID, w.Name, w.LocationID, w.Active))
	if pgCode(err) == "23505" {
		return application.Warehouse{}, application.ErrConflict
	}
	if err != nil {
		return application.Warehouse{}, fmt.Errorf("update warehouse: %w", err)
	}
	return out, nil
}

func (r *Repository) GetWarehouse(ctx context.Context, id string) (application.Warehouse, error) {
	if !validUUID(id) {
		return application.Warehouse{}, application.ErrNotFound
	}
	w, err := scanWarehouse(r.pool.QueryRow(ctx, `SELECT `+warehouseCols+` FROM inventory.warehouses WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Warehouse{}, application.ErrNotFound
	}
	if err != nil {
		return application.Warehouse{}, fmt.Errorf("get warehouse: %w", err)
	}
	return w, nil
}

func (r *Repository) ListWarehouses(ctx context.Context, includeInactive bool, page application.Page) (application.Result[application.Warehouse], error) {
	var conds []string
	if !includeInactive {
		conds = append(conds, "active")
	}
	return listPage(ctx, r, "inventory.warehouses", warehouseCols, "id", conds, nil, page, scanWarehouse, func(w application.Warehouse) string { return w.ID })
}

// ---- storage locations ----

const locationCols = `id::text, warehouse_id::text, name, active, version, created_at, updated_at`

func scanLocation(row pgx.Row) (application.StorageLocation, error) {
	var l application.StorageLocation
	err := row.Scan(&l.ID, &l.WarehouseID, &l.Name, &l.Active, &l.Version, &l.CreatedAt, &l.UpdatedAt)
	return l, err
}

func (r *Repository) InsertLocationTx(ctx context.Context, tx pgx.Tx, warehouseID, name string) (application.StorageLocation, error) {
	l, err := scanLocation(tx.QueryRow(ctx, `INSERT INTO inventory.storage_locations(warehouse_id, name) VALUES ($1::uuid, $2) RETURNING `+locationCols, warehouseID, name))
	if pgCode(err) == "23505" {
		return application.StorageLocation{}, application.ErrConflict
	}
	if err != nil {
		return application.StorageLocation{}, fmt.Errorf("insert storage location: %w", err)
	}
	return l, nil
}

func (r *Repository) LockLocationTx(ctx context.Context, tx pgx.Tx, id string) (application.StorageLocation, error) {
	if !validUUID(id) {
		return application.StorageLocation{}, application.ErrNotFound
	}
	l, err := scanLocation(tx.QueryRow(ctx, `SELECT `+locationCols+` FROM inventory.storage_locations WHERE id = $1::uuid FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.StorageLocation{}, application.ErrNotFound
	}
	if err != nil {
		return application.StorageLocation{}, fmt.Errorf("lock storage location: %w", err)
	}
	return l, nil
}

func (r *Repository) UpdateLocationTx(ctx context.Context, tx pgx.Tx, l application.StorageLocation) (application.StorageLocation, error) {
	out, err := scanLocation(tx.QueryRow(ctx, `
		UPDATE inventory.storage_locations SET name = $2, active = $3, version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+locationCols, l.ID, l.Name, l.Active))
	if pgCode(err) == "23505" {
		return application.StorageLocation{}, application.ErrConflict
	}
	if err != nil {
		return application.StorageLocation{}, fmt.Errorf("update storage location: %w", err)
	}
	return out, nil
}

func (r *Repository) GetLocation(ctx context.Context, id string) (application.StorageLocation, error) {
	if !validUUID(id) {
		return application.StorageLocation{}, application.ErrNotFound
	}
	l, err := scanLocation(r.pool.QueryRow(ctx, `SELECT `+locationCols+` FROM inventory.storage_locations WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.StorageLocation{}, application.ErrNotFound
	}
	if err != nil {
		return application.StorageLocation{}, fmt.Errorf("get storage location: %w", err)
	}
	return l, nil
}

func (r *Repository) LocationLabels(ctx context.Context, ids []string) (map[string]string, error) {
	valid := make([]string, 0, len(ids))
	for _, id := range ids {
		if validUUID(id) {
			valid = append(valid, id)
		}
	}
	out := map[string]string{}
	if len(valid) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT l.id::text, w.name || ' / ' || l.name FROM inventory.storage_locations l JOIN inventory.warehouses w ON w.id = l.warehouse_id
		WHERE l.id = ANY($1::text[]::uuid[])`, valid)
	if err != nil {
		return nil, fmt.Errorf("storage location labels: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, label string
		if err := rows.Scan(&id, &label); err != nil {
			return nil, fmt.Errorf("storage location labels: scan: %w", err)
		}
		out[id] = label
	}
	return out, rows.Err()
}

func (r *Repository) ActiveLocationsTx(ctx context.Context, tx pgx.Tx, ids []string) (map[string]bool, error) {
	valid := make([]string, 0, len(ids))
	for _, id := range ids {
		if validUUID(id) {
			valid = append(valid, id)
		}
	}
	out := map[string]bool{}
	if len(valid) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT l.id::text FROM inventory.storage_locations l JOIN inventory.warehouses w ON w.id = l.warehouse_id
		WHERE l.id = ANY($1::text[]::uuid[]) AND l.active AND w.active`, valid)
	if err != nil {
		return nil, fmt.Errorf("active storage locations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("active storage locations: scan: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (r *Repository) ListLocations(ctx context.Context, warehouseID string, includeInactive bool, page application.Page) (application.Result[application.StorageLocation], error) {
	if !validUUID(warehouseID) {
		return application.Result[application.StorageLocation]{Items: []application.StorageLocation{}}, nil
	}
	conds := []string{"warehouse_id = $1::uuid"}
	if !includeInactive {
		conds = append(conds, "active")
	}
	return listPage(ctx, r, "inventory.storage_locations", locationCols, "id", conds, []any{warehouseID}, page, scanLocation, func(l application.StorageLocation) string { return l.ID })
}

// ---- helpers ----

func listPage[T any](ctx context.Context, r *Repository, table, columns, idCol string, conds []string, args []any, page application.Page,
	scan func(pgx.Row) (T, error), id func(T) string) (application.Result[T], error) {
	return listPageOrdered(ctx, r, table, columns, idCol, false, conds, args, page, scan, id)
}

// listPageOrdered is keyset pagination over a UUIDv7 id column, ascending or descending.
func listPageOrdered[T any](ctx context.Context, r *Repository, table, columns, idCol string, desc bool, conds []string, args []any, page application.Page,
	scan func(pgx.Row) (T, error), id func(T) string) (application.Result[T], error) {
	page = page.Normalize()
	conds = append([]string(nil), conds...)
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.Result[T]{}, application.ErrInvalidCursor
		}
		args = append(args, page.Cursor)
		op := ">"
		if desc {
			op = "<"
		}
		conds = append(conds, fmt.Sprintf("%s %s $%d::uuid", idCol, op, len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	order := "ASC"
	if desc {
		order = "DESC"
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM %s%s ORDER BY %s %s LIMIT $%d`, columns, table, where, idCol, order, len(args)), args...)
	if err != nil {
		return application.Result[T]{}, fmt.Errorf("list %s: %w", table, err)
	}
	defer rows.Close()
	items := make([]T, 0, page.Limit+1)
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return application.Result[T]{}, fmt.Errorf("list %s: scan: %w", table, err)
		}
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		return application.Result[T]{}, fmt.Errorf("list %s: %w", table, err)
	}
	res := application.Result[T]{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = id(res.Items[page.Limit-1])
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
