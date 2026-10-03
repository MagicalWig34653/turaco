package transport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/inventory/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/inventory/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/inventory/transport"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type fakeAuth struct {
	user  string
	perms map[string]struct{}
	ok    bool
}

func (f fakeAuth) Authenticate(*http.Request) (authorization.Principal, bool, error) {
	return authorization.Principal{UserID: f.user, Permissions: f.perms}, f.ok, nil
}

func as(user string, perms ...string) fakeAuth {
	m := map[string]struct{}{}
	for _, p := range perms {
		m[p] = struct{}{}
	}
	return fakeAuth{user: user, perms: m, ok: true}
}

type dir struct{}

func (dir) ActiveLocations(context.Context, []string) (map[string]bool, error) {
	return map[string]bool{}, nil
}
func (dir) ActiveUsers(context.Context, []string) (map[string]bool, error) {
	return map[string]bool{}, nil
}
func (dir) ActiveTeams(context.Context, []string) (map[string]bool, error) {
	return map[string]bool{}, nil
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

type noAssets struct{}

func (noAssets) Assets(context.Context, []string) (map[string]application.AssetView, error) {
	return map[string]application.AssetView{}, nil
}
func (noAssets) CreateReceivedInTx(context.Context, pgx.Tx, audit.Actor, string, application.ReceivedAsset) (string, error) {
	return "", nil
}
func (noAssets) ReserveInTx(context.Context, pgx.Tx, audit.Actor, string, string) error { return nil }
func (noAssets) ReleaseReservationInTx(context.Context, pgx.Tx, audit.Actor, string, string) error {
	return nil
}
func (noAssets) AssignReservedInTx(context.Context, pgx.Tx, audit.Actor, string, string, application.AssetAssignee, string) error {
	return nil
}

type noOrders struct{}

func (noOrders) Order(context.Context, string) (application.OrderView, error) {
	return application.OrderView{}, application.ErrNotFound
}
func (noOrders) RecordReceiptInTx(context.Context, pgx.Tx, audit.Actor, string, string, []application.OrderReceipt) error {
	return nil
}

const (
	admin   = "00000000-0000-7000-8000-0000000000c3"
	someone = "00000000-0000-7000-8000-0000000000b2"
	product = "00000000-0000-7000-8000-0000000000d4"
)

func serve(t *testing.T, a authorization.Authenticator) (http.Handler, *application.Service) {
	t.Helper()
	pool := dbtest.Pool(t)
	svc := application.NewService(repository.New(pool), dir{}, products{product: {ID: product, Name: "Cable", Active: true, StockManaged: true}}, noAssets{}, noOrders{})
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	transport.Register(mux, svc, a, logger)
	t.Cleanup(func() {
		ctx := context.Background()
		conn, err := pool.Acquire(ctx)
		if err == nil {
			// Session-local: triggers are skipped on this connection only, never for concurrent tests.
			_, _ = conn.Exec(ctx, `SET session_replication_role = replica`)
			_, _ = conn.Exec(ctx, `DELETE FROM inventory.inventory_transactions WHERE product_id = $1::uuid`, product)
			_, _ = conn.Exec(ctx, `RESET session_replication_role`)
			conn.Release()
		}
		_, _ = pool.Exec(ctx, `DELETE FROM inventory.reservations WHERE product_id = $1::uuid`, product)
		_, _ = pool.Exec(ctx, `DELETE FROM inventory.stock_balances WHERE product_id = $1::uuid`, product)
		_, _ = pool.Exec(ctx, `DELETE FROM inventory.storage_locations WHERE name LIKE 'http-%'`)
		_, _ = pool.Exec(ctx, `DELETE FROM inventory.warehouses WHERE name LIKE 'http-%'`)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE actor_id = $1::uuid`, admin)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE actor_id = $1::uuid`, admin)
	})
	return httpx.Middleware(logger, mux), svc
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestInventoryOverHTTP(t *testing.T) {
	h, svc := serve(t, as(admin, "inventory.manage"))
	rec := do(h, "POST", "/api/v1/warehouses", `{"name":"http-wh"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create warehouse = %d %s", rec.Code, rec.Body)
	}
	var wh struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &wh)
	rec = do(h, "POST", "/api/v1/warehouses/"+wh.ID+"/storage-locations", `{"name":"http-shelf"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create location = %d %s", rec.Code, rec.Body)
	}
	var loc struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &loc)
	if err := pgx.BeginFunc(context.Background(), dbtest.Pool(t), func(tx pgx.Tx) error {
		_, err := svc.ReceiveInTx(context.Background(), tx, application.Caller{Actor: audit.UserActor(admin), CorrelationID: "http-test"},
			application.StockMove{ProductID: product, StorageLocationID: loc.ID, Quantity: 5})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	body := `{"productId":"` + product + `","storageLocationId":"` + loc.ID + `","quantity":2,"reason":"demo"}`
	if rec := do(h, "POST", "/api/v1/stock/issue", body); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"onHandDelta":-2`) {
		t.Errorf("issue = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/v1/stock/issue", strings.Replace(body, `"quantity":2`, `"quantity":9`, 1)); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "insufficient_stock") {
		t.Errorf("issue too much = %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "POST", "/api/v1/reservations", `{"kind":"quantity","productId":"`+product+`","storageLocationId":"`+loc.ID+`","quantity":3,"originType":"service_request","originId":"`+someone+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("reserve = %d %s", rec.Code, rec.Body)
	}
	var res struct {
		ID      string
		Version int
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec := do(h, "GET", "/api/v1/stock?productId="+product, ""); !strings.Contains(rec.Body.String(), `"available":0`) || !strings.Contains(rec.Body.String(), `"reserved":3`) {
		t.Errorf("stock = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/v1/reservations/"+res.ID+"/fulfill", `{}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"fulfilled"`) {
		t.Errorf("fulfill = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/v1/reservations/"+res.ID+"/release", `{}`); rec.Code != http.StatusConflict {
		t.Errorf("release after fulfill = %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/v1/inventory-transactions?productId="+product+"&contextType=service_request&contextId="+someone, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"type":"reservation"`) {
		t.Errorf("ledger = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/v1/reservations", `{"kind":"bulk"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown kind = %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/v1/reservations?status=expired", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("unsupported status filter = %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/v1/stock?productId=not-a-uuid", ""); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "onHand") {
		t.Errorf("malformed filter must yield an empty page: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/v1/reservations/not-a-uuid", ""); rec.Code != http.StatusNotFound {
		t.Errorf("malformed id = %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/v1/goods-receipts", `{"orderId":"`+someone+`","lines":[{"orderLineId":"`+someone+`","quantity":1}]}`); rec.Code != http.StatusNotFound {
		t.Errorf("receipt for an unknown order = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/v1/goods-receipts", `{"orderId":"`+someone+`","lines":[{"orderLineId":"x","quantity":1,"warrantyUntil":"01.02.2027"}]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad date = %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/v1/goods-receipts/not-a-uuid", ""); rec.Code != http.StatusNotFound {
		t.Errorf("malformed receipt id = %d", rec.Code)
	}
}

func TestPermissionsOverHTTP(t *testing.T) {
	view, _ := serve(t, as(someone, "inventory.view"))
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/v1/warehouses"}, {"POST", "/api/v1/stock/issue"}, {"POST", "/api/v1/stock/transfer"},
		{"POST", "/api/v1/stock/correct"}, {"POST", "/api/v1/reservations"}, {"POST", "/api/v1/reservations/" + admin + "/release"},
		{"PATCH", "/api/v1/storage-locations/" + admin}, {"POST", "/api/v1/goods-receipts"},
	} {
		if rec := do(view, c.method, c.path, `{}`); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with inventory.view = %d, want 403", c.method, c.path, rec.Code)
		}
	}
	for _, path := range []string{"/api/v1/warehouses", "/api/v1/stock", "/api/v1/inventory-transactions", "/api/v1/reservations", "/api/v1/goods-receipts"} {
		if rec := do(view, "GET", path, ""); rec.Code != http.StatusOK {
			t.Errorf("GET %s with inventory.view = %d", path, rec.Code)
		}
	}
	nobody, _ := serve(t, as(someone))
	for _, path := range []string{"/api/v1/warehouses", "/api/v1/stock", "/api/v1/inventory-transactions", "/api/v1/reservations", "/api/v1/goods-receipts"} {
		if rec := do(nobody, "GET", path, ""); rec.Code != http.StatusForbidden {
			t.Errorf("GET %s without permission = %d, want 403", path, rec.Code)
		}
	}
	anon, _ := serve(t, fakeAuth{})
	if rec := do(anon, "GET", "/api/v1/stock", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d", rec.Code)
	}
}
