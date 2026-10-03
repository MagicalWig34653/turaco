package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/inventory/application"
)

const receiptCols = `id::text, reference, order_id::text, supplier_id::text, delivery_note, received_by::text, created_at`

func scanReceipt(row pgx.Row) (application.GoodsReceipt, error) {
	var g application.GoodsReceipt
	err := row.Scan(&g.ID, &g.Reference, &g.OrderID, &g.SupplierID, &g.DeliveryNote, &g.ReceivedBy, &g.CreatedAt)
	return g, err
}

func (r *Repository) InsertGoodsReceiptTx(ctx context.Context, tx pgx.Tx, orderID, supplierID string, deliveryNote, receivedBy *string) (application.GoodsReceipt, error) {
	g, err := scanReceipt(tx.QueryRow(ctx, `
		INSERT INTO inventory.goods_receipts(order_id, supplier_id, delivery_note, received_by) VALUES ($1::uuid, $2::uuid, $3, $4::uuid)
		RETURNING `+receiptCols, orderID, supplierID, deliveryNote, receivedBy))
	if err != nil {
		return application.GoodsReceipt{}, fmt.Errorf("insert goods receipt: %w", err)
	}
	return g, nil
}

func (r *Repository) InsertGoodsReceiptLineTx(ctx context.Context, tx pgx.Tx, receiptID string, l application.GoodsReceiptLine) (application.GoodsReceiptLine, error) {
	out := l
	err := tx.QueryRow(ctx, `
		INSERT INTO inventory.goods_receipt_lines(receipt_id, order_line_id, product_id, quantity, storage_location_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::uuid) RETURNING id::text`,
		receiptID, l.OrderLineID, l.ProductID, l.Quantity, l.StorageLocationID).Scan(&out.ID)
	if err != nil {
		return application.GoodsReceiptLine{}, fmt.Errorf("insert goods receipt line: %w", err)
	}
	return out, nil
}

func (r *Repository) AddReceiptAssetTx(ctx context.Context, tx pgx.Tx, receiptLineID, assetID string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO inventory.goods_receipt_assets(receipt_line_id, asset_id) VALUES ($1::uuid, $2::uuid)`, receiptLineID, assetID); err != nil {
		return fmt.Errorf("insert goods receipt asset: %w", err)
	}
	return nil
}

func (r *Repository) receiptLines(ctx context.Context, ids []string) (map[string][]application.GoodsReceiptLine, error) {
	out := map[string][]application.GoodsReceiptLine{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT l.receipt_id::text, l.id::text, l.order_line_id::text, l.product_id::text, l.quantity, l.storage_location_id::text,
		       coalesce((SELECT array_agg(a.asset_id::text ORDER BY a.asset_id) FROM inventory.goods_receipt_assets a WHERE a.receipt_line_id = l.id), '{}')
		FROM inventory.goods_receipt_lines l WHERE l.receipt_id = ANY($1::text[]::uuid[]) ORDER BY l.id`, ids)
	if err != nil {
		return nil, fmt.Errorf("list goods receipt lines: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var receipt string
		var l application.GoodsReceiptLine
		if err := rows.Scan(&receipt, &l.ID, &l.OrderLineID, &l.ProductID, &l.Quantity, &l.StorageLocationID, &l.AssetIDs); err != nil {
			return nil, fmt.Errorf("list goods receipt lines: scan: %w", err)
		}
		out[receipt] = append(out[receipt], l)
	}
	return out, rows.Err()
}

func (r *Repository) GetGoodsReceipt(ctx context.Context, id string) (application.GoodsReceipt, error) {
	if !validUUID(id) {
		return application.GoodsReceipt{}, application.ErrNotFound
	}
	g, err := scanReceipt(r.pool.QueryRow(ctx, `SELECT `+receiptCols+` FROM inventory.goods_receipts WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.GoodsReceipt{}, application.ErrNotFound
	}
	if err != nil {
		return application.GoodsReceipt{}, fmt.Errorf("get goods receipt: %w", err)
	}
	lines, err := r.receiptLines(ctx, []string{id})
	if err != nil {
		return application.GoodsReceipt{}, err
	}
	g.Lines = lines[id]
	return g, nil
}

func (r *Repository) ListGoodsReceipts(ctx context.Context, orderID string, page application.Page) (application.Result[application.GoodsReceipt], error) {
	var conds []string
	var args []any
	if orderID != "" {
		if !validUUID(orderID) {
			return application.Result[application.GoodsReceipt]{Items: []application.GoodsReceipt{}}, nil
		}
		args = append(args, orderID)
		conds = append(conds, "order_id = $1::uuid")
	}
	res, err := listPageOrdered(ctx, r, "inventory.goods_receipts", receiptCols, "id", true, conds, args, page, scanReceipt, func(g application.GoodsReceipt) string { return g.ID })
	if err != nil {
		return res, err
	}
	ids := make([]string, 0, len(res.Items))
	for _, g := range res.Items {
		ids = append(ids, g.ID)
	}
	lines, err := r.receiptLines(ctx, ids)
	if err != nil {
		return application.Result[application.GoodsReceipt]{}, err
	}
	for i := range res.Items {
		res.Items[i].Lines = lines[res.Items[i].ID]
	}
	return res, nil
}
