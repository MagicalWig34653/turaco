package repository_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/procurement/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/procurement/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

type products map[string]application.ProductInfo

func (p products) Products(_ context.Context, ids []string) (map[string]application.ProductInfo, error) {
	out := map[string]application.ProductInfo{}
	for _, id := range ids {
		if v, ok := p[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

type dir struct{}

func (dir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// approvals records the requests; the decision is simulated by calling OnApprovalDecided.
type approvals struct {
	mu        sync.Mutex
	requested []application.ApprovalRequest
	cancelled []string
	refuse    error
}

func (a *approvals) RequestInTx(_ context.Context, _ pgx.Tx, _ audit.Actor, _ string, r application.ApprovalRequest) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.refuse != nil {
		return a.refuse
	}
	a.requested = append(a.requested, r)
	return nil
}
func (a *approvals) CancelBySubjectInTx(_ context.Context, _ pgx.Tx, _ audit.Actor, _, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cancelled = append(a.cancelled, id)
	return nil
}

type env struct {
	t                          *testing.T
	pool                       *pgxpool.Pool
	svc                        *application.Service
	appr                       *approvals
	corr                       string
	manager, other, approverID string
	cable, laptop, retired     string
	supplier                   application.Supplier
	manage, view               application.Principal
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	e := &env{t: t, pool: pool, corr: "procurement-" + hex.EncodeToString(b), appr: &approvals{}}
	for _, dst := range []*string{&e.manager, &e.other, &e.approverID, &e.cable, &e.laptop, &e.retired} {
		if err := pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	pr := products{
		e.cable:   {ID: e.cable, Name: "Cable", Active: true},
		e.laptop:  {ID: e.laptop, Name: "Laptop", Active: true},
		e.retired: {ID: e.retired, Name: "Old", Active: false},
	}
	e.svc = application.NewService(repository.New(pool), dir{}, pr, e.appr)
	e.manage = application.Principal{UserID: e.manager, Manage: true}
	e.view = application.Principal{UserID: e.other, View: true}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, e.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, e.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM procurement.purchase_orders WHERE supplier_id IN (SELECT id FROM procurement.suppliers WHERE name LIKE $1)`, e.corr+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM procurement.procurement_requests WHERE product_id = ANY($1::uuid[])`, []string{e.cable, e.laptop, e.retired})
		_, _ = pool.Exec(ctx, `DELETE FROM procurement.suppliers WHERE name LIKE $1`, e.corr+"%")
	})
	var err error
	e.supplier, err = e.svc.CreateSupplier(context.Background(), e.caller(e.manager), e.manage, e.corr+" Supplies", "C-1")
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) caller(user string) application.Caller {
	return application.Caller{Actor: audit.UserActor(user), CorrelationID: e.corr}
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) order() application.Order {
	e.t.Helper()
	o, err := e.svc.CreateOrder(context.Background(), e.caller(e.manager), e.manage, application.NewOrder{SupplierID: e.supplier.ID})
	if err != nil {
		e.t.Fatal(err)
	}
	return o
}

func (e *env) line(orderID, product string, qty int, price int64, request string) application.Line {
	e.t.Helper()
	l, err := e.svc.AddLine(context.Background(), e.caller(e.manager), e.manage, orderID, nil, application.NewLine{ProductID: product, Quantity: qty, UnitPriceCents: price, RequestID: request})
	if err != nil {
		e.t.Fatalf("add line: %v", err)
	}
	return l
}

func (e *env) status(id string) string {
	e.t.Helper()
	o, err := e.svc.GetOrder(context.Background(), e.view, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return o.Order.Status
}

// decide simulates the ApprovalDecided event reaching the workflow consumer.
func (e *env) decide(orderID, decision string) {
	e.t.Helper()
	payload, _ := json.Marshal(map[string]any{"approvalId": "x", "subjectType": "purchase_order", "subjectId": orderID, "stepIndex": 0, "decision": decision})
	err := pgx.BeginFunc(context.Background(), e.pool, func(tx pgx.Tx) error {
		return e.svc.OnApprovalDecided(context.Background(), tx, events.OutboxEvent{ID: "e", EventType: "ApprovalDecided", Payload: payload, CorrelationID: e.corr})
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) approve(orderID string) application.Order {
	e.t.Helper()
	o, err := e.svc.Submit(context.Background(), e.caller(e.manager), e.manage, orderID, nil, application.Approver{UserID: &e.approverID})
	if err != nil {
		e.t.Fatalf("submit: %v", err)
	}
	e.decide(orderID, "approve")
	return o
}

func TestPurchaseOrderLifecycleWithApproval(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	o := e.order()
	var inv *application.InvalidInputError
	if _, err := e.svc.Submit(ctx, e.caller(e.manager), e.manage, o.ID, nil, application.Approver{UserID: &e.approverID}); !errors.As(err, &inv) {
		t.Errorf("submitting an empty order: %v", err)
	}
	e.line(o.ID, e.cable, 10, 250, "")
	e.line(o.ID, e.laptop, 2, 90000, "")
	d, err := e.svc.GetOrder(ctx, e.manage, o.ID)
	if err != nil || len(d.Lines) != 2 || d.TotalCents != 10*250+2*90000 || d.Lines[1].LineNo != 2 || !d.LinesEditable {
		t.Fatalf("detail = %+v %v", d, err)
	}
	if _, err := e.svc.Submit(ctx, e.caller(e.manager), e.manage, o.ID, nil, application.Approver{}); !errors.As(err, &inv) {
		t.Errorf("submit needs exactly one approver: %v", err)
	}
	if _, err := e.svc.Submit(ctx, e.caller(e.manager), e.manage, o.ID, nil, application.Approver{UserID: &e.approverID}); err != nil {
		t.Fatal(err)
	}
	if len(e.appr.requested) != 1 || e.appr.requested[0].SubjectID != o.ID || len(e.appr.requested[0].ExcludedUserIDs) != 1 || e.appr.requested[0].ExcludedUserIDs[0] != e.manager {
		t.Fatalf("approval request = %+v", e.appr.requested)
	}
	var tr *application.InvalidTransitionError
	if _, err := e.svc.AddLine(ctx, e.caller(e.manager), e.manage, o.ID, nil, application.NewLine{ProductID: e.cable, Quantity: 1}); !errors.As(err, &tr) {
		t.Errorf("a pending order is not editable: %v", err)
	}
	if _, err := e.svc.Send(ctx, e.caller(e.manager), e.manage, o.ID, nil); !errors.As(err, &tr) {
		t.Errorf("sending before approval: %v", err)
	}
	e.decide(o.ID, "approve")
	e.decide(o.ID, "approve") // redelivery changes nothing
	if e.status(o.ID) != "approved" {
		t.Fatalf("status = %s", e.status(o.ID))
	}
	if e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'PurchaseOrderApproved'`, e.corr) != 1 {
		t.Error("PurchaseOrderApproved must be published exactly once")
	}
	sent, err := e.svc.Send(ctx, e.caller(e.manager), e.manage, o.ID, nil)
	if err != nil || sent.Status != "sent" || sent.SentAt == nil {
		t.Fatalf("send = %+v %v", sent, err)
	}
	if ack, err := e.svc.Acknowledge(ctx, e.caller(e.manager), e.manage, o.ID, nil); err != nil || ack.Status != "acknowledged" {
		t.Fatalf("acknowledge = %+v %v", ack, err)
	}
	if _, err := e.svc.Acknowledge(ctx, e.caller(e.manager), e.manage, o.ID, nil); !errors.As(err, &tr) {
		t.Errorf("acknowledging twice: %v", err)
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action LIKE 'procurement.order.%'`, e.corr) < 6 {
		t.Error("every order operation must be audited")
	}
}

func TestRejectionReturnsTheOrderToDraft(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	o := e.order()
	e.line(o.ID, e.cable, 1, 100, "")
	if _, err := e.svc.Submit(ctx, e.caller(e.manager), e.manage, o.ID, nil, application.Approver{UserID: &e.approverID}); err != nil {
		t.Fatal(err)
	}
	e.decide(o.ID, "reject")
	d, err := e.svc.GetOrder(ctx, e.view, o.ID)
	if err != nil || d.Order.Status != "draft" || d.Order.StatusReason == nil || *d.Order.StatusReason != application.RejectedReason {
		t.Fatalf("after rejection = %+v %v", d.Order, err)
	}
	if _, err := e.svc.UpdateLine(ctx, e.caller(e.manager), e.manage, o.ID, d.Lines[0].ID, nil, 2, 100); err != nil {
		t.Errorf("a rejected order can be corrected: %v", err)
	}
	if _, err := e.svc.Submit(ctx, e.caller(e.manager), e.manage, o.ID, nil, application.Approver{UserID: &e.approverID}); err != nil {
		t.Errorf("and submitted again: %v", err)
	}
}

func TestApprovalRefusalAndIgnoredEvents(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	o := e.order()
	e.line(o.ID, e.cable, 1, 100, "")
	e.appr.refuse = application.ErrNoEligibleApprover
	if _, err := e.svc.Submit(ctx, e.caller(e.manager), e.manage, o.ID, nil, application.Approver{UserID: &e.manager}); !errors.Is(err, application.ErrNoEligibleApprover) {
		t.Errorf("approver refused: %v", err)
	}
	if e.status(o.ID) != "draft" {
		t.Error("a refused submission must leave the order in draft")
	}
	// Events of other subjects and for orders that are not pending change nothing.
	foreign, _ := json.Marshal(map[string]any{"subjectType": "service_request", "subjectId": o.ID, "decision": "approve"})
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		return e.svc.OnApprovalDecided(ctx, tx, events.OutboxEvent{Payload: foreign, CorrelationID: e.corr})
	}); err != nil {
		t.Fatal(err)
	}
	e.decide(o.ID, "approve")
	if e.status(o.ID) != "draft" {
		t.Error("a stale approval must not approve a draft")
	}
	bad, _ := json.Marshal(map[string]any{"subjectType": "purchase_order", "subjectId": o.ID, "decision": "maybe"})
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		return e.svc.OnApprovalDecided(ctx, tx, events.OutboxEvent{Payload: bad, CorrelationID: e.corr})
	})
	if !events.IsPermanent(err) {
		t.Errorf("unknown decision: %v", err)
	}
}

func TestNeedsLinkToLinesAndFollowTheOrder(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	need, err := e.svc.CreateNeed(ctx, e.caller(e.manager), e.manage, application.NewNeed{ProductID: e.cable, Quantity: 5, ContextType: "service_request", ContextID: e.other, Notes: "for the new office"})
	if err != nil || need.Status != "open" || need.Reference == "" {
		t.Fatalf("need = %+v %v", need, err)
	}
	o := e.order()
	if _, err := e.svc.AddLine(ctx, e.caller(e.manager), e.manage, o.ID, nil, application.NewLine{ProductID: e.laptop, Quantity: 5, RequestID: need.ID}); !errors.Is(err, application.ErrNeedUnavailable) {
		t.Errorf("a need for another product: %v", err)
	}
	var inv *application.InvalidInputError
	if _, err := e.svc.AddLine(ctx, e.caller(e.manager), e.manage, o.ID, nil, application.NewLine{ProductID: e.cable, Quantity: 3, RequestID: need.ID}); !errors.As(err, &inv) {
		t.Errorf("a line below the requested quantity: %v", err)
	}
	line, err := e.svc.AddLine(ctx, e.caller(e.manager), e.manage, o.ID, nil, application.NewLine{ProductID: e.cable, RequestID: need.ID, UnitPriceCents: 100})
	if err != nil || line.Quantity != 5 {
		t.Fatalf("the quantity defaults to the request: %+v %v", line, err)
	}
	if got, _ := e.svc.GetNeed(ctx, e.view, need.ID); got.Status != "ordered" {
		t.Errorf("need = %s", got.Status)
	}
	o2 := e.order()
	if _, err := e.svc.AddLine(ctx, e.caller(e.manager), e.manage, o2.ID, nil, application.NewLine{ProductID: e.cable, RequestID: need.ID}); !errors.Is(err, application.ErrNeedUnavailable) {
		t.Errorf("a need can be on one line only: %v", err)
	}
	if _, err := e.svc.CancelNeed(ctx, e.caller(e.manager), e.manage, need.ID, nil, "not needed"); err == nil {
		t.Error("an ordered need cannot be cancelled")
	}
	// Removing the line reopens the need; cancelling the order does too.
	if err := e.svc.RemoveLine(ctx, e.caller(e.manager), e.manage, o.ID, line.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.GetNeed(ctx, e.view, need.ID); got.Status != "open" {
		t.Errorf("after removing the line: %s", got.Status)
	}
	e.line(o2.ID, e.cable, 5, 100, need.ID)
	cancelled, err := e.svc.Cancel(ctx, e.caller(e.manager), e.manage, o2.ID, nil, "wrong supplier")
	if err != nil || cancelled.Status != "cancelled" || cancelled.ClosedAt == nil {
		t.Fatalf("cancel = %+v %v", cancelled, err)
	}
	if got, _ := e.svc.GetNeed(ctx, e.view, need.ID); got.Status != "open" {
		t.Errorf("after cancelling the order: %s", got.Status)
	}
	if _, err := e.svc.Cancel(ctx, e.caller(e.manager), e.manage, o2.ID, nil, "again"); err == nil {
		t.Error("a cancelled order cannot be cancelled again")
	}
	if cn, err := e.svc.CancelNeed(ctx, e.caller(e.manager), e.manage, need.ID, nil, "not needed"); err != nil || cn.Status != "cancelled" {
		t.Errorf("cancel an open need = %+v %v", cn, err)
	}
	if _, err := e.svc.CreateNeed(ctx, e.caller(e.manager), e.manage, application.NewNeed{ProductID: e.retired, Quantity: 1}); !errors.Is(err, application.ErrProductInvalid) {
		t.Errorf("inactive product: %v", err)
	}
}

func TestCancellingAPendingOrderCancelsItsApproval(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	o := e.order()
	e.line(o.ID, e.cable, 1, 100, "")
	if _, err := e.svc.Submit(ctx, e.caller(e.manager), e.manage, o.ID, nil, application.Approver{UserID: &e.approverID}); err != nil {
		t.Fatal(err)
	}
	var inv *application.InvalidInputError
	if _, err := e.svc.Cancel(ctx, e.caller(e.manager), e.manage, o.ID, nil, ""); !errors.As(err, &inv) {
		t.Errorf("cancel needs a reason: %v", err)
	}
	if _, err := e.svc.Cancel(ctx, e.caller(e.manager), e.manage, o.ID, nil, "changed plans"); err != nil {
		t.Fatal(err)
	}
	if len(e.appr.cancelled) != 1 || e.appr.cancelled[0] != o.ID {
		t.Errorf("cancelled approvals = %v", e.appr.cancelled)
	}
}

func TestReceiptTrackingPartialFullAndOverDelivery(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	need, _ := e.svc.CreateNeed(ctx, e.caller(e.manager), e.manage, application.NewNeed{ProductID: e.cable, Quantity: 10})
	o := e.order()
	l1 := e.line(o.ID, e.cable, 10, 100, need.ID)
	l2 := e.line(o.ID, e.laptop, 2, 90000, "")
	var tr *application.InvalidTransitionError
	receive := func(lines ...application.ReceiptLine) (application.Receivable, error) {
		var out application.Receivable
		err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			var err error
			out, err = e.svc.RecordReceiptInTx(ctx, tx, e.caller(e.manager), o.ID, lines)
			return err
		})
		return out, err
	}
	if _, err := receive(application.ReceiptLine{LineID: l1.ID, Quantity: 1}); !errors.As(err, &tr) {
		t.Errorf("receiving before the order was sent: %v", err)
	}
	e.approve(o.ID)
	if _, err := e.svc.Send(ctx, e.caller(e.manager), e.manage, o.ID, nil); err != nil {
		t.Fatal(err)
	}
	r, err := receive(application.ReceiptLine{LineID: l1.ID, Quantity: 4})
	if err != nil || r.Status != "partially_received" {
		t.Fatalf("first receipt = %+v %v", r, err)
	}
	if _, err := receive(application.ReceiptLine{LineID: l1.ID, Quantity: 7}); !errors.Is(err, application.ErrOverReceipt) {
		t.Errorf("over-delivery: %v", err)
	}
	var inv *application.InvalidInputError
	if _, err := receive(application.ReceiptLine{LineID: l1.ID, Quantity: 1}, application.ReceiptLine{LineID: l1.ID, Quantity: 1}); !errors.As(err, &inv) {
		t.Errorf("a line twice in one receipt: %v", err)
	}
	if _, err := receive(application.ReceiptLine{LineID: l2.ID, Quantity: 2}, application.ReceiptLine{LineID: l1.ID, Quantity: 7}); !errors.Is(err, application.ErrOverReceipt) {
		t.Errorf("a failing line must fail the whole receipt: %v", err)
	}
	if lines, _ := e.svc.ReceivableOrder(ctx, o.ID); lines.Lines[1].ReceivedQuantity != 0 {
		t.Error("the refused receipt must not book any line")
	}
	if got, _ := e.svc.GetNeed(ctx, e.view, need.ID); got.Status != "ordered" {
		t.Errorf("the need stays ordered while its line is open: %s", got.Status)
	}
	if _, err := e.svc.Cancel(ctx, e.caller(e.manager), e.manage, o.ID, nil, "too late"); !errors.As(err, &tr) {
		t.Errorf("an order with receipts cannot be cancelled: %v", err)
	}
	if _, err := e.svc.Close(ctx, e.caller(e.manager), e.manage, o.ID, nil, ""); !errors.As(err, &inv) {
		t.Errorf("closing short needs a reason: %v", err)
	}
	r, err = receive(application.ReceiptLine{LineID: l1.ID, Quantity: 6}, application.ReceiptLine{LineID: l2.ID, Quantity: 2})
	if err != nil || r.Status != "received" {
		t.Fatalf("final receipt = %+v %v", r, err)
	}
	if got, _ := e.svc.GetNeed(ctx, e.view, need.ID); got.Status != "fulfilled" {
		t.Errorf("need = %s", got.Status)
	}
	if e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'PurchaseOrderReceived'`, e.corr) != 1 {
		t.Error("PurchaseOrderReceived must be published once")
	}
	closed, err := e.svc.Close(ctx, e.caller(e.manager), e.manage, o.ID, nil, "")
	if err != nil || closed.Status != "closed" {
		t.Fatalf("close = %+v %v", closed, err)
	}
}

func TestClosingShortReopensWhatNeverArrived(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	needA, _ := e.svc.CreateNeed(ctx, e.caller(e.manager), e.manage, application.NewNeed{ProductID: e.cable, Quantity: 2})
	needB, _ := e.svc.CreateNeed(ctx, e.caller(e.manager), e.manage, application.NewNeed{ProductID: e.laptop, Quantity: 3})
	o := e.order()
	la := e.line(o.ID, e.cable, 2, 100, needA.ID)
	e.line(o.ID, e.laptop, 3, 900, needB.ID)
	e.approve(o.ID)
	if _, err := e.svc.Send(ctx, e.caller(e.manager), e.manage, o.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		_, err := e.svc.RecordReceiptInTx(ctx, tx, e.caller(e.manager), o.ID, []application.ReceiptLine{{LineID: la.ID, Quantity: 2}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Close(ctx, e.caller(e.manager), e.manage, o.ID, nil, "supplier discontinued the laptop"); err != nil {
		t.Fatal(err)
	}
	if a, _ := e.svc.GetNeed(ctx, e.view, needA.ID); a.Status != "fulfilled" {
		t.Errorf("delivered need = %s", a.Status)
	}
	if b, _ := e.svc.GetNeed(ctx, e.view, needB.ID); b.Status != "open" {
		t.Errorf("undelivered need = %s, want open", b.Status)
	}
}

func TestConcurrentReceiptsNeverOverDeliver(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	o := e.order()
	l := e.line(o.ID, e.cable, 10, 100, "")
	e.approve(o.ID)
	if _, err := e.svc.Send(ctx, e.caller(e.manager), e.manage, o.ID, nil); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var ok, refused atomic.Int32
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
				_, err := e.svc.RecordReceiptInTx(ctx, tx, e.caller(e.manager), o.ID, []application.ReceiptLine{{LineID: l.ID, Quantity: 3}})
				return err
			})
			var tr *application.InvalidTransitionError
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, application.ErrOverReceipt), errors.As(err, &tr):
				refused.Add(1)
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 3 {
		t.Errorf("%d receipts of 3 booked from 10 ordered, want 3", ok.Load())
	}
	if lines, _ := e.svc.ReceivableOrder(ctx, o.ID); lines.Lines[0].ReceivedQuantity != 9 {
		t.Errorf("received = %d, want 9", lines.Lines[0].ReceivedQuantity)
	}
}

func TestSuppliersAndPermissions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.svc.CreateSupplier(ctx, e.caller(e.manager), e.manage, e.corr+" supplies", ""); !errors.Is(err, application.ErrConflict) {
		t.Errorf("duplicate supplier name (case-insensitive): %v", err)
	}
	upd, err := e.svc.UpdateSupplier(ctx, e.caller(e.manager), e.manage, e.supplier.ID, e.supplier.Version, application.SupplierUpdate{AccountReference: ptr("")})
	if err != nil || upd.AccountReference != nil || upd.Version != 2 {
		t.Fatalf("update = %+v %v", upd, err)
	}
	if _, err := e.svc.UpdateSupplier(ctx, e.caller(e.manager), e.manage, e.supplier.ID, 1, application.SupplierUpdate{}); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale version: %v", err)
	}
	if _, err := e.svc.SetSupplierActive(ctx, e.caller(e.manager), e.manage, e.supplier.ID, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateOrder(ctx, e.caller(e.manager), e.manage, application.NewOrder{SupplierID: e.supplier.ID}); !errors.Is(err, application.ErrSupplierInvalid) {
		t.Errorf("order for an inactive supplier: %v", err)
	}
	if _, err := e.svc.CreateOrder(ctx, e.caller(e.manager), e.manage, application.NewOrder{SupplierID: e.supplier.ID, Currency: "euro"}); err == nil {
		t.Error("invalid currency accepted")
	}
	for name, fn := range map[string]func() error{
		"create supplier": func() error { _, err := e.svc.CreateSupplier(ctx, e.caller(e.other), e.view, "x", ""); return err },
		"create order": func() error {
			_, err := e.svc.CreateOrder(ctx, e.caller(e.other), e.view, application.NewOrder{})
			return err
		},
		"create need": func() error {
			_, err := e.svc.CreateNeed(ctx, e.caller(e.other), e.view, application.NewNeed{})
			return err
		},
		"send": func() error { _, err := e.svc.Send(ctx, e.caller(e.other), e.view, e.supplier.ID, nil); return err },
	} {
		if err := fn(); !errors.Is(err, application.ErrForbidden) {
			t.Errorf("%s without procurement.manage: %v", name, err)
		}
	}
	if _, err := e.svc.ListOrders(ctx, application.Principal{UserID: e.other}, application.OrderFilter{}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("list without procurement.view: %v", err)
	}
}

func ptr(s string) *string { return &s }

func TestDatabaseInvariants(t *testing.T) {
	e := newEnv(t)
	o := e.order()
	l := e.line(o.ID, e.cable, 2, 100, "")
	for name, sql := range map[string]string{
		"received above ordered": `UPDATE procurement.purchase_order_lines SET received_quantity = 3 WHERE id = $1::uuid`,
		"negative price":         `UPDATE procurement.purchase_order_lines SET unit_price_cents = -1 WHERE id = $1::uuid`,
		"zero quantity":          `UPDATE procurement.purchase_order_lines SET quantity = 0 WHERE id = $1::uuid`,
	} {
		if _, err := e.pool.Exec(context.Background(), sql, l.ID); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := e.pool.Exec(context.Background(), `UPDATE procurement.purchase_orders SET status = 'lost' WHERE id = $1::uuid`, o.ID); err == nil {
		t.Error("unknown order status accepted")
	}
	if _, err := e.pool.Exec(context.Background(), `UPDATE procurement.purchase_orders SET currency = 'eur' WHERE id = $1::uuid`, o.ID); err == nil {
		t.Error("lower-case currency accepted")
	}
}
