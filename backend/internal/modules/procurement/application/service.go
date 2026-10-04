package application

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Service performs Procurement operations. Audit actions:
// procurement.supplier.*, procurement.request.* and procurement.order.*
// (one per operation). Notes are never copied into audit; ids, quantities,
// prices, statuses and given reasons are.
type Service struct {
	store     Store
	dir       Directory
	products  Products
	approvals Approvals
}

func NewService(store Store, dir Directory, products Products, approvals Approvals) *Service {
	return &Service{store: store, dir: dir, products: products, approvals: approvals}
}

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

func publish(ctx context.Context, tx pgx.Tx, c Caller, typ string, payload map[string]any) error {
	var actor *string
	if c.Actor.UserID != "" {
		a := c.Actor.UserID
		actor = &a
	}
	return events.Publish(ctx, tx, events.Publication{Type: typ, ActorID: actor, CorrelationID: c.CorrelationID, Payload: payload})
}

func recordAudit(ctx context.Context, tx pgx.Tx, c Caller, action, targetType, targetID string, before, after any, meta map[string]any) error {
	if len(meta) == 0 {
		meta = nil
	}
	return audit.Record(ctx, tx, audit.Change{
		Action: action, TargetType: targetType, TargetID: targetID, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: before, After: after, Metadata: meta,
	})
}

func cleanName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > maxName || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, false) {
		return "", invalid("name must be 1-%d characters without control or invisible formatting characters", maxName)
	}
	return s, nil
}

func cleanNotes(s string) (string, error) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxText || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, true) {
		return "", invalid("notes must be at most %d characters without control or invisible formatting characters", maxText)
	}
	return s, nil
}

func cleanReason(s string, required bool) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" && !required {
		return "", nil
	}
	if s == "" || utf8.RuneCountInString(s) > maxReason || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, false) {
		return "", invalid("a reason of 1-%d characters without control or invisible formatting characters is required", maxReason)
	}
	return s, nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func userPtr(c Caller) *string {
	if c.Actor.UserID == "" {
		return nil
	}
	u := c.Actor.UserID
	return &u
}

func checkQuantity(q int) error {
	if q < 1 || q > MaxQuantity {
		return invalid("quantity must be between 1 and %d", MaxQuantity)
	}
	return nil
}

// ---- suppliers ----

func supplierState(s *Supplier) any {
	if s == nil {
		return nil
	}
	return map[string]any{"active": s.Active, "version": s.Version}
}

// CreateSupplier creates a supplier. Requires procurement.manage.
func (s *Service) CreateSupplier(ctx context.Context, c Caller, p Principal, name, accountReference string) (Supplier, error) {
	if err := c.validate(); err != nil {
		return Supplier{}, err
	}
	if !p.Manage {
		return Supplier{}, ErrForbidden
	}
	name, err := cleanName(name)
	if err != nil {
		return Supplier{}, err
	}
	account, err := cleanAccount(accountReference)
	if err != nil {
		return Supplier{}, err
	}
	var out Supplier
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		out, err = s.store.InsertSupplierTx(ctx, tx, name, account)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "procurement.supplier.created", "supplier", out.ID, nil, supplierState(&out), nil)
	})
	return out, err
}

func cleanAccount(s string) (*string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(s) > 100 || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, false) {
		return nil, invalid("account reference must be at most 100 characters without control or invisible formatting characters")
	}
	return &s, nil
}

// SupplierUpdate changes a supplier; nil fields stay unchanged and an empty
// account reference clears it.
type SupplierUpdate struct {
	Name             *string
	AccountReference *string
}

// UpdateSupplier changes a supplier. Requires procurement.manage.
func (s *Service) UpdateSupplier(ctx context.Context, c Caller, p Principal, id string, expected int, in SupplierUpdate) (Supplier, error) {
	if err := c.validate(); err != nil {
		return Supplier{}, err
	}
	if !p.Manage {
		return Supplier{}, ErrForbidden
	}
	var name string
	if in.Name != nil {
		n, err := cleanName(*in.Name)
		if err != nil {
			return Supplier{}, err
		}
		name = n
	}
	var account *string
	if in.AccountReference != nil {
		a, err := cleanAccount(*in.AccountReference)
		if err != nil {
			return Supplier{}, err
		}
		account = a
	}
	var out Supplier
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockSupplierTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Version != expected {
			return ErrVersionConflict
		}
		next := cur
		var changed []string
		if in.Name != nil && name != cur.Name {
			next.Name = name
			changed = append(changed, "name")
		}
		if in.AccountReference != nil {
			next.AccountReference = account
			changed = append(changed, "accountReference")
		}
		if len(changed) == 0 {
			out = cur
			return nil
		}
		out, err = s.store.UpdateSupplierTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "procurement.supplier.updated", "supplier", id, supplierState(&cur), supplierState(&out), map[string]any{"changedFields": changed})
	})
	return out, err
}

// SetSupplierActive activates or deactivates a supplier (explicit operation).
// Requires procurement.manage.
func (s *Service) SetSupplierActive(ctx context.Context, c Caller, p Principal, id string, expected *int, active bool) (Supplier, error) {
	if err := c.validate(); err != nil {
		return Supplier{}, err
	}
	if !p.Manage {
		return Supplier{}, ErrForbidden
	}
	action := "procurement.supplier.deactivated"
	if active {
		action = "procurement.supplier.activated"
	}
	var out Supplier
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockSupplierTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if expected != nil && *expected != cur.Version {
			return ErrVersionConflict
		}
		if cur.Active == active {
			out = cur
			return nil
		}
		next := cur
		next.Active = active
		out, err = s.store.UpdateSupplierTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, action, "supplier", id, supplierState(&cur), supplierState(&out), nil)
	})
	return out, err
}

// ListSuppliers lists suppliers by name prefix. Requires procurement.view.
func (s *Service) ListSuppliers(ctx context.Context, p Principal, prefix string, includeInactive bool, page Page) (Result[Supplier], error) {
	if !p.canView() {
		return Result[Supplier]{}, ErrForbidden
	}
	return s.store.ListSuppliers(ctx, strings.TrimSpace(prefix), includeInactive, page.Normalize())
}

// GetSupplier returns one supplier. Requires procurement.view.
func (s *Service) GetSupplier(ctx context.Context, p Principal, id string) (Supplier, error) {
	if !p.canView() {
		return Supplier{}, ErrForbidden
	}
	return s.store.GetSupplier(ctx, id)
}

// ---- needs (Procurement Requests) ----

func needState(n *Need) any {
	if n == nil {
		return nil
	}
	return map[string]any{"status": n.Status, "productId": n.ProductID, "quantity": n.Quantity, "version": n.Version}
}

// NewNeed describes an acquisition need.
type NewNeed struct {
	ProductID   string
	Quantity    int
	ContextType string
	ContextID   string
	Notes       string
}

// CreateNeed records an acquisition need. Requires procurement.manage.
func (s *Service) CreateNeed(ctx context.Context, c Caller, p Principal, in NewNeed) (Need, error) {
	if err := c.validate(); err != nil {
		return Need{}, err
	}
	if !p.Manage {
		return Need{}, ErrForbidden
	}
	if err := checkQuantity(in.Quantity); err != nil {
		return Need{}, err
	}
	n := Need{ProductID: in.ProductID, Quantity: in.Quantity, RequestedBy: userPtr(c)}
	if in.ContextType != "" || in.ContextID != "" {
		if len(in.ContextID) != 36 || !regexp.MustCompile(`^[a-z][a-z_]{1,39}$`).MatchString(in.ContextType) {
			return Need{}, invalid("an origin needs a lower-case type and an id")
		}
		n.ContextType, n.ContextID = &in.ContextType, &in.ContextID
	}
	notes, err := cleanNotes(in.Notes)
	if err != nil {
		return Need{}, err
	}
	n.Notes = strPtr(notes)
	found, err := s.products.Products(ctx, []string{in.ProductID})
	if err != nil {
		return Need{}, fmt.Errorf("check product: %w", err)
	}
	if pr, ok := found[in.ProductID]; !ok || !pr.Active {
		return Need{}, ErrProductInvalid
	}
	var out Need
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		out, err = s.store.InsertNeedTx(ctx, tx, n)
		if err != nil {
			return err
		}
		meta := map[string]any{}
		if out.ContextType != nil {
			meta["originType"], meta["originId"] = *out.ContextType, *out.ContextID
		}
		return recordAudit(ctx, tx, c, "procurement.request.created", "procurement_request", out.ID, nil, needState(&out), meta)
	})
	return out, err
}

// CancelNeed cancels an open need with a reason. Requires procurement.manage.
func (s *Service) CancelNeed(ctx context.Context, c Caller, p Principal, id string, expected *int, reason string) (Need, error) {
	if err := c.validate(); err != nil {
		return Need{}, err
	}
	if !p.Manage {
		return Need{}, ErrForbidden
	}
	reason, err := cleanReason(reason, true)
	if err != nil {
		return Need{}, err
	}
	var out Need
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockNeedTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if expected != nil && *expected != cur.Version {
			return ErrVersionConflict
		}
		if cur.Status != NeedOpen {
			return &InvalidTransitionError{Operation: "cancel", From: cur.Status}
		}
		next := cur
		next.Status, next.StatusReason = NeedCancelled, &reason
		out, err = s.store.UpdateNeedTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "procurement.request.cancelled", "procurement_request", id, needState(&cur), needState(&out), map[string]any{"reason": reason})
	})
	return out, err
}

// ListNeeds lists procurement requests. Requires procurement.view.
func (s *Service) ListNeeds(ctx context.Context, p Principal, f NeedFilter) (Result[Need], error) {
	if !p.canView() {
		return Result[Need]{}, ErrForbidden
	}
	f.Page = f.Page.Normalize()
	return s.store.ListNeeds(ctx, f)
}

// GetNeed returns one procurement request. Requires procurement.view.
func (s *Service) GetNeed(ctx context.Context, p Principal, id string) (Need, error) {
	if !p.canView() {
		return Need{}, ErrForbidden
	}
	return s.store.GetNeed(ctx, id)
}

// NeedsByIDs returns id -> procurement request for the existing requests among
// ids (at most MaxLookupIDs). It performs no permission check: it is the
// contract other modules use after authorizing their own caller (Planning
// validates and shows the procurement requests an Initiative includes).
func (s *Service) NeedsByIDs(ctx context.Context, ids []string) (map[string]Need, error) {
	if len(ids) > MaxLookupIDs {
		return nil, invalid("at most %d procurement requests can be looked up at once", MaxLookupIDs)
	}
	norm := make([]string, 0, len(ids))
	for _, id := range ids {
		norm = append(norm, strings.ToLower(id))
	}
	found, err := s.store.NeedsByIDs(ctx, norm)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Need, len(found))
	for _, n := range found {
		out[n.ID] = n
	}
	return out, nil
}

// Names are the display names that make ids in lists readable.
type Names struct {
	Products  map[string]string
	Suppliers map[string]string
}

// NamesFor resolves product and supplier names. It performs no permission
// check: callers use it for ids they already returned.
func (s *Service) NamesFor(ctx context.Context, productIDs, supplierIDs []string) (Names, error) {
	out := Names{Products: map[string]string{}, Suppliers: map[string]string{}}
	if len(productIDs) > 0 {
		found, err := s.products.Products(ctx, productIDs)
		if err != nil {
			return Names{}, fmt.Errorf("load product names: %w", err)
		}
		for id, p := range found {
			out.Products[id] = p.Name
		}
	}
	if len(supplierIDs) > 0 {
		names, err := s.store.SupplierNames(ctx, supplierIDs)
		if err != nil {
			return Names{}, err
		}
		out.Suppliers = names
	}
	return out, nil
}
