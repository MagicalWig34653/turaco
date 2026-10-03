package application

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Purchase Order operations.
const (
	OpSubmit      = "submit"
	OpSend        = "send"
	OpAcknowledge = "acknowledge"
	OpCancel      = "cancel"
	OpClose       = "close"
)

// AllowedOperations lists the operations the status allows (the API offers
// them to people with procurement.manage; line edits need draft).
func AllowedOperations(status string) []string {
	switch status {
	case POStatusDraft:
		return []string{OpSubmit, OpCancel}
	case POStatusPendingApproval, POStatusApproved:
		if status == POStatusApproved {
			return []string{OpSend, OpCancel}
		}
		return []string{OpCancel}
	case POStatusSent:
		return []string{OpAcknowledge, OpCancel}
	case POStatusAcknowledged:
		return []string{OpCancel}
	case POStatusPartiallyReceived, POStatusReceived:
		return []string{OpClose}
	}
	return []string{}
}

func orderState(o *Order) any {
	if o == nil {
		return nil
	}
	return map[string]any{"status": o.Status, "supplierId": o.SupplierID, "currency": o.Currency, "version": o.Version}
}

// Detail is an order with its lines, totals and the operations its status allows.
type Detail struct {
	Order        Order
	Lines        []Line
	TotalCents   int64
	SupplierName string
	ProductNames map[string]string
	// ProductTracking maps a product to "asset", "stock" or "none" (how goods are tracked once received).
	ProductTracking map[string]string
	AllowedOps      []string
	LinesEditable   bool
}

// ---- create and edit ----

// NewOrder describes a new Purchase Order.
type NewOrder struct {
	SupplierID string
	Currency   string
	Notes      string
}

// CreateOrder creates a draft Purchase Order. Requires procurement.manage.
func (s *Service) CreateOrder(ctx context.Context, c Caller, p Principal, in NewOrder) (Order, error) {
	if err := c.validate(); err != nil {
		return Order{}, err
	}
	if !p.Manage {
		return Order{}, ErrForbidden
	}
	currency := in.Currency
	if currency == "" {
		currency = "EUR"
	}
	if !currencyPattern.MatchString(currency) {
		return Order{}, invalid("currency must be a three-letter ISO code such as EUR")
	}
	notes, err := cleanNotes(in.Notes)
	if err != nil {
		return Order{}, err
	}
	var out Order
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		active, err := s.store.ActiveSuppliersTx(ctx, tx, []string{in.SupplierID})
		if err != nil {
			return err
		}
		if !active[in.SupplierID] {
			return ErrSupplierInvalid
		}
		out, err = s.store.InsertOrderTx(ctx, tx, in.SupplierID, currency, strPtr(notes), userPtr(c))
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "procurement.order.created", "purchase_order", out.ID, nil, orderState(&out), nil)
	})
	return out, err
}

// OrderUpdate changes a draft order; nil fields stay unchanged.
type OrderUpdate struct {
	SupplierID *string
	Currency   *string
	Notes      *string
}

// UpdateOrder changes the supplier, currency or notes of a draft order.
// Requires procurement.manage.
func (s *Service) UpdateOrder(ctx context.Context, c Caller, p Principal, id string, expected int, in OrderUpdate) (Order, error) {
	if err := c.validate(); err != nil {
		return Order{}, err
	}
	if !p.Manage {
		return Order{}, ErrForbidden
	}
	var notes string
	if in.Notes != nil {
		n, err := cleanNotes(*in.Notes)
		if err != nil {
			return Order{}, err
		}
		notes = n
	}
	if in.Currency != nil && !currencyPattern.MatchString(*in.Currency) {
		return Order{}, invalid("currency must be a three-letter ISO code such as EUR")
	}
	var out Order
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockOrderTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Version != expected {
			return ErrVersionConflict
		}
		if cur.Status != POStatusDraft {
			return &InvalidTransitionError{Operation: "update", From: cur.Status}
		}
		if err := s.recordEditor(ctx, tx, c, id); err != nil {
			return err
		}
		next := cur
		var changed []string
		if in.SupplierID != nil && *in.SupplierID != cur.SupplierID {
			active, err := s.store.ActiveSuppliersTx(ctx, tx, []string{*in.SupplierID})
			if err != nil {
				return err
			}
			if !active[*in.SupplierID] {
				return ErrSupplierInvalid
			}
			next.SupplierID = *in.SupplierID
			changed = append(changed, "supplier")
		}
		if in.Currency != nil && *in.Currency != cur.Currency {
			next.Currency = *in.Currency
			changed = append(changed, "currency")
		}
		if in.Notes != nil {
			next.Notes = strPtr(notes)
			changed = append(changed, "notes")
		}
		if len(changed) == 0 {
			out = cur
			return nil
		}
		out, err = s.store.UpdateOrderTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "procurement.order.updated", "purchase_order", id, orderState(&cur), orderState(&out), map[string]any{"changedFields": changed})
	})
	return out, err
}

// ---- lines ----

// NewLine describes a Purchase Order line. When RequestID is set the line
// satisfies that procurement request: the product must match and Quantity
// defaults to the requested quantity (and may not be below it).
type NewLine struct {
	ProductID      string
	Quantity       int
	UnitPriceCents int64
	RequestID      string
}

func checkPrice(c int64) error {
	if c < 0 || c > MaxPriceCents {
		return invalid("unit price must be between 0 and %d minor units", MaxPriceCents)
	}
	return nil
}

// lockDraft locks an order and requires status draft and the expected version.
func (s *Service) lockDraft(ctx context.Context, tx pgx.Tx, id string, expected *int, op string) (Order, error) {
	cur, err := s.store.LockOrderTx(ctx, tx, id)
	if err != nil {
		return Order{}, err
	}
	if expected != nil && *expected != cur.Version {
		return Order{}, ErrVersionConflict
	}
	if cur.Status != POStatusDraft {
		return Order{}, &InvalidTransitionError{Operation: op, From: cur.Status}
	}
	return cur, nil
}

// recordEditor remembers who changed a draft: editors can never approve the order.
func (s *Service) recordEditor(ctx context.Context, tx pgx.Tx, c Caller, orderID string) error {
	if c.Actor.UserID == "" {
		return nil
	}
	return s.store.AddEditorTx(ctx, tx, orderID, c.Actor.UserID)
}

// touch bumps the order version after a line change so editors detect it.
func (s *Service) touch(ctx context.Context, tx pgx.Tx, o Order) (Order, error) {
	return s.store.UpdateOrderTx(ctx, tx, o)
}

// AddLine adds a line to a draft order. Requires procurement.manage.
func (s *Service) AddLine(ctx context.Context, c Caller, p Principal, orderID string, expected *int, in NewLine) (Line, error) {
	if err := c.validate(); err != nil {
		return Line{}, err
	}
	if !p.Manage {
		return Line{}, ErrForbidden
	}
	if err := checkPrice(in.UnitPriceCents); err != nil {
		return Line{}, err
	}
	found, err := s.products.Products(ctx, []string{in.ProductID})
	if err != nil {
		return Line{}, fmt.Errorf("check product: %w", err)
	}
	if pr, ok := found[in.ProductID]; !ok || !pr.Active {
		return Line{}, ErrProductInvalid
	}
	var out Line
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		order, err := s.lockDraft(ctx, tx, orderID, expected, "add_line")
		if err != nil {
			return err
		}
		if err := s.recordEditor(ctx, tx, c, orderID); err != nil {
			return err
		}
		lines, err := s.store.LinesTx(ctx, tx, orderID, false)
		if err != nil {
			return err
		}
		if len(lines) >= MaxLines {
			return invalid("an order has at most %d lines", MaxLines)
		}
		line := Line{OrderID: orderID, LineNo: nextLineNo(lines), ProductID: in.ProductID, Quantity: in.Quantity, UnitPriceCents: in.UnitPriceCents}
		if in.RequestID != "" {
			need, err := s.store.LockNeedTx(ctx, tx, in.RequestID)
			if err != nil {
				if err == ErrNotFound {
					return ErrNeedUnavailable
				}
				return err
			}
			if need.Status != NeedOpen || need.ProductID != in.ProductID {
				return ErrNeedUnavailable
			}
			if line.Quantity == 0 {
				line.Quantity = need.Quantity
			}
			if line.Quantity < need.Quantity {
				return invalid("the line quantity may not be below the requested quantity of %d", need.Quantity)
			}
			line.RequestID = &in.RequestID
			next := need
			next.Status = NeedOrdered
			if _, err := s.store.UpdateNeedTx(ctx, tx, next); err != nil {
				return err
			}
		}
		if err := checkQuantity(line.Quantity); err != nil {
			return err
		}
		out, err = s.store.InsertLineTx(ctx, tx, line)
		if err != nil {
			return err
		}
		if _, err := s.touch(ctx, tx, order); err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "procurement.order.line_added", "purchase_order", orderID, nil, nil, lineMeta(out))
	})
	return out, err
}

func nextLineNo(lines []Line) int {
	n := 0
	for _, l := range lines {
		n = max(n, l.LineNo)
	}
	return n + 1
}

func lineMeta(l Line) map[string]any {
	m := map[string]any{"lineId": l.ID, "lineNo": l.LineNo, "productId": l.ProductID, "quantity": l.Quantity, "unitPriceCents": l.UnitPriceCents}
	if l.RequestID != nil {
		m["requestId"] = *l.RequestID
	}
	return m
}

// UpdateLine changes the quantity and price of a line of a draft order.
// Requires procurement.manage.
func (s *Service) UpdateLine(ctx context.Context, c Caller, p Principal, orderID, lineID string, expected *int, quantity int, unitPriceCents int64) (Line, error) {
	if err := c.validate(); err != nil {
		return Line{}, err
	}
	if !p.Manage {
		return Line{}, ErrForbidden
	}
	if err := checkQuantity(quantity); err != nil {
		return Line{}, err
	}
	if err := checkPrice(unitPriceCents); err != nil {
		return Line{}, err
	}
	var out Line
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		order, err := s.lockDraft(ctx, tx, orderID, expected, "update_line")
		if err != nil {
			return err
		}
		if err := s.recordEditor(ctx, tx, c, orderID); err != nil {
			return err
		}
		lines, err := s.store.LinesTx(ctx, tx, orderID, true)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(lines, func(l Line) bool { return l.ID == lineID })
		if i < 0 {
			return ErrNotFound
		}
		line := lines[i]
		if line.RequestID != nil {
			need, err := s.store.LockNeedTx(ctx, tx, *line.RequestID)
			if err != nil {
				return err
			}
			if quantity < need.Quantity {
				return invalid("the line quantity may not be below the requested quantity of %d", need.Quantity)
			}
		}
		line.Quantity, line.UnitPriceCents = quantity, unitPriceCents
		out, err = s.store.UpdateLineTx(ctx, tx, line)
		if err != nil {
			return err
		}
		if _, err := s.touch(ctx, tx, order); err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "procurement.order.line_updated", "purchase_order", orderID, nil, nil, lineMeta(out))
	})
	return out, err
}

// RemoveLine removes a line of a draft order; its procurement request becomes
// open again. Requires procurement.manage.
func (s *Service) RemoveLine(ctx context.Context, c Caller, p Principal, orderID, lineID string, expected *int) error {
	if err := c.validate(); err != nil {
		return err
	}
	if !p.Manage {
		return ErrForbidden
	}
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		order, err := s.lockDraft(ctx, tx, orderID, expected, "remove_line")
		if err != nil {
			return err
		}
		if err := s.recordEditor(ctx, tx, c, orderID); err != nil {
			return err
		}
		lines, err := s.store.LinesTx(ctx, tx, orderID, true)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(lines, func(l Line) bool { return l.ID == lineID })
		if i < 0 {
			return ErrNotFound
		}
		line := lines[i]
		if _, err := s.store.DeleteLineTx(ctx, tx, orderID, lineID); err != nil {
			return err
		}
		if line.RequestID != nil {
			if err := s.reopenNeeds(ctx, tx, c, []string{*line.RequestID}); err != nil {
				return err
			}
		}
		if _, err := s.touch(ctx, tx, order); err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "procurement.order.line_removed", "purchase_order", orderID, nil, nil, lineMeta(line))
	})
}

// reopenNeeds puts ordered needs back to open (in id order, after the order lock).
func (s *Service) reopenNeeds(ctx context.Context, tx pgx.Tx, c Caller, ids []string) error {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	for _, id := range ids {
		need, err := s.store.LockNeedTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if need.Status != NeedOrdered {
			continue
		}
		next := need
		next.Status = NeedOpen
		out, err := s.store.UpdateNeedTx(ctx, tx, next)
		if err != nil {
			return err
		}
		if err := recordAudit(ctx, tx, c, "procurement.request.reopened", "procurement_request", id, needState(&need), needState(&out), nil); err != nil {
			return err
		}
	}
	return nil
}

// ---- lifecycle ----

// Approver names who approves a Purchase Order: exactly one of User and Team.
type Approver struct {
	UserID *string
	TeamID *string
}

// Submit sends a draft order to approval. The order needs at least one line;
// the creator and the submitter can never approve it. Requires procurement.manage.
func (s *Service) Submit(ctx context.Context, c Caller, p Principal, id string, expected *int, approver Approver) (Order, error) {
	if err := c.validate(); err != nil {
		return Order{}, err
	}
	if !p.Manage {
		return Order{}, ErrForbidden
	}
	if (approver.UserID == nil) == (approver.TeamID == nil) {
		return Order{}, invalid("exactly one approver (user or team) is required")
	}
	var out Order
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockDraft(ctx, tx, id, expected, OpSubmit)
		if err != nil {
			return err
		}
		lines, err := s.store.LinesTx(ctx, tx, id, false)
		if err != nil {
			return err
		}
		if len(lines) == 0 {
			return invalid("an order needs at least one line before it can be submitted")
		}
		active, err := s.store.ActiveSuppliersTx(ctx, tx, []string{cur.SupplierID})
		if err != nil {
			return err
		}
		if !active[cur.SupplierID] {
			return ErrSupplierInvalid
		}
		excluded := []string{}
		if cur.CreatedBy != nil {
			excluded = append(excluded, *cur.CreatedBy)
		}
		if me := c.Actor.UserID; me != "" {
			excluded = append(excluded, me)
		}
		excluded = append(excluded, cur.Editors...)
		slices.Sort(excluded)
		excluded = slices.Compact(excluded)
		next := cur
		next.Status, next.StatusReason = POStatusPendingApproval, nil
		out, err = s.store.UpdateOrderTx(ctx, tx, next)
		if err != nil {
			return err
		}
		if err := s.approvals.RequestInTx(ctx, tx, c.Actor, c.CorrelationID, ApprovalRequest{
			SubjectID: id, Label: cur.Reference, ApproverUserID: approver.UserID, ApproverTeamID: approver.TeamID, ExcludedUserIDs: excluded,
		}); err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "procurement.order.submit", "purchase_order", id, orderState(&cur), orderState(&out), nil)
	})
	return out, err
}

// Send marks an approved order as sent to the supplier. Requires procurement.manage.
func (s *Service) Send(ctx context.Context, c Caller, p Principal, id string, expected *int) (Order, error) {
	return s.advance(ctx, c, p, id, expected, OpSend, []string{POStatusApproved}, POStatusSent, "PurchaseOrderSent")
}

// Acknowledge records the supplier's confirmation of a sent order. Requires procurement.manage.
func (s *Service) Acknowledge(ctx context.Context, c Caller, p Principal, id string, expected *int) (Order, error) {
	return s.advance(ctx, c, p, id, expected, OpAcknowledge, []string{POStatusSent}, POStatusAcknowledged, "")
}

func (s *Service) advance(ctx context.Context, c Caller, p Principal, id string, expected *int, op string, from []string, to, event string) (Order, error) {
	if err := c.validate(); err != nil {
		return Order{}, err
	}
	if !p.Manage {
		return Order{}, ErrForbidden
	}
	var out Order
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockOrderTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if expected != nil && *expected != cur.Version {
			return ErrVersionConflict
		}
		if !slices.Contains(from, cur.Status) {
			return &InvalidTransitionError{Operation: op, From: cur.Status}
		}
		next := cur
		next.Status = to
		if to == POStatusSent {
			now := time.Now().UTC()
			next.SentAt = &now
		}
		out, err = s.store.UpdateOrderTx(ctx, tx, next)
		if err != nil {
			return err
		}
		if err := recordAudit(ctx, tx, c, "procurement.order."+op, "purchase_order", id, orderState(&cur), orderState(&out), nil); err != nil {
			return err
		}
		if event != "" {
			return publish(ctx, tx, c, event, map[string]any{"orderId": id, "supplierId": out.SupplierID})
		}
		return nil
	})
	return out, err
}

// Cancel cancels an order that has not received anything yet; its
// procurement requests become open again and a pending approval is cancelled.
// A reason is required. Requires procurement.manage.
func (s *Service) Cancel(ctx context.Context, c Caller, p Principal, id string, expected *int, reason string) (Order, error) {
	if err := c.validate(); err != nil {
		return Order{}, err
	}
	if !p.Manage {
		return Order{}, ErrForbidden
	}
	reason, err := cleanReason(reason, true)
	if err != nil {
		return Order{}, err
	}
	var out Order
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockOrderTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if expected != nil && *expected != cur.Version {
			return ErrVersionConflict
		}
		if !slices.Contains([]string{POStatusDraft, POStatusPendingApproval, POStatusApproved, POStatusSent, POStatusAcknowledged}, cur.Status) {
			return &InvalidTransitionError{Operation: OpCancel, From: cur.Status}
		}
		if cur.Status == POStatusPendingApproval {
			if err := s.approvals.CancelBySubjectInTx(ctx, tx, c.Actor, c.CorrelationID, id); err != nil {
				return err
			}
		}
		if err := s.releaseNeeds(ctx, tx, c, id, false); err != nil {
			return err
		}
		next := cur
		now := time.Now().UTC()
		next.Status, next.StatusReason, next.ClosedAt = POStatusCancelled, &reason, &now
		out, err = s.store.UpdateOrderTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "procurement.order.cancel", "purchase_order", id, orderState(&cur), orderState(&out), map[string]any{"reason": reason})
	})
	return out, err
}

// releaseNeeds detaches the needs of an order's lines and reopens them.
func (s *Service) releaseNeeds(ctx context.Context, tx pgx.Tx, c Caller, orderID string, onlyUnfulfilled bool) error {
	ids, err := s.store.ClearLineRequestsTx(ctx, tx, orderID, onlyUnfulfilled)
	if err != nil {
		return err
	}
	return s.reopenNeeds(ctx, tx, c, ids)
}

// Close closes a received order, or a partially received one when the missing
// quantity is accepted (a reason is then required; the procurement requests
// for what never arrived become open again). Requires procurement.manage.
func (s *Service) Close(ctx context.Context, c Caller, p Principal, id string, expected *int, reason string) (Order, error) {
	if err := c.validate(); err != nil {
		return Order{}, err
	}
	if !p.Manage {
		return Order{}, ErrForbidden
	}
	reason = strings.TrimSpace(reason)
	var out Order
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockOrderTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if expected != nil && *expected != cur.Version {
			return ErrVersionConflict
		}
		if cur.Status != POStatusReceived && cur.Status != POStatusPartiallyReceived {
			return &InvalidTransitionError{Operation: OpClose, From: cur.Status}
		}
		next := cur
		meta := map[string]any(nil)
		if cur.Status == POStatusPartiallyReceived {
			r, err := cleanReason(reason, true)
			if err != nil {
				return err
			}
			next.StatusReason = &r
			meta = map[string]any{"reason": r, "short": true}
			if err := s.releaseNeeds(ctx, tx, c, id, true); err != nil {
				return err
			}
		}
		now := time.Now().UTC()
		next.Status, next.ClosedAt = POStatusClosed, &now
		out, err = s.store.UpdateOrderTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "procurement.order.close", "purchase_order", id, orderState(&cur), orderState(&out), meta)
	})
	return out, err
}

// ---- reads ----

// ListOrders lists purchase orders. Requires procurement.view.
func (s *Service) ListOrders(ctx context.Context, p Principal, f OrderFilter) (Result[Order], error) {
	if !p.canView() {
		return Result[Order]{}, ErrForbidden
	}
	if f.Status != "" && !slices.Contains(POStatuses, f.Status) {
		return Result[Order]{}, invalid("unknown status")
	}
	f.Page = f.Page.Normalize()
	return s.store.ListOrders(ctx, f)
}

// GetOrder returns an order with its lines. Requires procurement.view.
func (s *Service) GetOrder(ctx context.Context, p Principal, id string) (Detail, error) {
	if !p.canView() {
		return Detail{}, ErrForbidden
	}
	o, err := s.store.GetOrder(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	lines, err := s.store.Lines(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	d := Detail{Order: o, Lines: lines, ProductNames: map[string]string{}, ProductTracking: map[string]string{}, LinesEditable: o.Status == POStatusDraft}
	if p.Manage {
		d.AllowedOps = AllowedOperations(o.Status)
	} else {
		d.AllowedOps = []string{}
	}
	ids := make([]string, 0, len(lines))
	for _, l := range lines {
		d.TotalCents += int64(l.Quantity) * l.UnitPriceCents // bounded: 100 lines x 1e6 x 1e9 < 2^63
		ids = append(ids, l.ProductID)
	}
	if sup, err := s.store.GetSupplier(ctx, o.SupplierID); err == nil {
		d.SupplierName = sup.Name
	}
	if len(ids) > 0 {
		found, err := s.products.Products(ctx, ids)
		if err != nil {
			return Detail{}, fmt.Errorf("load product names: %w", err)
		}
		for id, pr := range found {
			d.ProductNames[id] = pr.Name
			d.ProductTracking[id] = pr.Tracking()
		}
	}
	return d, nil
}
