// Package repository implements the Procurement store on PostgreSQL.
package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/procurement/application"
)

// Repository stores suppliers, procurement requests and purchase orders.
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

func prefixPattern(q string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
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

// listPage is keyset pagination over a UUIDv7 id column, newest first when desc.
func listPage[T any](ctx context.Context, r *Repository, table, columns string, desc bool, conds []string, args []any, page application.Page,
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
		conds = append(conds, fmt.Sprintf("id %s $%d::uuid", op, len(args)))
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
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM %s%s ORDER BY id %s LIMIT $%d`, columns, table, where, order, len(args)), args...)
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

// lock reads one row FOR UPDATE and maps a missing row to ErrNotFound.
func lock[T any](ctx context.Context, tx pgx.Tx, table, columns, id string, scan func(pgx.Row) (T, error)) (T, error) {
	var zero T
	if !validUUID(id) {
		return zero, application.ErrNotFound
	}
	v, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM `+table+` WHERE id = $1::uuid FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, application.ErrNotFound
	}
	if err != nil {
		return zero, fmt.Errorf("lock %s: %w", table, err)
	}
	return v, nil
}

func get[T any](ctx context.Context, r *Repository, table, columns, id string, scan func(pgx.Row) (T, error)) (T, error) {
	var zero T
	if !validUUID(id) {
		return zero, application.ErrNotFound
	}
	v, err := scan(r.pool.QueryRow(ctx, `SELECT `+columns+` FROM `+table+` WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, application.ErrNotFound
	}
	if err != nil {
		return zero, fmt.Errorf("get %s: %w", table, err)
	}
	return v, nil
}

// ---- suppliers ----

const supplierCols = `id::text, name, account_reference, active, version, created_at, updated_at`

func scanSupplier(row pgx.Row) (application.Supplier, error) {
	var s application.Supplier
	err := row.Scan(&s.ID, &s.Name, &s.AccountReference, &s.Active, &s.Version, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

func (r *Repository) InsertSupplierTx(ctx context.Context, tx pgx.Tx, name string, account *string) (application.Supplier, error) {
	s, err := scanSupplier(tx.QueryRow(ctx, `INSERT INTO procurement.suppliers(name, account_reference) VALUES ($1, $2) RETURNING `+supplierCols, name, account))
	if pgCode(err) == "23505" {
		return application.Supplier{}, application.ErrConflict
	}
	if err != nil {
		return application.Supplier{}, fmt.Errorf("insert supplier: %w", err)
	}
	return s, nil
}

func (r *Repository) LockSupplierTx(ctx context.Context, tx pgx.Tx, id string) (application.Supplier, error) {
	return lock(ctx, tx, "procurement.suppliers", supplierCols, id, scanSupplier)
}

func (r *Repository) UpdateSupplierTx(ctx context.Context, tx pgx.Tx, s application.Supplier) (application.Supplier, error) {
	out, err := scanSupplier(tx.QueryRow(ctx, `
		UPDATE procurement.suppliers SET name = $2, account_reference = $3, active = $4, version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+supplierCols, s.ID, s.Name, s.AccountReference, s.Active))
	if pgCode(err) == "23505" {
		return application.Supplier{}, application.ErrConflict
	}
	if err != nil {
		return application.Supplier{}, fmt.Errorf("update supplier: %w", err)
	}
	return out, nil
}

func (r *Repository) GetSupplier(ctx context.Context, id string) (application.Supplier, error) {
	return get(ctx, r, "procurement.suppliers", supplierCols, id, scanSupplier)
}

func (r *Repository) ActiveSuppliersTx(ctx context.Context, tx pgx.Tx, ids []string) (map[string]bool, error) {
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
	rows, err := tx.Query(ctx, `SELECT id::text FROM procurement.suppliers WHERE id = ANY($1::text[]::uuid[]) AND active`, valid)
	if err != nil {
		return nil, fmt.Errorf("active suppliers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("active suppliers: scan: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (r *Repository) ListSuppliers(ctx context.Context, prefix string, includeInactive bool, page application.Page) (application.Result[application.Supplier], error) {
	var conds []string
	var args []any
	if !includeInactive {
		conds = append(conds, "active")
	}
	if prefix != "" {
		args = append(args, prefixPattern(strings.ToLower(prefix)))
		conds = append(conds, fmt.Sprintf("lower(name) LIKE $%d", len(args)))
	}
	return listPage(ctx, r, "procurement.suppliers", supplierCols, false, conds, args, page, scanSupplier, func(s application.Supplier) string { return s.ID })
}

// ---- needs ----

const needCols = `id::text, reference, product_id::text, quantity, status, status_reason, context_type, context_id::text, notes, requested_by::text, version, created_at, updated_at`

func scanNeed(row pgx.Row) (application.Need, error) {
	var n application.Need
	err := row.Scan(&n.ID, &n.Reference, &n.ProductID, &n.Quantity, &n.Status, &n.StatusReason, &n.ContextType, &n.ContextID, &n.Notes, &n.RequestedBy,
		&n.Version, &n.CreatedAt, &n.UpdatedAt)
	return n, err
}

func (r *Repository) InsertNeedTx(ctx context.Context, tx pgx.Tx, n application.Need) (application.Need, error) {
	out, err := scanNeed(tx.QueryRow(ctx, `
		INSERT INTO procurement.procurement_requests(product_id, quantity, context_type, context_id, notes, requested_by)
		VALUES ($1::uuid, $2, $3, $4::uuid, $5, $6::uuid) RETURNING `+needCols,
		n.ProductID, n.Quantity, n.ContextType, n.ContextID, n.Notes, n.RequestedBy))
	if err != nil {
		return application.Need{}, fmt.Errorf("insert procurement request: %w", err)
	}
	return out, nil
}

func (r *Repository) LockNeedTx(ctx context.Context, tx pgx.Tx, id string) (application.Need, error) {
	return lock(ctx, tx, "procurement.procurement_requests", needCols, id, scanNeed)
}

func (r *Repository) UpdateNeedTx(ctx context.Context, tx pgx.Tx, n application.Need) (application.Need, error) {
	out, err := scanNeed(tx.QueryRow(ctx, `
		UPDATE procurement.procurement_requests SET status = $2, status_reason = $3, version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+needCols, n.ID, n.Status, n.StatusReason))
	if err != nil {
		return application.Need{}, fmt.Errorf("update procurement request: %w", err)
	}
	return out, nil
}

func (r *Repository) GetNeed(ctx context.Context, id string) (application.Need, error) {
	return get(ctx, r, "procurement.procurement_requests", needCols, id, scanNeed)
}

func (r *Repository) ListNeeds(ctx context.Context, f application.NeedFilter) (application.Result[application.Need], error) {
	var conds []string
	var args []any
	empty := application.Result[application.Need]{Items: []application.Need{}}
	for _, c := range []struct {
		value, cond string
		uuid        bool
	}{
		{f.Status, "status = $%d", false}, {f.ProductID, "product_id = $%d::uuid", true},
		{f.ContextType, "context_type = $%d", false}, {f.ContextID, "context_id = $%d::uuid", true},
	} {
		if c.value == "" {
			continue
		}
		if c.uuid && !validUUID(c.value) {
			return empty, nil
		}
		args = append(args, c.value)
		conds = append(conds, fmt.Sprintf(c.cond, len(args)))
	}
	return listPage(ctx, r, "procurement.procurement_requests", needCols, true, conds, args, f.Page, scanNeed, func(n application.Need) string { return n.ID })
}

// ---- orders ----

const orderCols = `id::text, reference, supplier_id::text, status, status_reason, currency, notes, created_by::text, sent_at, closed_at, version, created_at, updated_at`

func scanOrder(row pgx.Row) (application.Order, error) {
	var o application.Order
	err := row.Scan(&o.ID, &o.Reference, &o.SupplierID, &o.Status, &o.StatusReason, &o.Currency, &o.Notes, &o.CreatedBy, &o.SentAt, &o.ClosedAt,
		&o.Version, &o.CreatedAt, &o.UpdatedAt)
	return o, err
}

func (r *Repository) InsertOrderTx(ctx context.Context, tx pgx.Tx, supplierID, currency string, notes, createdBy *string) (application.Order, error) {
	o, err := scanOrder(tx.QueryRow(ctx, `
		INSERT INTO procurement.purchase_orders(supplier_id, currency, notes, created_by) VALUES ($1::uuid, $2, $3, $4::uuid) RETURNING `+orderCols,
		supplierID, currency, notes, createdBy))
	if err != nil {
		return application.Order{}, fmt.Errorf("insert purchase order: %w", err)
	}
	return o, nil
}

func (r *Repository) LockOrderTx(ctx context.Context, tx pgx.Tx, id string) (application.Order, error) {
	return lock(ctx, tx, "procurement.purchase_orders", orderCols, id, scanOrder)
}

func (r *Repository) UpdateOrderTx(ctx context.Context, tx pgx.Tx, o application.Order) (application.Order, error) {
	out, err := scanOrder(tx.QueryRow(ctx, `
		UPDATE procurement.purchase_orders SET supplier_id = $2::uuid, status = $3, status_reason = $4, currency = $5, notes = $6,
			sent_at = $7, closed_at = $8, version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+orderCols, o.ID, o.SupplierID, o.Status, o.StatusReason, o.Currency, o.Notes, o.SentAt, o.ClosedAt))
	if err != nil {
		return application.Order{}, fmt.Errorf("update purchase order: %w", err)
	}
	return out, nil
}

func (r *Repository) GetOrder(ctx context.Context, id string) (application.Order, error) {
	return get(ctx, r, "procurement.purchase_orders", orderCols, id, scanOrder)
}

func (r *Repository) ListOrders(ctx context.Context, f application.OrderFilter) (application.Result[application.Order], error) {
	var conds []string
	var args []any
	if f.Status != "" {
		args = append(args, f.Status)
		conds = append(conds, fmt.Sprintf("status = $%d", len(args)))
	}
	if f.SupplierID != "" {
		if !validUUID(f.SupplierID) {
			return application.Result[application.Order]{Items: []application.Order{}}, nil
		}
		args = append(args, f.SupplierID)
		conds = append(conds, fmt.Sprintf("supplier_id = $%d::uuid", len(args)))
	}
	return listPage(ctx, r, "procurement.purchase_orders", orderCols, true, conds, args, f.Page, scanOrder, func(o application.Order) string { return o.ID })
}

// ---- lines ----

const lineCols = `id::text, order_id::text, line_no, product_id::text, quantity, unit_price_cents, received_quantity, request_id::text, created_at`

func scanLine(row pgx.Row) (application.Line, error) {
	var l application.Line
	err := row.Scan(&l.ID, &l.OrderID, &l.LineNo, &l.ProductID, &l.Quantity, &l.UnitPriceCents, &l.ReceivedQuantity, &l.RequestID, &l.CreatedAt)
	return l, err
}

func collectLines(rows pgx.Rows) ([]application.Line, error) {
	defer rows.Close()
	out := []application.Line{}
	for rows.Next() {
		l, err := scanLine(rows)
		if err != nil {
			return nil, fmt.Errorf("scan order line: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (r *Repository) LinesTx(ctx context.Context, tx pgx.Tx, orderID string, lockRows bool) ([]application.Line, error) {
	sql := `SELECT ` + lineCols + ` FROM procurement.purchase_order_lines WHERE order_id = $1::uuid ORDER BY line_no`
	if lockRows {
		sql += ` FOR UPDATE`
	}
	rows, err := tx.Query(ctx, sql, orderID)
	if err != nil {
		return nil, fmt.Errorf("list order lines: %w", err)
	}
	return collectLines(rows)
}

func (r *Repository) Lines(ctx context.Context, orderID string) ([]application.Line, error) {
	if !validUUID(orderID) {
		return []application.Line{}, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+lineCols+` FROM procurement.purchase_order_lines WHERE order_id = $1::uuid ORDER BY line_no`, orderID)
	if err != nil {
		return nil, fmt.Errorf("list order lines: %w", err)
	}
	return collectLines(rows)
}

func (r *Repository) InsertLineTx(ctx context.Context, tx pgx.Tx, l application.Line) (application.Line, error) {
	out, err := scanLine(tx.QueryRow(ctx, `
		INSERT INTO procurement.purchase_order_lines(order_id, line_no, product_id, quantity, unit_price_cents, request_id)
		VALUES ($1::uuid, $2, $3::uuid, $4, $5, $6::uuid) RETURNING `+lineCols,
		l.OrderID, l.LineNo, l.ProductID, l.Quantity, l.UnitPriceCents, l.RequestID))
	if pgCode(err) == "23505" {
		return application.Line{}, application.ErrConflict
	}
	if err != nil {
		return application.Line{}, fmt.Errorf("insert order line: %w", err)
	}
	return out, nil
}

func (r *Repository) UpdateLineTx(ctx context.Context, tx pgx.Tx, l application.Line) (application.Line, error) {
	out, err := scanLine(tx.QueryRow(ctx, `
		UPDATE procurement.purchase_order_lines SET quantity = $2, unit_price_cents = $3 WHERE id = $1::uuid RETURNING `+lineCols,
		l.ID, l.Quantity, l.UnitPriceCents))
	if err != nil {
		return application.Line{}, fmt.Errorf("update order line: %w", err)
	}
	return out, nil
}

func (r *Repository) DeleteLineTx(ctx context.Context, tx pgx.Tx, orderID, lineID string) (bool, error) {
	tag, err := tx.Exec(ctx, `DELETE FROM procurement.purchase_order_lines WHERE id = $1::uuid AND order_id = $2::uuid`, lineID, orderID)
	if err != nil {
		return false, fmt.Errorf("delete order line: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (r *Repository) ReceiveLineTx(ctx context.Context, tx pgx.Tx, orderID, lineID string, qty int) (bool, error) {
	if !validUUID(lineID) {
		return false, nil
	}
	tag, err := tx.Exec(ctx, `
		UPDATE procurement.purchase_order_lines SET received_quantity = received_quantity + $3
		WHERE id = $1::uuid AND order_id = $2::uuid AND received_quantity + $3 <= quantity`, lineID, orderID, qty)
	if err != nil {
		return false, fmt.Errorf("receive order line: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (r *Repository) ClearLineRequestsTx(ctx context.Context, tx pgx.Tx, orderID string, onlyUnfulfilled bool) ([]string, error) {
	cond := ""
	if onlyUnfulfilled {
		cond = " AND received_quantity < quantity"
	}
	rows, err := tx.Query(ctx, `
		WITH old AS (
			SELECT id, request_id FROM procurement.purchase_order_lines
			WHERE order_id = $1::uuid AND request_id IS NOT NULL`+cond+` FOR UPDATE
		), cleared AS (
			UPDATE procurement.purchase_order_lines l SET request_id = NULL FROM old WHERE l.id = old.id RETURNING old.request_id
		)
		SELECT request_id::text FROM cleared`, orderID)
	if err != nil {
		return nil, fmt.Errorf("clear line requests: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id *string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("clear line requests: scan: %w", err)
		}
		if id != nil {
			ids = append(ids, *id)
		}
	}
	return ids, rows.Err()
}
