package main

import (
	"context"
	"errors"
	"testing"

	assetsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/application"
	inventoryapp "github.com/MagicalWig34653/turaco/backend/internal/modules/inventory/application"
	procurementapp "github.com/MagicalWig34653/turaco/backend/internal/modules/procurement/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

// An approved purchase order is delivered in two receipts: quantity products
// become ledger-backed stock at a storage location, serialized products become
// assets, the order tracks the received quantities, and a refused receipt
// books nothing in any module.
func TestGoodsReceiptBooksOrderStockAndAssetsAtomically(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	var cable, notebook, wh, shelf string
	q := func(dst *string, sql string, args ...any) {
		if err := w.pool.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	q(&cable, `INSERT INTO products.products(name, serialized, stock_managed, asset_managed) VALUES ($1, false, true, false) RETURNING id::text`, w.corr+" cable")
	q(&notebook, `INSERT INTO products.products(name, serialized, stock_managed, asset_managed) VALUES ($1, true, false, true) RETURNING id::text`, w.corr+" notebook")
	q(&wh, `INSERT INTO inventory.warehouses(name) VALUES ($1) RETURNING id::text`, w.corr+" warehouse")
	q(&shelf, `INSERT INTO inventory.storage_locations(warehouse_id, name) VALUES ($1::uuid, 'A1') RETURNING id::text`, wh)
	var supplier string
	t.Cleanup(func() {
		conn, err := w.pool.Acquire(ctx)
		if err == nil {
			_, _ = conn.Exec(ctx, `SET session_replication_role = replica`)
			_, _ = conn.Exec(ctx, `DELETE FROM inventory.goods_receipt_assets WHERE asset_id IN (SELECT id FROM assets.assets WHERE product_id = $1::uuid)`, notebook)
			_, _ = conn.Exec(ctx, `DELETE FROM inventory.goods_receipt_lines WHERE product_id = ANY($1::uuid[])`, []string{cable, notebook})
			_, _ = conn.Exec(ctx, `DELETE FROM inventory.goods_receipts WHERE order_id IN (SELECT id FROM procurement.purchase_orders WHERE supplier_id = $1::uuid)`, supplier)
			_, _ = conn.Exec(ctx, `DELETE FROM inventory.inventory_transactions WHERE product_id = ANY($1::uuid[])`, []string{cable, notebook})
			conn.Release()
		}
		_, _ = w.pool.Exec(ctx, `DELETE FROM inventory.stock_balances WHERE product_id = $1::uuid`, cable)
		_, _ = w.pool.Exec(ctx, `DELETE FROM assets.assets WHERE product_id = $1::uuid`, notebook)
		_, _ = w.pool.Exec(ctx, `DELETE FROM approvals.approvals WHERE subject_type = 'purchase_order' AND subject_id IN (SELECT id FROM procurement.purchase_orders WHERE supplier_id = $1::uuid)`, supplier)
		_, _ = w.pool.Exec(ctx, `DELETE FROM procurement.purchase_orders WHERE supplier_id = $1::uuid`, supplier)
		_, _ = w.pool.Exec(ctx, `DELETE FROM procurement.suppliers WHERE id = $1::uuid`, supplier)
		_, _ = w.pool.Exec(ctx, `DELETE FROM inventory.storage_locations WHERE id = $1::uuid`, shelf)
		_, _ = w.pool.Exec(ctx, `DELETE FROM inventory.warehouses WHERE id = $1::uuid`, wh)
		_, _ = w.pool.Exec(ctx, `DELETE FROM products.products WHERE id = ANY($1::uuid[])`, []string{cable, notebook})
	})

	proc := wiring.Procurement(w.pool)
	inv := wiring.Inventory(w.pool)
	assetSvc := wiring.Assets(w.pool)
	pm := procurementapp.Principal{UserID: w.creator, Manage: true}
	im := inventoryapp.Principal{UserID: w.creator, Manage: true}
	pc := procurementapp.Caller{Actor: audit.UserActor(w.creator), CorrelationID: w.corr}
	ic := inventoryapp.Caller{Actor: audit.UserActor(w.creator), CorrelationID: w.corr}

	s, err := proc.CreateSupplier(ctx, pc, pm, w.corr+" Supplies", "")
	if err != nil {
		t.Fatal(err)
	}
	supplier = s.ID
	order, err := proc.CreateOrder(ctx, pc, pm, procurementapp.NewOrder{SupplierID: supplier})
	if err != nil {
		t.Fatal(err)
	}
	cableLine, err := proc.AddLine(ctx, pc, pm, order.ID, nil, procurementapp.NewLine{ProductID: cable, Quantity: 10, UnitPriceCents: 200})
	if err != nil {
		t.Fatal(err)
	}
	bookLine, err := proc.AddLine(ctx, pc, pm, order.ID, nil, procurementapp.NewLine{ProductID: notebook, Quantity: 2, UnitPriceCents: 90000})
	if err != nil {
		t.Fatal(err)
	}
	receiptFor := func(cableQty int, units ...inventoryapp.ReceivedUnit) inventoryapp.ReceiptInput {
		in := inventoryapp.ReceiptInput{OrderID: order.ID, DeliveryNote: "LS-1", Lines: []inventoryapp.ReceiptLineInput{
			{OrderLineID: cableLine.ID, Quantity: cableQty, StorageLocationID: shelf},
		}}
		if len(units) > 0 {
			in.Lines = append(in.Lines, inventoryapp.ReceiptLineInput{OrderLineID: bookLine.ID, Quantity: len(units), Units: units})
		}
		return in
	}
	if _, err := inv.PostGoodsReceipt(ctx, ic, im, receiptFor(1)); !errors.Is(err, inventoryapp.ErrOrderNotReceivable) {
		t.Errorf("receiving before the order was sent: %v", err)
	}
	// Approve and send the order.
	if _, err := proc.Submit(ctx, pc, pm, order.ID, nil, procurementapp.Approver{UserID: &w.assignee}); err != nil {
		t.Fatal(err)
	}
	var approvalID string
	q(&approvalID, `SELECT id::text FROM approvals.approvals WHERE subject_type = 'purchase_order' AND subject_id = $1::uuid`, order.ID)
	if err := decideApproval(w, w.assignee, approvalID, "approve"); err != nil {
		t.Fatal(err)
	}
	w.dispatch()
	if _, err := proc.Send(ctx, pc, pm, order.ID, nil); err != nil {
		t.Fatal(err)
	}

	// A receipt that fails on the second line (serialized product without serial numbers) books nothing.
	bad := receiptFor(4)
	bad.Lines = append(bad.Lines, inventoryapp.ReceiptLineInput{OrderLineID: bookLine.ID, Quantity: 1, Units: []inventoryapp.ReceivedUnit{{SerialNumber: ""}}})
	if _, err := inv.PostGoodsReceipt(ctx, ic, im, bad); err == nil {
		t.Fatal("a unit without a serial number was accepted")
	}
	if _, err := inv.PostGoodsReceipt(ctx, ic, im, receiptFor(11)); !errors.Is(err, inventoryapp.ErrOverReceipt) {
		t.Errorf("over-delivery: %v", err)
	}
	if _, err := inv.PostGoodsReceipt(ctx, ic, im, receiptFor(4, inventoryapp.ReceivedUnit{SerialNumber: w.corr + "-A"}, inventoryapp.ReceivedUnit{SerialNumber: w.corr + "-A"})); !errors.Is(err, inventoryapp.ErrDuplicateAsset) {
		t.Errorf("duplicate serial numbers in one delivery: %v", err)
	}
	var stock, received, assetCount int
	count := func() {
		_ = w.pool.QueryRow(ctx, `SELECT coalesce(sum(on_hand), 0) FROM inventory.stock_balances WHERE product_id = $1::uuid`, cable).Scan(&stock)
		_ = w.pool.QueryRow(ctx, `SELECT coalesce(sum(received_quantity), 0) FROM procurement.purchase_order_lines WHERE order_id = $1::uuid`, order.ID).Scan(&received)
		_ = w.pool.QueryRow(ctx, `SELECT count(*) FROM assets.assets WHERE product_id = $1::uuid`, notebook).Scan(&assetCount)
	}
	count()
	if stock != 0 || received != 0 || assetCount != 0 {
		t.Fatalf("refused receipts booked stock=%d received=%d assets=%d", stock, received, assetCount)
	}

	// First delivery: 4 cables and 1 notebook.
	gr, err := inv.PostGoodsReceipt(ctx, ic, im, receiptFor(4, inventoryapp.ReceivedUnit{SerialNumber: w.corr + "-A", AssetTag: w.corr + "-T1"}))
	if err != nil || len(gr.Lines) != 2 || gr.Reference == "" {
		t.Fatalf("receipt = %+v %v", gr, err)
	}
	count()
	if stock != 4 || received != 5 || assetCount != 1 {
		t.Errorf("after the first delivery: stock=%d received=%d assets=%d", stock, received, assetCount)
	}
	detail, err := proc.GetOrder(ctx, procurementapp.Principal{View: true}, order.ID)
	if err != nil || detail.Order.Status != "partially_received" {
		t.Fatalf("order = %+v %v", detail.Order, err)
	}
	if len(gr.Lines[1].AssetIDs) != 1 {
		t.Fatalf("asset ids = %v", gr.Lines[1].AssetIDs)
	}
	a, err := assetSvc.Get(ctx, assetsapp.Principal{View: true}, gr.Lines[1].AssetIDs[0])
	if err != nil || a.Asset.Status != "received" || a.Asset.SupplierID == nil || *a.Asset.SupplierID != supplier || a.Asset.SourceID == nil || *a.Asset.SourceID != gr.ID {
		t.Fatalf("asset = %+v %v", a.Asset, err)
	}

	// Final delivery completes the order; assets can be registered as available right away.
	final := receiptFor(6, inventoryapp.ReceivedUnit{SerialNumber: w.corr + "-B"})
	final.AssetsAvailable = true
	gr2, err := inv.PostGoodsReceipt(ctx, ic, im, final)
	if err != nil {
		t.Fatal(err)
	}
	count()
	if stock != 10 || received != 12 || assetCount != 2 {
		t.Errorf("after the final delivery: stock=%d received=%d assets=%d", stock, received, assetCount)
	}
	if a, _ := assetSvc.Get(ctx, assetsapp.Principal{View: true}, gr2.Lines[1].AssetIDs[0]); a.Asset.Status != "available" {
		t.Errorf("asset = %s, want available", a.Asset.Status)
	}
	if d, _ := proc.GetOrder(ctx, procurementapp.Principal{View: true}, order.ID); d.Order.Status != "received" {
		t.Errorf("order = %s, want received", d.Order.Status)
	}
	if _, err := inv.PostGoodsReceipt(ctx, ic, im, receiptFor(1)); !errors.Is(err, inventoryapp.ErrOrderNotReceivable) {
		t.Errorf("receiving on a completed order: %v", err)
	}

	// The receipt is immutable and listed.
	if _, err := w.pool.Exec(ctx, `UPDATE inventory.goods_receipts SET delivery_note = 'changed' WHERE id = $1::uuid`, gr.ID); err == nil {
		t.Error("a posted receipt was changed")
	}
	list, err := inv.ListGoodsReceipts(ctx, inventoryapp.Principal{View: true}, order.ID, inventoryapp.Page{})
	if err != nil || len(list.Items) != 2 || len(list.Items[0].Lines) != 2 {
		t.Errorf("receipts = %+v %v", list, err)
	}
	var onHand, reserved, ledger int
	_ = w.pool.QueryRow(ctx, `SELECT coalesce(sum(on_hand_delta), 0), coalesce(sum(reserved_delta), 0), count(*) FROM inventory.inventory_transactions WHERE product_id = $1::uuid AND type = 'goods_receipt'`, cable).Scan(&onHand, &reserved, &ledger)
	if onHand != 10 || reserved != 0 || ledger != 2 {
		t.Errorf("ledger: on hand %d, reserved %d, rows %d", onHand, reserved, ledger)
	}
}
