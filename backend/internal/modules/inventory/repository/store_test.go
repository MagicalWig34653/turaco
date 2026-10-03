package repository_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/inventory/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/inventory/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

type dir struct{ active map[string]bool }

func (d dir) pick(ids []string) map[string]bool {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = d.active[id]
	}
	return out
}
func (d dir) ActiveLocations(_ context.Context, ids []string) (map[string]bool, error) {
	return d.pick(ids), nil
}
func (d dir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	return d.pick(ids), nil
}
func (d dir) ActiveTeams(_ context.Context, ids []string) (map[string]bool, error) {
	return d.pick(ids), nil
}

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

// assets is an in-memory stand-in for the Assets contract with the same state rules.
type assets struct {
	mu     sync.Mutex
	status map[string]string
	prod   map[string]string
}

func (a *assets) Assets(_ context.Context, ids []string) (map[string]application.AssetView, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]application.AssetView{}
	for _, id := range ids {
		if s, ok := a.status[id]; ok {
			out[id] = application.AssetView{ID: id, ProductID: a.prod[id], Status: s}
		}
	}
	return out, nil
}
func (a *assets) move(id, from, to string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.status[id] != from {
		return application.ErrAssetUnavailable
	}
	a.status[id] = to
	return nil
}
func (a *assets) ReserveInTx(_ context.Context, _ pgx.Tx, _ audit.Actor, _, id string) error {
	return a.move(id, "available", "reserved")
}
func (a *assets) ReleaseReservationInTx(_ context.Context, _ pgx.Tx, _ audit.Actor, _, id string) error {
	return a.move(id, "reserved", "available")
}
func (a *assets) AssignReservedInTx(_ context.Context, _ pgx.Tx, _ audit.Actor, _, id string, _ application.AssetAssignee, _ string) error {
	return a.move(id, "reserved", "assigned")
}

func (a *assets) CreateReceivedInTx(context.Context, pgx.Tx, audit.Actor, string, application.ReceivedAsset) (string, error) {
	return "", errors.New("not used in these tests")
}

type noOrders struct{}

func (noOrders) Order(context.Context, string) (application.OrderView, error) {
	return application.OrderView{}, application.ErrNotFound
}
func (noOrders) RecordReceiptInTx(context.Context, pgx.Tx, audit.Actor, string, string, []application.OrderReceipt) error {
	return nil
}

type env struct {
	t                         *testing.T
	pool                      *pgxpool.Pool
	svc                       *application.Service
	assets                    *assets
	corr                      string
	manager, stranger, holder string
	widget, laptop, dock      string
	asset                     string
	wh                        application.Warehouse
	shelfA, shelfB            application.StorageLocation
	manage, view              application.Principal
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	e := &env{t: t, pool: pool, corr: "inventory-" + hex.EncodeToString(b)}
	for _, dst := range []*string{&e.manager, &e.stranger, &e.holder, &e.widget, &e.laptop, &e.dock, &e.asset} {
		if err := pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	e.assets = &assets{status: map[string]string{e.asset: "available"}, prod: map[string]string{e.asset: e.laptop}}
	pr := products{
		e.widget: {ID: e.widget, Name: "Cable", Active: true, StockManaged: true},
		e.laptop: {ID: e.laptop, Name: "Laptop", Active: true, Serialized: true, AssetManaged: true},
		e.dock:   {ID: e.dock, Name: "Dock", Active: false, StockManaged: true},
	}
	e.svc = application.NewService(repository.New(pool), dir{active: map[string]bool{e.holder: true}}, pr, e.assets, noOrders{})
	e.manage = application.Principal{UserID: e.manager, Manage: true}
	e.view = application.Principal{UserID: e.stranger, View: true}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, e.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, e.corr)
		// The ledger is immutable by trigger; test rows are removed with triggers skipped on this connection only.
		conn, err := pool.Acquire(ctx)
		if err != nil {
			return
		}
		defer conn.Release()
		// Session-local: triggers are skipped on this connection only, never for concurrent tests.
		_, _ = conn.Exec(ctx, `SET session_replication_role = replica`)
		_, _ = conn.Exec(ctx, `DELETE FROM inventory.inventory_transactions WHERE correlation_id = $1`, e.corr)
		_, _ = conn.Exec(ctx, `RESET session_replication_role`)
		_, _ = pool.Exec(ctx, `DELETE FROM inventory.reservations WHERE product_id = ANY($1::uuid[])`, []string{e.widget, e.laptop, e.dock})
		_, _ = pool.Exec(ctx, `DELETE FROM inventory.stock_balances WHERE product_id = ANY($1::uuid[])`, []string{e.widget, e.laptop, e.dock})
		if e.wh.ID != "" {
			_, _ = pool.Exec(ctx, `DELETE FROM inventory.storage_locations WHERE warehouse_id = $1::uuid`, e.wh.ID)
			_, _ = pool.Exec(ctx, `DELETE FROM inventory.warehouses WHERE id = $1::uuid`, e.wh.ID)
		}
	})
	var err error
	if e.wh, err = e.svc.CreateWarehouse(context.Background(), e.caller(e.manager), e.manage, "WH "+e.corr, nil); err != nil {
		t.Fatal(err)
	}
	if e.shelfA, err = e.svc.CreateStorageLocation(context.Background(), e.caller(e.manager), e.manage, e.wh.ID, "A"); err != nil {
		t.Fatal(err)
	}
	if e.shelfB, err = e.svc.CreateStorageLocation(context.Background(), e.caller(e.manager), e.manage, e.wh.ID, "B"); err != nil {
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

// receive books received goods like a goods receipt does.
func (e *env) receive(loc string, qty int) {
	e.t.Helper()
	err := pgx.BeginFunc(context.Background(), e.pool, func(tx pgx.Tx) error {
		_, err := e.svc.ReceiveInTx(context.Background(), tx, e.caller(e.manager), application.StockMove{ProductID: e.widget, StorageLocationID: loc, Quantity: qty})
		return err
	})
	if err != nil {
		e.t.Fatalf("receive: %v", err)
	}
}

func (e *env) balance(loc string) application.Balance {
	e.t.Helper()
	res, err := e.svc.ListStock(context.Background(), e.view, application.StockFilter{ProductID: e.widget, StorageLocationID: loc})
	if err != nil {
		e.t.Fatal(err)
	}
	if len(res.Items) == 0 {
		return application.Balance{}
	}
	return res.Items[0]
}

// reconcile checks the invariant: the ledger sums equal the balances.
func (e *env) reconcile() {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), `
		SELECT b.storage_location_id::text, b.on_hand, b.reserved,
		       coalesce((SELECT sum(on_hand_delta) FROM inventory.inventory_transactions t WHERE t.product_id = b.product_id AND t.storage_location_id = b.storage_location_id), 0),
		       coalesce((SELECT sum(reserved_delta) FROM inventory.inventory_transactions t WHERE t.product_id = b.product_id AND t.storage_location_id = b.storage_location_id), 0)
		FROM inventory.stock_balances b WHERE b.product_id = $1::uuid`, e.widget)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var loc string
		var onHand, reserved, sumOn, sumRes int
		if err := rows.Scan(&loc, &onHand, &reserved, &sumOn, &sumRes); err != nil {
			e.t.Fatal(err)
		}
		if onHand != sumOn || reserved != sumRes {
			e.t.Errorf("balance %s = %d/%d but the ledger sums to %d/%d", loc, onHand, reserved, sumOn, sumRes)
		}
	}
}

func TestLedgerBalancesAndAudit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.receive(e.shelfA.ID, 10)
	move := func(q int) application.StockMove {
		return application.StockMove{ProductID: e.widget, StorageLocationID: e.shelfA.ID, Quantity: q, Reason: "demo"}
	}
	if _, err := e.svc.Issue(ctx, e.caller(e.manager), e.manage, move(3)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Return(ctx, e.caller(e.manager), e.manage, move(1)); err != nil {
		t.Fatal(err)
	}
	rows, err := e.svc.Transfer(ctx, e.caller(e.manager), e.manage, application.TransferMove{ProductID: e.widget, FromID: e.shelfA.ID, ToID: e.shelfB.ID, Quantity: 4})
	if err != nil || len(rows) != 2 || rows[0].GroupID != rows[1].GroupID || rows[0].OnHandDelta != -4 || rows[1].OnHandDelta != 4 {
		t.Fatalf("transfer = %+v %v", rows, err)
	}
	if _, err := e.svc.Correct(ctx, e.caller(e.manager), e.manage, application.Correction{ProductID: e.widget, StorageLocationID: e.shelfB.ID, Delta: -1, Reason: "count"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Dispose(ctx, e.caller(e.manager), e.manage, application.StockMove{ProductID: e.widget, StorageLocationID: e.shelfA.ID, Quantity: 1, Reason: "broken"}); err != nil {
		t.Fatal(err)
	}
	if a, b := e.balance(e.shelfA.ID), e.balance(e.shelfB.ID); a.OnHand != 3 || b.OnHand != 3 {
		t.Errorf("balances = %d and %d, want 3 and 3", a.OnHand, b.OnHand)
	}
	e.reconcile()
	// One audit event per operation (receive, issue, return, transfer, correct, dispose), not per ledger row.
	if n := e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action LIKE 'inventory.stock.%'`, e.corr); n != 6 {
		t.Errorf("%d stock audit events, want 6", n)
	}
	if n := e.count(`SELECT count(*) FROM inventory.inventory_transactions WHERE correlation_id = $1`, e.corr); n != 7 {
		t.Errorf("%d ledger rows, want 7", n)
	}
	all, err := e.svc.ListTransactions(ctx, e.view, application.TransactionFilter{ProductID: e.widget, Page: application.Page{Limit: 3}})
	if err != nil || len(all.Items) != 3 || all.NextCursor == "" {
		t.Fatalf("ledger page = %+v %v", all, err)
	}
	if all.Items[0].ID < all.Items[1].ID {
		t.Error("the ledger is listed newest first")
	}
}

func TestLedgerRowsAreImmutable(t *testing.T) {
	e := newEnv(t)
	e.receive(e.shelfA.ID, 1)
	var id string
	if err := e.pool.QueryRow(context.Background(), `SELECT id::text FROM inventory.inventory_transactions WHERE correlation_id = $1 LIMIT 1`, e.corr).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for name, sql := range map[string]string{
		"update": `UPDATE inventory.inventory_transactions SET on_hand_delta = 99 WHERE id = $1::uuid`,
		"delete": `DELETE FROM inventory.inventory_transactions WHERE id = $1::uuid`,
	} {
		if _, err := e.pool.Exec(context.Background(), sql, id); err == nil {
			t.Errorf("%s of a ledger row was accepted", name)
		}
	}
}

func TestStockRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.receive(e.shelfA.ID, 5)
	mv := func(p string, q int) application.StockMove {
		return application.StockMove{ProductID: p, StorageLocationID: e.shelfA.ID, Quantity: q, Reason: "x"}
	}
	if _, err := e.svc.Issue(ctx, e.caller(e.manager), e.manage, mv(e.widget, 6)); !errors.Is(err, application.ErrInsufficientStock) {
		t.Errorf("issuing more than on hand: %v", err)
	}
	if _, err := e.svc.Issue(ctx, e.caller(e.manager), e.manage, mv(e.laptop, 1)); !errors.Is(err, application.ErrProductInvalid) {
		t.Errorf("a serialized product has no stock: %v", err)
	}
	if _, err := e.svc.Return(ctx, e.caller(e.manager), e.manage, mv(e.dock, 1)); !errors.Is(err, application.ErrProductInvalid) {
		t.Errorf("adding stock of an inactive product: %v", err)
	}
	var inv *application.InvalidInputError
	for _, q := range []int{0, -1, application.MaxQuantity + 1} {
		if _, err := e.svc.Issue(ctx, e.caller(e.manager), e.manage, mv(e.widget, q)); !errors.As(err, &inv) {
			t.Errorf("quantity %d: %v", q, err)
		}
	}
	if _, err := e.svc.Dispose(ctx, e.caller(e.manager), e.manage, application.StockMove{ProductID: e.widget, StorageLocationID: e.shelfA.ID, Quantity: 1}); !errors.As(err, &inv) {
		t.Errorf("dispose needs a reason: %v", err)
	}
	if _, err := e.svc.Correct(ctx, e.caller(e.manager), e.manage, application.Correction{ProductID: e.widget, StorageLocationID: e.shelfA.ID, Delta: 0, Reason: "x"}); !errors.As(err, &inv) {
		t.Errorf("zero correction: %v", err)
	}
	if _, err := e.svc.Transfer(ctx, e.caller(e.manager), e.manage, application.TransferMove{ProductID: e.widget, FromID: e.shelfA.ID, ToID: e.shelfA.ID, Quantity: 1}); !errors.As(err, &inv) {
		t.Errorf("transfer to itself: %v", err)
	}
	if _, err := e.svc.Issue(ctx, e.caller(e.stranger), e.view, mv(e.widget, 1)); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("issue without inventory.manage: %v", err)
	}
	if _, err := e.svc.ListStock(ctx, application.Principal{UserID: e.stranger}, application.StockFilter{}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("stock without inventory.view: %v", err)
	}
	// Putting stock into a deactivated location is refused, taking it out still works.
	if _, err := e.svc.SetStorageLocationActive(ctx, e.caller(e.manager), e.manage, e.shelfA.ID, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Return(ctx, e.caller(e.manager), e.manage, mv(e.widget, 1)); !errors.Is(err, application.ErrLocationInactive) {
		t.Errorf("returning into an inactive location: %v", err)
	}
	if _, err := e.svc.Issue(ctx, e.caller(e.manager), e.manage, mv(e.widget, 1)); err != nil {
		t.Errorf("draining an inactive location: %v", err)
	}
	e.reconcile()
}

func TestReservationsNeverExceedAvailableStock(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.receive(e.shelfA.ID, 10)
	r1, err := e.svc.ReserveQuantity(ctx, e.caller(e.manager), e.manage, application.QuantityReservation{
		ProductID: e.widget, StorageLocationID: e.shelfA.ID, Quantity: 7, Origin: application.Origin{Type: "service_request", ID: e.holder}})
	if err != nil || r1.Status != "active" || r1.ContextType == nil {
		t.Fatalf("reserve = %+v %v", r1, err)
	}
	if b := e.balance(e.shelfA.ID); b.Reserved != 7 || b.Available() != 3 {
		t.Errorf("balance = %+v", b)
	}
	q := application.QuantityReservation{ProductID: e.widget, StorageLocationID: e.shelfA.ID, Quantity: 4}
	if _, err := e.svc.ReserveQuantity(ctx, e.caller(e.manager), e.manage, q); !errors.Is(err, application.ErrInsufficientStock) {
		t.Errorf("over-reserving: %v", err)
	}
	// Reserved stock cannot be issued directly, transferred away or corrected below the reservation.
	if _, err := e.svc.Issue(ctx, e.caller(e.manager), e.manage, application.StockMove{ProductID: e.widget, StorageLocationID: e.shelfA.ID, Quantity: 4}); !errors.Is(err, application.ErrInsufficientStock) {
		t.Errorf("issuing reserved stock: %v", err)
	}
	if _, err := e.svc.Correct(ctx, e.caller(e.manager), e.manage, application.Correction{ProductID: e.widget, StorageLocationID: e.shelfA.ID, Delta: -4, Reason: "count"}); !errors.Is(err, application.ErrInsufficientStock) {
		t.Errorf("correcting below the reserved quantity: %v", err)
	}
	released, err := e.svc.Release(ctx, e.caller(e.manager), e.manage, r1.ID, nil, "not needed")
	if err != nil || released.Status != "released" || released.ClosedAt == nil {
		t.Fatalf("release = %+v %v", released, err)
	}
	var tr *application.InvalidTransitionError
	if _, err := e.svc.Release(ctx, e.caller(e.manager), e.manage, r1.ID, nil, ""); !errors.As(err, &tr) {
		t.Errorf("a released reservation cannot be rewound: %v", err)
	}
	if _, err := e.svc.Fulfill(ctx, e.caller(e.manager), e.manage, r1.ID, nil, nil, ""); !errors.As(err, &tr) {
		t.Errorf("a released reservation cannot be fulfilled: %v", err)
	}
	r2, err := e.svc.ReserveQuantity(ctx, e.caller(e.manager), e.manage, application.QuantityReservation{ProductID: e.widget, StorageLocationID: e.shelfA.ID, Quantity: 4})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Fulfill(ctx, e.caller(e.manager), e.manage, r2.ID, nil, nil, "handed over"); err != nil {
		t.Fatal(err)
	}
	if b := e.balance(e.shelfA.ID); b.OnHand != 6 || b.Reserved != 0 {
		t.Errorf("after fulfilling: %+v", b)
	}
	e.reconcile()
	for _, ev := range []string{"StockReserved", "ReservationReleased", "ReservationFulfilled"} {
		if e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = $2`, e.corr, ev) == 0 {
			t.Errorf("%s was not published", ev)
		}
	}
}

func TestConcurrentReservationsNeverOversell(t *testing.T) {
	e := newEnv(t)
	e.receive(e.shelfA.ID, 10)
	var wg sync.WaitGroup
	var ok, refused atomic.Int32
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.ReserveQuantity(context.Background(), e.caller(e.manager), e.manage, application.QuantityReservation{
				ProductID: e.widget, StorageLocationID: e.shelfA.ID, Quantity: 2})
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, application.ErrInsufficientStock):
				refused.Add(1)
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 5 || refused.Load() != 19 {
		t.Errorf("reserved %d times and refused %d, want exactly 5 reservations of 2 from 10 pieces", ok.Load(), refused.Load())
	}
	if b := e.balance(e.shelfA.ID); b.Reserved != 10 || b.Available() != 0 {
		t.Errorf("balance = %+v", b)
	}
	e.reconcile()
}

func TestConcurrentIssuesAndTransfersKeepTheLedgerConsistent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.receive(e.shelfA.ID, 20)
	e.receive(e.shelfB.ID, 20)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			from, to := e.shelfA.ID, e.shelfB.ID
			if i%2 == 1 {
				from, to = to, from // opposite transfers must not deadlock
			}
			_, err := e.svc.Transfer(ctx, e.caller(e.manager), e.manage, application.TransferMove{ProductID: e.widget, FromID: from, ToID: to, Quantity: 3})
			if err != nil && !errors.Is(err, application.ErrInsufficientStock) {
				t.Errorf("transfer: %v", err)
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			loc := e.shelfA.ID
			if i%3 == 0 {
				loc = e.shelfB.ID
			}
			_, err := e.svc.Issue(ctx, e.caller(e.manager), e.manage, application.StockMove{ProductID: e.widget, StorageLocationID: loc, Quantity: 1})
			if err != nil && !errors.Is(err, application.ErrInsufficientStock) {
				t.Errorf("issue: %v", err)
			}
		}()
	}
	wg.Wait()
	a, b := e.balance(e.shelfA.ID), e.balance(e.shelfB.ID)
	if a.OnHand < 0 || b.OnHand < 0 {
		t.Errorf("negative stock: %d, %d", a.OnHand, b.OnHand)
	}
	e.reconcile()
}

func TestAssetReservations(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	r, err := e.svc.ReserveAsset(ctx, e.caller(e.manager), e.manage, application.AssetReservation{AssetID: e.asset})
	if err != nil || r.Kind != "asset" || r.ProductID != e.laptop {
		t.Fatalf("reserve asset = %+v %v", r, err)
	}
	if _, err := e.svc.ReserveAsset(ctx, e.caller(e.manager), e.manage, application.AssetReservation{AssetID: e.asset}); !errors.Is(err, application.ErrAssetUnavailable) {
		t.Errorf("reserving a reserved asset: %v", err)
	}
	if _, err := e.svc.ReserveAsset(ctx, e.caller(e.manager), e.manage, application.AssetReservation{AssetID: e.stranger}); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown asset: %v", err)
	}
	var inv *application.InvalidInputError
	if _, err := e.svc.Fulfill(ctx, e.caller(e.manager), e.manage, r.ID, nil, nil, ""); !errors.As(err, &inv) {
		t.Errorf("fulfilling without an assignee: %v", err)
	}
	if _, err := e.svc.Fulfill(ctx, e.caller(e.manager), e.manage, r.ID, nil, &application.AssetAssignee{Type: "user", ID: e.stranger}, ""); !errors.Is(err, application.ErrAssigneeInvalid) {
		t.Errorf("inactive assignee: %v", err)
	}
	if _, err := e.svc.Release(ctx, e.caller(e.manager), e.manage, r.ID, nil, ""); err != nil {
		t.Fatal(err)
	}
	if e.assets.status[e.asset] != "available" {
		t.Errorf("the released asset is %s", e.assets.status[e.asset])
	}
	r2, err := e.svc.ReserveAsset(ctx, e.caller(e.manager), e.manage, application.AssetReservation{AssetID: e.asset})
	if err != nil {
		t.Fatal(err)
	}
	done, err := e.svc.Fulfill(ctx, e.caller(e.manager), e.manage, r2.ID, nil, &application.AssetAssignee{Type: "user", ID: e.holder}, "welcome")
	if err != nil || done.Status != "fulfilled" || e.assets.status[e.asset] != "assigned" {
		t.Fatalf("fulfill = %+v %v (asset %s)", done, err, e.assets.status[e.asset])
	}
}

func TestConcurrentAssetReservationsHaveOneWinner(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	var won atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.ReserveAsset(context.Background(), e.caller(e.manager), e.manage, application.AssetReservation{AssetID: e.asset})
			switch {
			case err == nil:
				won.Add(1)
			case errors.Is(err, application.ErrAssetUnavailable), errors.Is(err, application.ErrConflict):
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Errorf("%d reservations succeeded, want 1", won.Load())
	}
	if e.count(`SELECT count(*) FROM inventory.reservations WHERE asset_id = $1::uuid AND status = 'active'`, e.asset) != 1 {
		t.Error("exactly one active reservation per asset is expected")
	}
}

func TestWarehousesAndLocations(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.svc.CreateWarehouse(ctx, e.caller(e.manager), e.manage, "wh "+e.corr, nil); !errors.Is(err, application.ErrConflict) {
		t.Errorf("duplicate warehouse name (case-insensitive): %v", err)
	}
	if _, err := e.svc.CreateStorageLocation(ctx, e.caller(e.manager), e.manage, e.wh.ID, "a"); !errors.Is(err, application.ErrConflict) {
		t.Errorf("duplicate location name: %v", err)
	}
	if _, err := e.svc.CreateWarehouse(ctx, e.caller(e.manager), e.manage, "Bad", ptr(e.stranger)); !errors.Is(err, application.ErrReferenceInvalid) {
		t.Errorf("unknown organization location: %v", err)
	}
	renamed, err := e.svc.RenameStorageLocation(ctx, e.caller(e.manager), e.manage, e.shelfA.ID, e.shelfA.Version, "A1")
	if err != nil || renamed.Name != "A1" || renamed.Version != 2 {
		t.Fatalf("rename = %+v %v", renamed, err)
	}
	if _, err := e.svc.RenameStorageLocation(ctx, e.caller(e.manager), e.manage, e.shelfA.ID, 1, "A2"); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale version: %v", err)
	}
	if _, err := e.svc.SetWarehouseActive(ctx, e.caller(e.manager), e.manage, e.wh.ID, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateStorageLocation(ctx, e.caller(e.manager), e.manage, e.wh.ID, "C"); !errors.Is(err, application.ErrLocationInactive) {
		t.Errorf("new location in an inactive warehouse: %v", err)
	}
	e.receiveErr(e.shelfB.ID)
	list, err := e.svc.ListWarehouses(ctx, e.view, false, application.Page{})
	for _, w := range list.Items {
		if w.ID == e.wh.ID {
			t.Error("inactive warehouses are hidden by default")
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateWarehouse(ctx, e.caller(e.stranger), e.view, "X", nil); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("create without inventory.manage: %v", err)
	}
}

func ptr(s string) *string { return &s }

// receiveErr asserts that receiving into a location of an inactive warehouse is refused.
func (e *env) receiveErr(loc string) {
	e.t.Helper()
	err := pgx.BeginFunc(context.Background(), e.pool, func(tx pgx.Tx) error {
		_, err := e.svc.ReceiveInTx(context.Background(), tx, e.caller(e.manager), application.StockMove{ProductID: e.widget, StorageLocationID: loc, Quantity: 1})
		return err
	})
	if !errors.Is(err, application.ErrLocationInactive) {
		e.t.Errorf("receiving into an inactive warehouse: %v", err)
	}
}

func TestDatabaseInvariants(t *testing.T) {
	e := newEnv(t)
	e.receive(e.shelfA.ID, 2)
	for name, sql := range map[string]string{
		"negative on hand":      `UPDATE inventory.stock_balances SET on_hand = -1 WHERE product_id = $1::uuid`,
		"reserved over on hand": `UPDATE inventory.stock_balances SET reserved = on_hand + 1 WHERE product_id = $1::uuid`,
		"negative reserved":     `UPDATE inventory.stock_balances SET reserved = -1 WHERE product_id = $1::uuid`,
	} {
		if _, err := e.pool.Exec(context.Background(), sql, e.widget); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO inventory.reservations(kind, product_id, quantity) VALUES ('quantity', $1::uuid, 1)`, e.widget); err == nil {
		t.Error("a quantity reservation without a storage location was accepted")
	}
}

func TestInvalidIdsAreValidationErrorsNotServerErrors(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var inv *application.InvalidInputError
	mv := application.StockMove{ProductID: e.widget, StorageLocationID: "not-a-uuid", Quantity: 1, Reason: "x"}
	for name, fn := range map[string]func() error{
		"issue":  func() error { _, err := e.svc.Issue(ctx, e.caller(e.manager), e.manage, mv); return err },
		"return": func() error { _, err := e.svc.Return(ctx, e.caller(e.manager), e.manage, mv); return err },
		"transfer": func() error {
			_, err := e.svc.Transfer(ctx, e.caller(e.manager), e.manage, application.TransferMove{ProductID: e.widget, FromID: "x", ToID: e.shelfA.ID, Quantity: 1})
			return err
		},
		"reserve": func() error {
			_, err := e.svc.ReserveQuantity(ctx, e.caller(e.manager), e.manage, application.QuantityReservation{ProductID: e.widget, StorageLocationID: "x", Quantity: 1})
			return err
		},
	} {
		if err := fn(); !errors.As(err, &inv) {
			t.Errorf("%s with a malformed id: %v", name, err)
		}
	}
	// A return needs a reason like a correction does.
	if _, err := e.svc.Return(ctx, e.caller(e.manager), e.manage, application.StockMove{ProductID: e.widget, StorageLocationID: e.shelfA.ID, Quantity: 1}); !errors.As(err, &inv) {
		t.Errorf("return without a reason: %v", err)
	}
}
