package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

var systemActor = audit.SystemActor("procurement-workflow")

// RejectedReason is stored on an order returned to draft by a rejection.
const RejectedReason = "approval_rejected"

// OnApprovalDecided moves a Purchase Order whose approval was decided: an
// approval makes it `approved`, a rejection returns it to `draft` (the reason
// `approval_rejected` is stored; the approver's comment stays with the
// approval) so it can be corrected and submitted again. It runs in the
// dispatcher's claim transaction and is idempotent: events of other subjects
// and stale events (the order is no longer pending approval) change nothing.
func (s *Service) OnApprovalDecided(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		SubjectType string `json:"subjectType"`
		SubjectID   string `json:"subjectId"`
		Decision    string `json:"decision"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return events.Permanent(fmt.Errorf("decode ApprovalDecided payload: %w", err))
	}
	if p.SubjectType != SubjectType {
		return nil
	}
	if p.Decision != "approve" && p.Decision != "reject" {
		return events.Permanent(fmt.Errorf("approval decision %q of purchase order %s is unknown", p.Decision, p.SubjectID))
	}
	cur, err := s.store.LockOrderTx(ctx, tx, p.SubjectID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if cur.Status != POStatusPendingApproval {
		return nil
	}
	c := Caller{Actor: systemActor, CorrelationID: ev.CorrelationID}
	next := cur
	action, reason := "procurement.order.approved", ""
	if p.Decision == "approve" {
		next.Status, next.StatusReason = POStatusApproved, nil
	} else {
		next.Status, next.StatusReason = POStatusDraft, strPtr(RejectedReason)
		action, reason = "procurement.order.approval_rejected", RejectedReason
	}
	out, err := s.store.UpdateOrderTx(ctx, tx, next)
	if err != nil {
		return err
	}
	var meta map[string]any
	if reason != "" {
		meta = map[string]any{"reason": reason}
	}
	if err := recordAudit(ctx, tx, c, action, "purchase_order", cur.ID, orderState(&cur), orderState(&out), meta); err != nil {
		return err
	}
	if p.Decision == "approve" {
		return publish(ctx, tx, c, "PurchaseOrderApproved", map[string]any{"orderId": cur.ID, "supplierId": cur.SupplierID})
	}
	return nil
}

// ---- receipt tracking (the contract Goods Receipt uses) ----

// Receivable is what goods receipt needs to know about an order.
type Receivable struct {
	OrderID    string
	Reference  string
	SupplierID string
	Status     string
	Lines      []Line
}

// ReceiptLine is the quantity received for one order line.
type ReceiptLine struct {
	LineID   string
	Quantity int
}

// ReceivableOrder returns an order and its lines for goods receipt. It
// performs no permission check: the caller has authorized the receipt.
func (s *Service) ReceivableOrder(ctx context.Context, id string) (Receivable, error) {
	o, err := s.store.GetOrder(ctx, id)
	if err != nil {
		return Receivable{}, err
	}
	lines, err := s.store.Lines(ctx, id)
	if err != nil {
		return Receivable{}, err
	}
	return Receivable{OrderID: o.ID, Reference: o.Reference, SupplierID: o.SupplierID, Status: o.Status, Lines: lines}, nil
}

// RecordReceiptInTx books received quantities on the order's lines in the
// caller's transaction: it refuses over-delivery (ErrOverReceipt), moves the
// order to `partially_received` or `received`, fulfills the procurement
// requests of completed lines and emits PurchaseOrderReceived once everything
// arrived. It performs no permission check: the caller has authorized the
// receipt.
func (s *Service) RecordReceiptInTx(ctx context.Context, tx pgx.Tx, c Caller, orderID string, receipt []ReceiptLine) (Receivable, error) {
	if err := c.validate(); err != nil {
		return Receivable{}, err
	}
	if len(receipt) == 0 {
		return Receivable{}, invalid("a receipt needs at least one line")
	}
	cur, err := s.store.LockOrderTx(ctx, tx, orderID)
	if err != nil {
		return Receivable{}, err
	}
	if !slices.Contains([]string{POStatusSent, POStatusAcknowledged, POStatusPartiallyReceived}, cur.Status) {
		return Receivable{}, &InvalidTransitionError{Operation: "receive", From: cur.Status}
	}
	seen := map[string]bool{}
	for _, r := range receipt {
		if seen[r.LineID] {
			return Receivable{}, invalid("a line may appear only once per receipt")
		}
		seen[r.LineID] = true
		if err := checkQuantity(r.Quantity); err != nil {
			return Receivable{}, err
		}
	}
	for _, r := range receipt {
		ok, err := s.store.ReceiveLineTx(ctx, tx, orderID, r.LineID, r.Quantity)
		if err != nil {
			return Receivable{}, err
		}
		if !ok {
			return Receivable{}, ErrOverReceipt
		}
	}
	lines, err := s.store.LinesTx(ctx, tx, orderID, false)
	if err != nil {
		return Receivable{}, err
	}
	all := true
	var done []string
	for _, l := range lines {
		if l.Open() > 0 {
			all = false
		} else if l.RequestID != nil && seen[l.ID] {
			done = append(done, *l.RequestID)
		}
	}
	slices.Sort(done)
	for _, id := range done {
		need, err := s.store.LockNeedTx(ctx, tx, id)
		if err != nil {
			return Receivable{}, err
		}
		if need.Status != NeedOrdered {
			continue
		}
		next := need
		next.Status = NeedFulfilled
		out, err := s.store.UpdateNeedTx(ctx, tx, next)
		if err != nil {
			return Receivable{}, err
		}
		if err := recordAudit(ctx, tx, c, "procurement.request.fulfilled", "procurement_request", id, needState(&need), needState(&out), nil); err != nil {
			return Receivable{}, err
		}
	}
	next := cur
	next.Status = POStatusPartiallyReceived
	if all {
		next.Status = POStatusReceived
	}
	out, err := s.store.UpdateOrderTx(ctx, tx, next)
	if err != nil {
		return Receivable{}, err
	}
	meta := map[string]any{"lines": len(receipt)}
	if err := recordAudit(ctx, tx, c, "procurement.order.receive", "purchase_order", orderID, orderState(&cur), orderState(&out), meta); err != nil {
		return Receivable{}, err
	}
	if all {
		if err := publish(ctx, tx, c, "PurchaseOrderReceived", map[string]any{"orderId": orderID, "supplierId": out.SupplierID}); err != nil {
			return Receivable{}, err
		}
	}
	return Receivable{OrderID: out.ID, Reference: out.Reference, SupplierID: out.SupplierID, Status: out.Status, Lines: lines}, nil
}
