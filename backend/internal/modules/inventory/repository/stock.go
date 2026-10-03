package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/inventory/application"
)

// LockBalancesTx locks the existing balance rows of a product at the given
// locations in id order, so concurrent multi-row operations cannot deadlock.
func (r *Repository) LockBalancesTx(ctx context.Context, tx pgx.Tx, productID string, locationIDs []string) error {
	if _, err := tx.Exec(ctx, `
		SELECT 1 FROM inventory.stock_balances
		WHERE product_id = $1::uuid AND storage_location_id = ANY($2::text[]::uuid[])
		ORDER BY storage_location_id FOR UPDATE`, productID, locationIDs); err != nil {
		return fmt.Errorf("lock stock balances: %w", err)
	}
	return nil
}

func (r *Repository) AddStockTx(ctx context.Context, tx pgx.Tx, productID, locationID string, qty int) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO inventory.stock_balances(product_id, storage_location_id, on_hand) VALUES ($1::uuid, $2::uuid, $3)
		ON CONFLICT (product_id, storage_location_id)
		DO UPDATE SET on_hand = inventory.stock_balances.on_hand + EXCLUDED.on_hand, updated_at = now()`,
		productID, locationID, qty)
	if err != nil {
		return fmt.Errorf("add stock: %w", err)
	}
	return nil
}

// guarded runs an UPDATE that only applies while its condition holds and reports whether it did.
func guarded(ctx context.Context, tx pgx.Tx, what, set, cond string, productID, locationID string, qty int) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE inventory.stock_balances SET `+set+`, updated_at = now()
		WHERE product_id = $1::uuid AND storage_location_id = $2::uuid AND `+cond, productID, locationID, qty)
	if err != nil {
		return false, fmt.Errorf("%s: %w", what, err)
	}
	return tag.RowsAffected() == 1, nil
}

func (r *Repository) RemoveStockTx(ctx context.Context, tx pgx.Tx, productID, locationID string, qty int) (bool, error) {
	return guarded(ctx, tx, "remove stock", "on_hand = on_hand - $3", "on_hand - reserved >= $3", productID, locationID, qty)
}

func (r *Repository) ReserveStockTx(ctx context.Context, tx pgx.Tx, productID, locationID string, qty int) (bool, error) {
	return guarded(ctx, tx, "reserve stock", "reserved = reserved + $3", "on_hand - reserved >= $3", productID, locationID, qty)
}

func (r *Repository) UnreserveStockTx(ctx context.Context, tx pgx.Tx, productID, locationID string, qty int) (bool, error) {
	return guarded(ctx, tx, "unreserve stock", "reserved = reserved - $3", "reserved >= $3", productID, locationID, qty)
}

func (r *Repository) IssueReservedStockTx(ctx context.Context, tx pgx.Tx, productID, locationID string, qty int) (bool, error) {
	return guarded(ctx, tx, "issue reserved stock", "on_hand = on_hand - $3, reserved = reserved - $3", "reserved >= $3", productID, locationID, qty)
}

func (r *Repository) CorrectStockTx(ctx context.Context, tx pgx.Tx, productID, locationID string, delta int) (bool, error) {
	if delta > 0 {
		return true, r.AddStockTx(ctx, tx, productID, locationID, delta)
	}
	// A decrease may not take the balance below what is reserved.
	return guarded(ctx, tx, "correct stock", "on_hand = on_hand + $3", "on_hand + $3 >= reserved", productID, locationID, delta)
}

const txCols = `id::text, type, product_id::text, storage_location_id::text, on_hand_delta, reserved_delta, group_id::text,
	reservation_id::text, context_type, context_id::text, reason, actor_user_id::text, correlation_id, created_at`

func scanTx(row pgx.Row) (application.Transaction, error) {
	var t application.Transaction
	err := row.Scan(&t.ID, &t.Type, &t.ProductID, &t.StorageLocationID, &t.OnHandDelta, &t.ReservedDelta, &t.GroupID,
		&t.ReservationID, &t.ContextType, &t.ContextID, &t.Reason, &t.ActorUserID, &t.CorrelationID, &t.CreatedAt)
	return t, err
}

func (r *Repository) InsertTransactionTx(ctx context.Context, tx pgx.Tx, t application.Transaction) (application.Transaction, error) {
	var group any
	if t.GroupID != "" {
		group = t.GroupID
	}
	actorSystem := (*string)(nil)
	if t.ActorUserID == nil {
		s := "system"
		actorSystem = &s
	}
	out, err := scanTx(tx.QueryRow(ctx, `
		INSERT INTO inventory.inventory_transactions (type, product_id, storage_location_id, on_hand_delta, reserved_delta, group_id,
			reservation_id, context_type, context_id, reason, actor_user_id, actor_system, correlation_id)
		VALUES ($1, $2::uuid, $3::uuid, $4, $5, coalesce($6::uuid, uuidv7()), $7::uuid, $8, $9::uuid, $10, $11::uuid, $12, $13)
		RETURNING `+txCols,
		t.Type, t.ProductID, t.StorageLocationID, t.OnHandDelta, t.ReservedDelta, group,
		t.ReservationID, t.ContextType, t.ContextID, t.Reason, t.ActorUserID, actorSystem, t.CorrelationID))
	if err != nil {
		return application.Transaction{}, fmt.Errorf("insert inventory transaction: %w", err)
	}
	return out, nil
}

func (r *Repository) ListStock(ctx context.Context, f application.StockFilter) (application.Result[application.Balance], error) {
	page := f.Page.Normalize()
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	for _, c := range []struct {
		value, cond string
	}{
		{f.ProductID, "b.product_id = $%d::uuid"}, {f.WarehouseID, "l.warehouse_id = $%d::uuid"}, {f.StorageLocationID, "b.storage_location_id = $%d::uuid"},
	} {
		if c.value == "" {
			continue
		}
		if !validUUID(c.value) {
			return application.Result[application.Balance]{Items: []application.Balance{}}, nil
		}
		add(c.cond, c.value)
	}
	if f.WithStockOnly {
		conds = append(conds, "b.on_hand > 0")
	}
	if page.Cursor != "" {
		// The cursor is "<product id>:<location id>" over the primary key.
		pid, lid, ok := splitCursor(page.Cursor)
		if !ok {
			return application.Result[application.Balance]{}, application.ErrInvalidCursor
		}
		args = append(args, pid, lid)
		conds = append(conds, fmt.Sprintf("(b.product_id, b.storage_location_id) > ($%d::uuid, $%d::uuid)", len(args)-1, len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + joinAnd(conds)
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`
		SELECT b.product_id::text, b.storage_location_id::text, l.warehouse_id::text, b.on_hand, b.reserved, b.updated_at
		FROM inventory.stock_balances b JOIN inventory.storage_locations l ON l.id = b.storage_location_id%s
		ORDER BY b.product_id, b.storage_location_id LIMIT $%d`, where, len(args)), args...)
	if err != nil {
		return application.Result[application.Balance]{}, fmt.Errorf("list stock: %w", err)
	}
	defer rows.Close()
	items := make([]application.Balance, 0, page.Limit+1)
	for rows.Next() {
		var b application.Balance
		if err := rows.Scan(&b.ProductID, &b.StorageLocationID, &b.WarehouseID, &b.OnHand, &b.Reserved, &b.UpdatedAt); err != nil {
			return application.Result[application.Balance]{}, fmt.Errorf("list stock: scan: %w", err)
		}
		items = append(items, b)
	}
	if err := rows.Err(); err != nil {
		return application.Result[application.Balance]{}, fmt.Errorf("list stock: %w", err)
	}
	res := application.Result[application.Balance]{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		last := res.Items[page.Limit-1]
		res.NextCursor = last.ProductID + ":" + last.StorageLocationID
	}
	return res, nil
}

func splitCursor(c string) (string, string, bool) {
	if len(c) != 73 || c[36] != ':' || !validUUID(c[:36]) || !validUUID(c[37:]) {
		return "", "", false
	}
	return c[:36], c[37:], true
}

func joinAnd(conds []string) string {
	out := conds[0]
	for _, c := range conds[1:] {
		out += " AND " + c
	}
	return out
}

func (r *Repository) ListTransactions(ctx context.Context, f application.TransactionFilter) (application.Result[application.Transaction], error) {
	var conds []string
	var args []any
	empty := application.Result[application.Transaction]{Items: []application.Transaction{}}
	for _, c := range []struct {
		value, cond string
		uuid        bool
	}{
		{f.ProductID, "product_id = $%d::uuid", true}, {f.StorageLocationID, "storage_location_id = $%d::uuid", true},
		{f.Type, "type = $%d", false}, {f.ContextType, "context_type = $%d", false}, {f.ContextID, "context_id = $%d::uuid", true},
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
	return listPageOrdered(ctx, r, "inventory.inventory_transactions", txCols, "id", true, conds, args, f.Page, scanTx, func(t application.Transaction) string { return t.ID })
}

// ---- reservations ----

const resCols = `id::text, kind, product_id::text, storage_location_id::text, quantity, asset_id::text, status, context_type, context_id::text,
	reason, closed_at, created_by::text, version, created_at, updated_at`

func scanReservation(row pgx.Row) (application.Reservation, error) {
	var x application.Reservation
	err := row.Scan(&x.ID, &x.Kind, &x.ProductID, &x.StorageLocationID, &x.Quantity, &x.AssetID, &x.Status, &x.ContextType, &x.ContextID,
		&x.Reason, &x.ClosedAt, &x.CreatedBy, &x.Version, &x.CreatedAt, &x.UpdatedAt)
	return x, err
}

func (r *Repository) InsertReservationTx(ctx context.Context, tx pgx.Tx, n application.NewReservation) (application.Reservation, error) {
	out, err := scanReservation(tx.QueryRow(ctx, `
		INSERT INTO inventory.reservations(kind, product_id, storage_location_id, quantity, asset_id, context_type, context_id, created_by)
		VALUES ($1, $2::uuid, $3::uuid, $4, $5::uuid, $6, $7::uuid, $8::uuid) RETURNING `+resCols,
		n.Kind, n.ProductID, n.StorageLocationID, n.Quantity, n.AssetID, n.ContextType, n.ContextID, n.CreatedBy))
	if pgCode(err) == "23505" {
		return application.Reservation{}, application.ErrConflict
	}
	if err != nil {
		return application.Reservation{}, fmt.Errorf("insert reservation: %w", err)
	}
	return out, nil
}

func (r *Repository) LockReservationTx(ctx context.Context, tx pgx.Tx, id string) (application.Reservation, error) {
	if !validUUID(id) {
		return application.Reservation{}, application.ErrNotFound
	}
	out, err := scanReservation(tx.QueryRow(ctx, `SELECT `+resCols+` FROM inventory.reservations WHERE id = $1::uuid FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Reservation{}, application.ErrNotFound
	}
	if err != nil {
		return application.Reservation{}, fmt.Errorf("lock reservation: %w", err)
	}
	return out, nil
}

func (r *Repository) CloseReservationTx(ctx context.Context, tx pgx.Tx, id, status string, reason *string) (application.Reservation, error) {
	out, err := scanReservation(tx.QueryRow(ctx, `
		UPDATE inventory.reservations SET status = $2, reason = $3, closed_at = now(), version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+resCols, id, status, reason))
	if err != nil {
		return application.Reservation{}, fmt.Errorf("close reservation: %w", err)
	}
	return out, nil
}

func (r *Repository) GetReservation(ctx context.Context, id string) (application.Reservation, error) {
	if !validUUID(id) {
		return application.Reservation{}, application.ErrNotFound
	}
	out, err := scanReservation(r.pool.QueryRow(ctx, `SELECT `+resCols+` FROM inventory.reservations WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Reservation{}, application.ErrNotFound
	}
	if err != nil {
		return application.Reservation{}, fmt.Errorf("get reservation: %w", err)
	}
	return out, nil
}

func (r *Repository) ListReservations(ctx context.Context, f application.ReservationFilter) (application.Result[application.Reservation], error) {
	var conds []string
	var args []any
	empty := application.Result[application.Reservation]{Items: []application.Reservation{}}
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
	return listPageOrdered(ctx, r, "inventory.reservations", resCols, "id", true, conds, args, f.Page, scanReservation, func(x application.Reservation) string { return x.ID })
}
