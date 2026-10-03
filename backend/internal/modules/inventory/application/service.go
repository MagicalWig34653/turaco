package application

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Service performs Inventory operations. Audit actions: inventory.warehouse.*,
// inventory.storage_location.*, inventory.stock.<type> (one event per
// operation, not per ledger row), inventory.reservation.*. Free text beyond
// reasons is never copied into the audit log.
type Service struct {
	store    Store
	dir      Directory
	products Products
	assets   Assets
}

func NewService(store Store, dir Directory, products Products, assets Assets) *Service {
	return &Service{store: store, dir: dir, products: products, assets: assets}
}

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

func cleanReason(s string, required bool) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" && !required {
		return "", nil
	}
	if s == "" || utf8.RuneCountInString(s) > maxReason || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, false) {
		if required {
			return "", invalid("a reason of 1-%d characters without control or invisible formatting characters is required", maxReason)
		}
		return "", invalid("reason must be at most %d characters without control or invisible formatting characters", maxReason)
	}
	return s, nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (o Origin) check() (*string, *string, error) {
	if o.Type == "" && o.ID == "" {
		return nil, nil, nil
	}
	if len(o.ID) != 36 || len(o.Type) < 2 || len(o.Type) > 40 {
		return nil, nil, invalid("an origin needs a type and an id")
	}
	for i, r := range o.Type {
		if !(r >= 'a' && r <= 'z' || (i > 0 && r == '_')) {
			return nil, nil, invalid("origin type must consist of lower-case letters and underscores")
		}
	}
	return &o.Type, &o.ID, nil
}

// ---- warehouses ----

func warehouseState(w *Warehouse) any {
	if w == nil {
		return nil
	}
	return map[string]any{"active": w.Active, "locationId": w.LocationID, "version": w.Version}
}

// CreateWarehouse creates a warehouse, optionally inside an Organization
// Location. Requires inventory.manage.
func (s *Service) CreateWarehouse(ctx context.Context, c Caller, p Principal, name string, locationID *string) (Warehouse, error) {
	if err := c.validate(); err != nil {
		return Warehouse{}, err
	}
	if !p.Manage {
		return Warehouse{}, ErrForbidden
	}
	name, err := cleanName(name)
	if err != nil {
		return Warehouse{}, err
	}
	if locationID != nil {
		ok, err := s.dir.ActiveLocations(ctx, []string{*locationID})
		if err != nil {
			return Warehouse{}, fmt.Errorf("check location: %w", err)
		}
		if !ok[*locationID] {
			return Warehouse{}, ErrReferenceInvalid
		}
	}
	var out Warehouse
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		w, err := s.store.InsertWarehouseTx(ctx, tx, name, locationID)
		if err != nil {
			return err
		}
		out = w
		return recordAudit(ctx, tx, c, "inventory.warehouse.created", "warehouse", w.ID, nil, warehouseState(&w), nil)
	})
	return out, err
}

// WarehouseUpdate changes a warehouse; nil fields stay unchanged.
type WarehouseUpdate struct {
	Name          *string
	LocationID    *string
	ClearLocation bool
}

// UpdateWarehouse renames a warehouse or changes its Location. Requires inventory.manage.
func (s *Service) UpdateWarehouse(ctx context.Context, c Caller, p Principal, id string, expected int, in WarehouseUpdate) (Warehouse, error) {
	if err := c.validate(); err != nil {
		return Warehouse{}, err
	}
	if !p.Manage {
		return Warehouse{}, ErrForbidden
	}
	if in.LocationID != nil {
		ok, err := s.dir.ActiveLocations(ctx, []string{*in.LocationID})
		if err != nil {
			return Warehouse{}, fmt.Errorf("check location: %w", err)
		}
		if !ok[*in.LocationID] {
			return Warehouse{}, ErrReferenceInvalid
		}
	}
	var name string
	if in.Name != nil {
		n, err := cleanName(*in.Name)
		if err != nil {
			return Warehouse{}, err
		}
		name = n
	}
	var out Warehouse
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockWarehouseTx(ctx, tx, id)
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
		switch {
		case in.ClearLocation && cur.LocationID != nil:
			next.LocationID = nil
			changed = append(changed, "location")
		case in.LocationID != nil && (cur.LocationID == nil || *cur.LocationID != *in.LocationID):
			next.LocationID = in.LocationID
			changed = append(changed, "location")
		}
		if len(changed) == 0 {
			out = cur
			return nil
		}
		out, err = s.store.UpdateWarehouseTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "inventory.warehouse.updated", "warehouse", id, warehouseState(&cur), warehouseState(&out), map[string]any{"changedFields": changed})
	})
	return out, err
}

// SetWarehouseActive activates or deactivates a warehouse. Stock may remain
// in an inactive warehouse and be drained, but nothing new can be put in.
// Requires inventory.manage.
func (s *Service) SetWarehouseActive(ctx context.Context, c Caller, p Principal, id string, expected *int, active bool) (Warehouse, error) {
	if err := c.validate(); err != nil {
		return Warehouse{}, err
	}
	if !p.Manage {
		return Warehouse{}, ErrForbidden
	}
	action := "inventory.warehouse.deactivated"
	if active {
		action = "inventory.warehouse.activated"
	}
	var out Warehouse
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockWarehouseTx(ctx, tx, id)
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
		out, err = s.store.UpdateWarehouseTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, action, "warehouse", id, warehouseState(&cur), warehouseState(&out), nil)
	})
	return out, err
}

// ListWarehouses lists warehouses. Requires inventory.view.
func (s *Service) ListWarehouses(ctx context.Context, p Principal, includeInactive bool, page Page) (Result[Warehouse], error) {
	if !p.canView() {
		return Result[Warehouse]{}, ErrForbidden
	}
	return s.store.ListWarehouses(ctx, includeInactive, page.Normalize())
}

// GetWarehouse returns one warehouse. Requires inventory.view.
func (s *Service) GetWarehouse(ctx context.Context, p Principal, id string) (Warehouse, error) {
	if !p.canView() {
		return Warehouse{}, ErrForbidden
	}
	return s.store.GetWarehouse(ctx, id)
}

// ---- storage locations ----

func locationState(l *StorageLocation) any {
	if l == nil {
		return nil
	}
	return map[string]any{"warehouseId": l.WarehouseID, "active": l.Active, "version": l.Version}
}

// CreateStorageLocation creates a storage location in an active warehouse.
// Requires inventory.manage.
func (s *Service) CreateStorageLocation(ctx context.Context, c Caller, p Principal, warehouseID, name string) (StorageLocation, error) {
	if err := c.validate(); err != nil {
		return StorageLocation{}, err
	}
	if !p.Manage {
		return StorageLocation{}, ErrForbidden
	}
	name, err := cleanName(name)
	if err != nil {
		return StorageLocation{}, err
	}
	var out StorageLocation
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		w, err := s.store.LockWarehouseTx(ctx, tx, warehouseID)
		if err != nil {
			return err
		}
		if !w.Active {
			return ErrLocationInactive
		}
		l, err := s.store.InsertLocationTx(ctx, tx, warehouseID, name)
		if err != nil {
			return err
		}
		out = l
		return recordAudit(ctx, tx, c, "inventory.storage_location.created", "storage_location", l.ID, nil, locationState(&l), nil)
	})
	return out, err
}

// RenameStorageLocation renames a storage location. Requires inventory.manage.
func (s *Service) RenameStorageLocation(ctx context.Context, c Caller, p Principal, id string, expected int, name string) (StorageLocation, error) {
	if err := c.validate(); err != nil {
		return StorageLocation{}, err
	}
	if !p.Manage {
		return StorageLocation{}, ErrForbidden
	}
	name, err := cleanName(name)
	if err != nil {
		return StorageLocation{}, err
	}
	var out StorageLocation
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockLocationTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Version != expected {
			return ErrVersionConflict
		}
		if cur.Name == name {
			out = cur
			return nil
		}
		next := cur
		next.Name = name
		out, err = s.store.UpdateLocationTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "inventory.storage_location.renamed", "storage_location", id, locationState(&cur), locationState(&out), nil)
	})
	return out, err
}

// SetStorageLocationActive activates or deactivates a storage location.
// Requires inventory.manage.
func (s *Service) SetStorageLocationActive(ctx context.Context, c Caller, p Principal, id string, expected *int, active bool) (StorageLocation, error) {
	if err := c.validate(); err != nil {
		return StorageLocation{}, err
	}
	if !p.Manage {
		return StorageLocation{}, ErrForbidden
	}
	action := "inventory.storage_location.deactivated"
	if active {
		action = "inventory.storage_location.activated"
	}
	var out StorageLocation
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockLocationTx(ctx, tx, id)
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
		out, err = s.store.UpdateLocationTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, action, "storage_location", id, locationState(&cur), locationState(&out), nil)
	})
	return out, err
}

// ListStorageLocations lists the storage locations of a warehouse. Requires inventory.view.
func (s *Service) ListStorageLocations(ctx context.Context, p Principal, warehouseID string, includeInactive bool, page Page) (Result[StorageLocation], error) {
	if !p.canView() {
		return Result[StorageLocation]{}, ErrForbidden
	}
	return s.store.ListLocations(ctx, warehouseID, includeInactive, page.Normalize())
}

// ---- reads ----

// ListStock lists balances. Requires inventory.view.
func (s *Service) ListStock(ctx context.Context, p Principal, f StockFilter) (Result[Balance], error) {
	if !p.canView() {
		return Result[Balance]{}, ErrForbidden
	}
	f.Page = f.Page.Normalize()
	return s.store.ListStock(ctx, f)
}

// ListTransactions lists the ledger, newest first. Requires inventory.view.
func (s *Service) ListTransactions(ctx context.Context, p Principal, f TransactionFilter) (Result[Transaction], error) {
	if !p.canView() {
		return Result[Transaction]{}, ErrForbidden
	}
	f.Page = f.Page.Normalize()
	return s.store.ListTransactions(ctx, f)
}

// ListReservations lists reservations. Requires inventory.view.
func (s *Service) ListReservations(ctx context.Context, p Principal, f ReservationFilter) (Result[Reservation], error) {
	if !p.canView() {
		return Result[Reservation]{}, ErrForbidden
	}
	f.Page = f.Page.Normalize()
	return s.store.ListReservations(ctx, f)
}

// GetReservation returns one reservation. Requires inventory.view.
func (s *Service) GetReservation(ctx context.Context, p Principal, id string) (Reservation, error) {
	if !p.canView() {
		return Reservation{}, ErrForbidden
	}
	return s.store.GetReservation(ctx, id)
}
