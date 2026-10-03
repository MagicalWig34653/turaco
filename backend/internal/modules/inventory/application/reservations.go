package application

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func reservationState(r *Reservation) any {
	if r == nil {
		return nil
	}
	return map[string]any{"kind": r.Kind, "status": r.Status, "productId": r.ProductID, "quantity": r.Quantity, "assetId": r.AssetID, "version": r.Version}
}

func reservationMeta(r Reservation) map[string]any {
	meta := map[string]any{"kind": r.Kind, "productId": r.ProductID}
	if r.Quantity != nil {
		meta["quantity"] = *r.Quantity
	}
	if r.AssetID != nil {
		meta["assetId"] = *r.AssetID
	}
	if r.ContextType != nil {
		meta["originType"], meta["originId"] = *r.ContextType, *r.ContextID
	}
	return meta
}

func reservationEvent(r Reservation) map[string]any {
	e := map[string]any{"reservationId": r.ID, "kind": r.Kind, "productId": r.ProductID, "status": r.Status}
	if r.Quantity != nil {
		e["quantity"] = *r.Quantity
	}
	if r.AssetID != nil {
		e["assetId"] = *r.AssetID
	}
	if r.ContextType != nil {
		e["contextType"], e["contextId"] = *r.ContextType, *r.ContextID
	}
	return e
}

// QuantityReservation reserves stock at one Storage Location.
type QuantityReservation struct {
	ProductID         string
	StorageLocationID string
	Quantity          int
	Origin            Origin
}

// ReserveQuantity reserves available stock. It never over-reserves: the
// balance row is updated only while available >= quantity, so concurrent
// reservations cannot oversell. Requires inventory.manage.
func (s *Service) ReserveQuantity(ctx context.Context, c Caller, p Principal, in QuantityReservation) (Reservation, error) {
	if err := c.validate(); err != nil {
		return Reservation{}, err
	}
	if !p.Manage {
		return Reservation{}, ErrForbidden
	}
	if err := checkQuantity(in.Quantity); err != nil {
		return Reservation{}, err
	}
	ct, ci, err := in.Origin.check()
	if err != nil {
		return Reservation{}, err
	}
	if err := s.stockProduct(ctx, in.ProductID, true); err != nil {
		return Reservation{}, err
	}
	var out Reservation
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		if err := s.locationActive(ctx, tx, in.StorageLocationID); err != nil {
			return err
		}
		ok, err := s.store.ReserveStockTx(ctx, tx, in.ProductID, in.StorageLocationID, in.Quantity)
		if err != nil {
			return err
		}
		if !ok {
			return ErrInsufficientStock
		}
		loc, qty := in.StorageLocationID, in.Quantity
		out, err = s.store.InsertReservationTx(ctx, tx, NewReservation{
			Kind: KindQuantity, ProductID: in.ProductID, StorageLocationID: &loc, Quantity: &qty,
			ContextType: ct, ContextID: ci, CreatedBy: userPtr(c),
		})
		if err != nil {
			return err
		}
		if _, err := s.ledgerRow(ctx, tx, c, Transaction{Type: TxReservation, ProductID: in.ProductID, StorageLocationID: in.StorageLocationID,
			ReservedDelta: in.Quantity, ReservationID: &out.ID, ContextType: ct, ContextID: ci}); err != nil {
			return err
		}
		if err := recordAudit(ctx, tx, c, "inventory.reservation.created", "reservation", out.ID, nil, reservationState(&out), reservationMeta(out)); err != nil {
			return err
		}
		return publish(ctx, tx, c, "StockReserved", reservationEvent(out))
	})
	return out, err
}

func userPtr(c Caller) *string {
	if c.Actor.UserID == "" {
		return nil
	}
	u := c.Actor.UserID
	return &u
}

// AssetReservation reserves one serialized Asset.
type AssetReservation struct {
	AssetID string
	Origin  Origin
}

// ReserveAsset reserves an available Asset: the Asset moves to `reserved` and
// at most one active reservation per Asset exists (database enforced).
// Requires inventory.manage.
func (s *Service) ReserveAsset(ctx context.Context, c Caller, p Principal, in AssetReservation) (Reservation, error) {
	if err := c.validate(); err != nil {
		return Reservation{}, err
	}
	if !p.Manage {
		return Reservation{}, ErrForbidden
	}
	ct, ci, err := in.Origin.check()
	if err != nil {
		return Reservation{}, err
	}
	found, err := s.assets.Assets(ctx, []string{in.AssetID})
	if err != nil {
		return Reservation{}, fmt.Errorf("load asset: %w", err)
	}
	asset, ok := found[in.AssetID]
	if !ok {
		return Reservation{}, ErrNotFound
	}
	var out Reservation
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		// The adapter reports an asset that is not available as ErrAssetUnavailable.
		if err := s.assets.ReserveInTx(ctx, tx, c.Actor, c.CorrelationID, asset.ID); err != nil {
			return err
		}
		id := asset.ID
		out, err = s.store.InsertReservationTx(ctx, tx, NewReservation{
			Kind: KindAsset, ProductID: asset.ProductID, AssetID: &id, ContextType: ct, ContextID: ci, CreatedBy: userPtr(c),
		})
		if err != nil {
			return err
		}
		if err := recordAudit(ctx, tx, c, "inventory.reservation.created", "reservation", out.ID, nil, reservationState(&out), reservationMeta(out)); err != nil {
			return err
		}
		return publish(ctx, tx, c, "StockReserved", reservationEvent(out))
	})
	return out, err
}

// Release gives a reservation up: reserved stock becomes available again, a
// reserved Asset becomes available. Requires inventory.manage.
func (s *Service) Release(ctx context.Context, c Caller, p Principal, id string, expected *int, reason string) (Reservation, error) {
	return s.closeReservation(ctx, c, p, id, expected, "release", ResReleased, reason, nil)
}

// Fulfill completes a reservation: reserved stock is issued, a reserved Asset
// is assigned to the given User, Team or Location. Requires inventory.manage.
func (s *Service) Fulfill(ctx context.Context, c Caller, p Principal, id string, expected *int, assignee *AssetAssignee, note string) (Reservation, error) {
	return s.closeReservation(ctx, c, p, id, expected, "fulfill", ResFulfilled, note, assignee)
}

func (s *Service) closeReservation(ctx context.Context, c Caller, p Principal, id string, expected *int, op, status, text string, assignee *AssetAssignee) (Reservation, error) {
	if err := c.validate(); err != nil {
		return Reservation{}, err
	}
	if !p.Manage {
		return Reservation{}, ErrForbidden
	}
	reason, err := cleanReason(text, false)
	if err != nil {
		return Reservation{}, err
	}
	var out Reservation
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockReservationTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if expected != nil && *expected != cur.Version {
			return ErrVersionConflict
		}
		if cur.Status != ResActive {
			return &InvalidTransitionError{Operation: op, From: cur.Status}
		}
		switch {
		case cur.Kind == KindQuantity && status == ResReleased:
			ok, err := s.store.UnreserveStockTx(ctx, tx, cur.ProductID, *cur.StorageLocationID, *cur.Quantity)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("inventory: reserved quantity of reservation %s is missing from the balance", cur.ID)
			}
			_, err = s.ledgerRow(ctx, tx, c, Transaction{Type: TxRelease, ProductID: cur.ProductID, StorageLocationID: *cur.StorageLocationID,
				ReservedDelta: -*cur.Quantity, ReservationID: &cur.ID, ContextType: cur.ContextType, ContextID: cur.ContextID, Reason: strPtr(reason)})
			if err != nil {
				return err
			}
		case cur.Kind == KindQuantity:
			ok, err := s.store.IssueReservedStockTx(ctx, tx, cur.ProductID, *cur.StorageLocationID, *cur.Quantity)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("inventory: reserved quantity of reservation %s is missing from the balance", cur.ID)
			}
			_, err = s.ledgerRow(ctx, tx, c, Transaction{Type: TxIssue, ProductID: cur.ProductID, StorageLocationID: *cur.StorageLocationID,
				OnHandDelta: -*cur.Quantity, ReservedDelta: -*cur.Quantity, ReservationID: &cur.ID,
				ContextType: cur.ContextType, ContextID: cur.ContextID, Reason: strPtr(reason)})
			if err != nil {
				return err
			}
		case status == ResReleased:
			if err := s.assets.ReleaseReservationInTx(ctx, tx, c.Actor, c.CorrelationID, *cur.AssetID); err != nil {
				return err
			}
		default:
			if assignee == nil {
				return invalid("an assignee is required to fulfill an asset reservation")
			}
			if err := s.checkAssignee(ctx, *assignee); err != nil {
				return err
			}
			if err := s.assets.AssignReservedInTx(ctx, tx, c.Actor, c.CorrelationID, *cur.AssetID, *assignee, reason); err != nil {
				return err
			}
		}
		var closeReason *string = strPtr(reason)
		out, err = s.store.CloseReservationTx(ctx, tx, cur.ID, status, closeReason)
		if err != nil {
			return err
		}
		meta := reservationMeta(out)
		if reason != "" {
			meta["reason"] = reason
		}
		if err := recordAudit(ctx, tx, c, "inventory.reservation."+status, "reservation", cur.ID, reservationState(&cur), reservationState(&out), meta); err != nil {
			return err
		}
		event := "ReservationReleased"
		if status == ResFulfilled {
			event = "ReservationFulfilled"
		}
		return publish(ctx, tx, c, event, reservationEvent(out))
	})
	return out, err
}

func (s *Service) checkAssignee(ctx context.Context, a AssetAssignee) error {
	var active map[string]bool
	var err error
	switch a.Type {
	case "user":
		active, err = s.dir.ActiveUsers(ctx, []string{a.ID})
	case "team":
		active, err = s.dir.ActiveTeams(ctx, []string{a.ID})
	case "location":
		active, err = s.dir.ActiveLocations(ctx, []string{a.ID})
	default:
		return invalid("assignee type must be user, team or location")
	}
	if err != nil {
		return fmt.Errorf("check assignee: %w", err)
	}
	if !active[a.ID] {
		return ErrAssigneeInvalid
	}
	return nil
}
