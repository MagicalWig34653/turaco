package application

import (
	"context"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
)

// StockMove describes a movement of one Product at one Storage Location.
type StockMove struct {
	ProductID         string
	StorageLocationID string
	Quantity          int
	Reason            string
	Origin            Origin
}

// TransferMove moves stock between two Storage Locations.
type TransferMove struct {
	ProductID string
	FromID    string
	ToID      string
	Quantity  int
	Reason    string
	Origin    Origin
}

// Correction changes a balance by a signed amount after a count or an error.
type Correction struct {
	ProductID         string
	StorageLocationID string
	Delta             int
	Reason            string
	Origin            Origin
}

// stockProduct checks that the Product is tracked by quantity. Adding stock
// also needs it to be active; taking stock out of an inactive product is fine.
func (s *Service) stockProduct(ctx context.Context, id string, adding bool) error {
	found, err := s.products.Products(ctx, []string{id})
	if err != nil {
		return fmt.Errorf("check product: %w", err)
	}
	p, ok := found[id]
	if !ok || !p.StockManaged || p.Serialized || (adding && !p.Active) {
		return ErrProductInvalid
	}
	return nil
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// checkIDs refuses ids that are not UUIDs before they reach SQL casts.
func checkIDs(ids ...string) error {
	for _, id := range ids {
		if !uuidPattern.MatchString(id) {
			return invalid("ids must be UUIDs")
		}
	}
	return nil
}

func checkQuantity(q int) error {
	if q < 1 || q > MaxQuantity {
		return invalid("quantity must be between 1 and %d", MaxQuantity)
	}
	return nil
}

// ledgerRow appends a ledger row inside tx.
func (s *Service) ledgerRow(ctx context.Context, tx pgx.Tx, c Caller, t Transaction) (Transaction, error) {
	t.CorrelationID = c.CorrelationID
	if c.Actor.UserID != "" {
		u := c.Actor.UserID
		t.ActorUserID = &u
	}
	return s.store.InsertTransactionTx(ctx, tx, t)
}

func stockMeta(typ string, rows []Transaction, reason string, origin Origin) map[string]any {
	locations := make([]string, 0, len(rows))
	for _, r := range rows {
		locations = append(locations, r.StorageLocationID)
	}
	meta := map[string]any{"type": typ, "productId": rows[0].ProductID, "storageLocationIds": locations, "groupId": rows[0].GroupID}
	deltas := make([]int, 0, len(rows))
	for _, r := range rows {
		deltas = append(deltas, r.OnHandDelta)
	}
	meta["onHandDeltas"] = deltas
	if reason != "" {
		meta["reason"] = reason
	}
	if origin.Type != "" {
		meta["originType"], meta["originId"] = origin.Type, origin.ID
	}
	return meta
}

// stockOp runs one stock operation: it validates, runs body in a transaction
// that appends the ledger rows and changes the balances, and writes one audit
// event for the whole operation.
func (s *Service) stockOp(ctx context.Context, c Caller, typ, reason string, origin Origin, body func(tx pgx.Tx, ctxType, ctxID *string) ([]Transaction, error)) ([]Transaction, error) {
	ct, ci, err := origin.check()
	if err != nil {
		return nil, err
	}
	var rows []Transaction
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		rows, err = body(tx, ct, ci)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "inventory.stock."+typ, "product", rows[0].ProductID, nil, nil, stockMeta(typ, rows, reason, origin))
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *Service) locationActive(ctx context.Context, tx pgx.Tx, ids ...string) error {
	active, err := s.store.ActiveLocationsTx(ctx, tx, ids)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if !active[id] {
			return ErrLocationInactive
		}
	}
	return nil
}

// ReceiveInTx puts received goods into stock in the caller's transaction (the
// contract Goods Receipt uses). It performs no permission check: the caller
// has authorized the receipt.
func (s *Service) ReceiveInTx(ctx context.Context, tx pgx.Tx, c Caller, in StockMove) (Transaction, error) {
	if err := s.stockProduct(ctx, in.ProductID, true); err != nil {
		return Transaction{}, err
	}
	return s.receiveChecked(ctx, tx, c, in)
}

// receiveChecked books received goods for a product the caller already checked
// (no read through the pool while the caller's transaction is open).
func (s *Service) receiveChecked(ctx context.Context, tx pgx.Tx, c Caller, in StockMove) (Transaction, error) {
	if err := c.validate(); err != nil {
		return Transaction{}, err
	}
	if err := checkQuantity(in.Quantity); err != nil {
		return Transaction{}, err
	}
	if err := checkIDs(in.ProductID, in.StorageLocationID); err != nil {
		return Transaction{}, err
	}
	ct, ci, err := in.Origin.check()
	if err != nil {
		return Transaction{}, err
	}
	if err := s.locationActive(ctx, tx, in.StorageLocationID); err != nil {
		return Transaction{}, err
	}
	if err := s.store.AddStockTx(ctx, tx, in.ProductID, in.StorageLocationID, in.Quantity); err != nil {
		return Transaction{}, err
	}
	row, err := s.ledgerRow(ctx, tx, c, Transaction{
		Type: TxGoodsReceipt, ProductID: in.ProductID, StorageLocationID: in.StorageLocationID, OnHandDelta: in.Quantity,
		ContextType: ct, ContextID: ci,
	})
	if err != nil {
		return Transaction{}, err
	}
	return row, recordAudit(ctx, tx, c, "inventory.stock.goods_receipt", "product", in.ProductID, nil, nil, stockMeta(TxGoodsReceipt, []Transaction{row}, "", in.Origin))
}

// Return puts previously issued stock back. Requires inventory.manage.
func (s *Service) Return(ctx context.Context, c Caller, p Principal, in StockMove) ([]Transaction, error) {
	reason, err := s.prepare(c, p, in.Quantity, in.Reason, true)
	if err != nil {
		return nil, err
	}
	if err := checkIDs(in.ProductID, in.StorageLocationID); err != nil {
		return nil, err
	}
	if err := s.stockProduct(ctx, in.ProductID, true); err != nil {
		return nil, err
	}
	return s.stockOp(ctx, c, TxReturn, reason, in.Origin, func(tx pgx.Tx, ct, ci *string) ([]Transaction, error) {
		if err := s.locationActive(ctx, tx, in.StorageLocationID); err != nil {
			return nil, err
		}
		if err := s.store.AddStockTx(ctx, tx, in.ProductID, in.StorageLocationID, in.Quantity); err != nil {
			return nil, err
		}
		row, err := s.ledgerRow(ctx, tx, c, Transaction{Type: TxReturn, ProductID: in.ProductID, StorageLocationID: in.StorageLocationID,
			OnHandDelta: in.Quantity, ContextType: ct, ContextID: ci, Reason: strPtr(reason)})
		return []Transaction{row}, err
	})
}

func (s *Service) prepare(c Caller, p Principal, quantity int, reason string, reasonRequired bool) (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	if !p.Manage {
		return "", ErrForbidden
	}
	if err := checkQuantity(quantity); err != nil {
		return "", err
	}
	return cleanReason(reason, reasonRequired)
}

// Issue takes stock out for use (handing it over). It needs available stock:
// reserved stock can only leave through its reservation. Requires inventory.manage.
func (s *Service) Issue(ctx context.Context, c Caller, p Principal, in StockMove) ([]Transaction, error) {
	return s.takeOut(ctx, c, p, TxIssue, in, false)
}

// Dispose removes stock that is broken or unusable. A reason is required.
// Requires inventory.manage.
func (s *Service) Dispose(ctx context.Context, c Caller, p Principal, in StockMove) ([]Transaction, error) {
	return s.takeOut(ctx, c, p, TxDisposal, in, true)
}

func (s *Service) takeOut(ctx context.Context, c Caller, p Principal, typ string, in StockMove, reasonRequired bool) ([]Transaction, error) {
	reason, err := s.prepare(c, p, in.Quantity, in.Reason, reasonRequired)
	if err != nil {
		return nil, err
	}
	if err := checkIDs(in.ProductID, in.StorageLocationID); err != nil {
		return nil, err
	}
	if err := s.stockProduct(ctx, in.ProductID, false); err != nil {
		return nil, err
	}
	return s.stockOp(ctx, c, typ, reason, in.Origin, func(tx pgx.Tx, ct, ci *string) ([]Transaction, error) {
		ok, err := s.store.RemoveStockTx(ctx, tx, in.ProductID, in.StorageLocationID, in.Quantity)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrInsufficientStock
		}
		row, err := s.ledgerRow(ctx, tx, c, Transaction{Type: typ, ProductID: in.ProductID, StorageLocationID: in.StorageLocationID,
			OnHandDelta: -in.Quantity, ContextType: ct, ContextID: ci, Reason: strPtr(reason)})
		return []Transaction{row}, err
	})
}

// Transfer moves available stock between two Storage Locations as two ledger
// rows of one group. Requires inventory.manage.
func (s *Service) Transfer(ctx context.Context, c Caller, p Principal, in TransferMove) ([]Transaction, error) {
	reason, err := s.prepare(c, p, in.Quantity, in.Reason, false)
	if err != nil {
		return nil, err
	}
	if err := checkIDs(in.ProductID, in.FromID, in.ToID); err != nil {
		return nil, err
	}
	if in.FromID == in.ToID {
		return nil, invalid("source and destination must differ")
	}
	if err := s.stockProduct(ctx, in.ProductID, true); err != nil {
		return nil, err
	}
	return s.stockOp(ctx, c, TxTransfer, reason, in.Origin, func(tx pgx.Tx, ct, ci *string) ([]Transaction, error) {
		if err := s.locationActive(ctx, tx, in.ToID); err != nil {
			return nil, err
		}
		// Both rows are locked in a fixed order first, so opposite transfers cannot deadlock.
		if err := s.store.LockBalancesTx(ctx, tx, in.ProductID, []string{in.FromID, in.ToID}); err != nil {
			return nil, err
		}
		ok, err := s.store.RemoveStockTx(ctx, tx, in.ProductID, in.FromID, in.Quantity)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrInsufficientStock
		}
		if err := s.store.AddStockTx(ctx, tx, in.ProductID, in.ToID, in.Quantity); err != nil {
			return nil, err
		}
		out, err := s.ledgerRow(ctx, tx, c, Transaction{Type: TxTransfer, ProductID: in.ProductID, StorageLocationID: in.FromID,
			OnHandDelta: -in.Quantity, ContextType: ct, ContextID: ci, Reason: strPtr(reason)})
		if err != nil {
			return nil, err
		}
		inRow, err := s.ledgerRow(ctx, tx, c, Transaction{Type: TxTransfer, ProductID: in.ProductID, StorageLocationID: in.ToID,
			OnHandDelta: in.Quantity, GroupID: out.GroupID, ContextType: ct, ContextID: ci, Reason: strPtr(reason)})
		return []Transaction{out, inRow}, err
	})
}

// Correct changes a balance by a signed amount (after a count, or to fix a
// mistake). A reason is required; the balance cannot drop below the reserved
// quantity. Requires inventory.manage.
func (s *Service) Correct(ctx context.Context, c Caller, p Principal, in Correction) ([]Transaction, error) {
	if in.Delta == 0 || in.Delta > MaxQuantity || in.Delta < -MaxQuantity {
		return nil, invalid("delta must be a non-zero number between -%d and %d", MaxQuantity, MaxQuantity)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	if !p.Manage {
		return nil, ErrForbidden
	}
	reason, err := cleanReason(in.Reason, true)
	if err != nil {
		return nil, err
	}
	if err := checkIDs(in.ProductID, in.StorageLocationID); err != nil {
		return nil, err
	}
	if err := s.stockProduct(ctx, in.ProductID, in.Delta > 0); err != nil {
		return nil, err
	}
	return s.stockOp(ctx, c, TxCorrection, reason, in.Origin, func(tx pgx.Tx, ct, ci *string) ([]Transaction, error) {
		if in.Delta > 0 {
			if err := s.locationActive(ctx, tx, in.StorageLocationID); err != nil {
				return nil, err
			}
		}
		ok, err := s.store.CorrectStockTx(ctx, tx, in.ProductID, in.StorageLocationID, in.Delta)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrInsufficientStock
		}
		row, err := s.ledgerRow(ctx, tx, c, Transaction{Type: TxCorrection, ProductID: in.ProductID, StorageLocationID: in.StorageLocationID,
			OnHandDelta: in.Delta, ContextType: ct, ContextID: ci, Reason: strPtr(reason)})
		return []Transaction{row}, err
	})
}
