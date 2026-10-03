// Package public is the Procurement module's contract for other modules:
// Goods Receipt (Inventory) reads orders and books received quantities in its
// own transaction, and adapters connect Procurement to the Products and
// Approvals contracts.
package public

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	approvalspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/procurement/application"
	productspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/products/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Caller identifies who acts and the operation the change belongs to.
type Caller struct {
	Actor         audit.Actor
	CorrelationID string
}

// Errors other modules may need to recognize.
var (
	ErrNotFound    = application.ErrNotFound
	ErrOverReceipt = application.ErrOverReceipt
)

// InvalidTransitionError and InvalidInputError are the module's refusal types.
type (
	InvalidTransitionError = application.InvalidTransitionError
	InvalidInputError      = application.InvalidInputError
)

// Line is a Purchase Order line as goods receipt sees it.
type Line struct {
	ID               string
	LineNo           int
	ProductID        string
	Quantity         int
	ReceivedQuantity int
	UnitPriceCents   int64
}

// Order is a Purchase Order as goods receipt sees it.
type Order struct {
	ID         string
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

// Orders is the module's public service.
type Orders struct{ svc *application.Service }

func New(svc *application.Service) *Orders { return &Orders{svc: svc} }

func view(r application.Receivable) Order {
	out := Order{ID: r.OrderID, Reference: r.Reference, SupplierID: r.SupplierID, Status: r.Status, Lines: make([]Line, 0, len(r.Lines))}
	for _, l := range r.Lines {
		out.Lines = append(out.Lines, Line{ID: l.ID, LineNo: l.LineNo, ProductID: l.ProductID, Quantity: l.Quantity,
			ReceivedQuantity: l.ReceivedQuantity, UnitPriceCents: l.UnitPriceCents})
	}
	return out
}

// Order returns an order with its lines.
func (o *Orders) Order(ctx context.Context, id string) (Order, error) {
	r, err := o.svc.ReceivableOrder(ctx, id)
	if err != nil {
		return Order{}, err
	}
	return view(r), nil
}

// RecordReceiptInTx books received quantities on the order's lines in the
// caller's transaction (see application.Service.RecordReceiptInTx).
func (o *Orders) RecordReceiptInTx(ctx context.Context, tx pgx.Tx, c Caller, orderID string, lines []ReceiptLine) (Order, error) {
	receipt := make([]application.ReceiptLine, 0, len(lines))
	for _, l := range lines {
		receipt = append(receipt, application.ReceiptLine{LineID: l.LineID, Quantity: l.Quantity})
	}
	r, err := o.svc.RecordReceiptInTx(ctx, tx, application.Caller{Actor: c.Actor, CorrelationID: c.CorrelationID}, orderID, receipt)
	if err != nil {
		return Order{}, err
	}
	return view(r), nil
}

// Products adapts the Products directory to the questions procurement asks.
type Products struct{ dir *productspublic.Directory }

func NewProducts(dir *productspublic.Directory) *Products { return &Products{dir: dir} }

func (p *Products) Products(ctx context.Context, ids []string) (map[string]application.ProductInfo, error) {
	found, err := p.dir.Products(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]application.ProductInfo, len(found))
	for id, pr := range found {
		out[id] = application.ProductInfo{ID: pr.ID, Name: pr.Name, Active: pr.Active, Serialized: pr.Serialized, StockManaged: pr.StockManaged, AssetManaged: pr.AssetManaged}
	}
	return out, nil
}

// Approvals adapts the Approvals contract to what purchase orders need.
type Approvals struct{ a *approvalspublic.Approvals }

func NewApprovals(a *approvalspublic.Approvals) *Approvals { return &Approvals{a: a} }

func (x *Approvals) RequestInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID string, r application.ApprovalRequest) error {
	requestedBy := actor.UserID
	var by *string
	if requestedBy != "" {
		by = &requestedBy
	}
	_, err := x.a.RequestInTx(ctx, tx, approvalspublic.Caller{Actor: actor, CorrelationID: correlationID}, approvalspublic.Request{
		SubjectType: application.SubjectType, SubjectID: r.SubjectID, SubjectLabel: r.Label, StepIndex: 0,
		ApproverUserID: r.ApproverUserID, ApproverTeamID: r.ApproverTeamID, ExcludedUserIDs: r.ExcludedUserIDs, RequestedBy: by,
	})
	var inv *approvalspublic.InvalidInputError
	if errors.Is(err, approvalspublic.ErrApproverInvalid) || errors.As(err, &inv) {
		return application.ErrNoEligibleApprover
	}
	return err
}

func (x *Approvals) CancelBySubjectInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, subjectID string) error {
	_, err := x.a.CancelBySubjectInTx(ctx, tx, approvalspublic.Caller{Actor: actor, CorrelationID: correlationID}, application.SubjectType, subjectID)
	return err
}
