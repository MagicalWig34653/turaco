package main

import (
	"context"
	"testing"

	procurementapp "github.com/MagicalWig34653/turaco/backend/internal/modules/procurement/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

// A purchase order goes through the real Approvals module: submitting creates
// an approval for the chosen approver, the decision is turned into the order's
// status by the outbox consumer, and the creator cannot approve their own order.
func TestPurchaseOrderApprovalRoundTrip(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	var product, supplier string
	if err := w.pool.QueryRow(ctx, `INSERT INTO products.products(name) VALUES ($1) RETURNING id::text`, w.corr+" cable").Scan(&product); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = w.pool.Exec(ctx, `DELETE FROM approvals.approvals WHERE subject_type = 'purchase_order' AND subject_id IN (SELECT id FROM procurement.purchase_orders WHERE supplier_id = $1::uuid)`, supplier)
		_, _ = w.pool.Exec(ctx, `DELETE FROM procurement.purchase_orders WHERE supplier_id = $1::uuid`, supplier)
		_, _ = w.pool.Exec(ctx, `DELETE FROM procurement.suppliers WHERE id = $1::uuid`, supplier)
		_, _ = w.pool.Exec(ctx, `DELETE FROM products.products WHERE id = $1::uuid`, product)
	})
	svc := wiring.Procurement(w.pool)
	manager := procurementapp.Principal{UserID: w.creator, Manage: true}
	c := procurementapp.Caller{Actor: audit.UserActor(w.creator), CorrelationID: w.corr}
	s, err := svc.CreateSupplier(ctx, c, manager, w.corr+" Supplies", "")
	if err != nil {
		t.Fatal(err)
	}
	supplier = s.ID
	order := func() procurementapp.Order {
		o, err := svc.CreateOrder(ctx, c, manager, procurementapp.NewOrder{SupplierID: supplier})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.AddLine(ctx, c, manager, o.ID, nil, procurementapp.NewLine{ProductID: product, Quantity: 3, UnitPriceCents: 500}); err != nil {
			t.Fatal(err)
		}
		return o
	}
	approvalOf := func(orderID string) string {
		var id string
		if err := w.pool.QueryRow(ctx, `SELECT id::text FROM approvals.approvals WHERE subject_type = 'purchase_order' AND subject_id = $1::uuid ORDER BY created_at DESC LIMIT 1`, orderID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	status := func(id string) string {
		d, err := svc.GetOrder(ctx, procurementapp.Principal{View: true}, id)
		if err != nil {
			t.Fatal(err)
		}
		return d.Order.Status
	}

	// The creator cannot name themselves as approver.
	o := order()
	if _, err := svc.Submit(ctx, c, manager, o.ID, nil, procurementapp.Approver{UserID: &w.creator}); err == nil {
		t.Fatal("the creator was accepted as the approver of their own order")
	}
	if status(o.ID) != "draft" {
		t.Error("a refused submission must leave the order in draft")
	}

	// Approved by the named approver.
	if _, err := svc.Submit(ctx, c, manager, o.ID, nil, procurementapp.Approver{UserID: &w.assignee}); err != nil {
		t.Fatal(err)
	}
	w.dispatch()
	if w.notified(w.assignee, "approval.requested") != 1 {
		t.Error("the approver must be notified")
	}
	if err := decideApproval(w, w.creator, approvalOf(o.ID), "approve"); err == nil {
		t.Error("the creator decided their own order")
	}
	if err := decideApproval(w, w.assignee, approvalOf(o.ID), "approve"); err != nil {
		t.Fatal(err)
	}
	w.dispatch()
	if status(o.ID) != "approved" {
		t.Errorf("status = %s, want approved", status(o.ID))
	}

	// Rejected: back to draft with the reason, correctable and resubmittable.
	r := order()
	if _, err := svc.Submit(ctx, c, manager, r.ID, nil, procurementapp.Approver{TeamID: &w.team}); err != nil {
		t.Fatal(err)
	}
	if err := decideApproval(w, w.member, approvalOf(r.ID), "reject"); err != nil {
		t.Fatal(err)
	}
	w.dispatch()
	d, err := svc.GetOrder(ctx, manager, r.ID)
	if err != nil || d.Order.Status != "draft" || d.Order.StatusReason == nil || *d.Order.StatusReason != procurementapp.RejectedReason {
		t.Fatalf("rejected order = %+v %v", d.Order, err)
	}
	if w.pendingEvents() != 0 {
		t.Errorf("%d events left unprocessed", w.pendingEvents())
	}
}
