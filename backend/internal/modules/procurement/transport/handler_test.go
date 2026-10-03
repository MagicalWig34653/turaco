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

	"github.com/MagicalWig34653/turaco/backend/internal/modules/procurement/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/procurement/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/procurement/transport"
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

func (dir) ActiveUsers(context.Context, []string) (map[string]bool, error) {
	return map[string]bool{}, nil
}

type approvals struct{}

func (approvals) RequestInTx(context.Context, pgx.Tx, audit.Actor, string, application.ApprovalRequest) error {
	return nil
}
func (approvals) CancelBySubjectInTx(context.Context, pgx.Tx, audit.Actor, string, string) error {
	return nil
}

const (
	admin   = "00000000-0000-7000-8000-0000000000c3"
	someone = "00000000-0000-7000-8000-0000000000b2"
	product = "00000000-0000-7000-8000-0000000000d4"
)

func serve(t *testing.T, a authorization.Authenticator) http.Handler {
	t.Helper()
	pool := dbtest.Pool(t)
	svc := application.NewService(repository.New(pool), dir{}, products{product: {ID: product, Name: "Cable", Active: true}}, approvals{})
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	transport.Register(mux, svc, a, logger)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM procurement.purchase_orders WHERE supplier_id IN (SELECT id FROM procurement.suppliers WHERE name LIKE 'http-%')`)
		_, _ = pool.Exec(ctx, `DELETE FROM procurement.procurement_requests WHERE product_id = $1::uuid`, product)
		_, _ = pool.Exec(ctx, `DELETE FROM procurement.suppliers WHERE name LIKE 'http-%'`)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE actor_id = $1::uuid`, admin)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE actor_id = $1::uuid`, admin)
	})
	return httpx.Middleware(logger, mux)
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func idOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var v struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || v.ID == "" {
		t.Fatalf("no id in %d %s", rec.Code, rec.Body)
	}
	return v.ID
}

func TestPurchaseOrderOverHTTP(t *testing.T) {
	h := serve(t, as(admin, "procurement.manage"))
	rec := do(h, "POST", "/api/v1/suppliers", `{"name":"http-supplier","accountReference":"K-1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("supplier = %d %s", rec.Code, rec.Body)
	}
	supplier := idOf(t, rec)
	if rec := do(h, "POST", "/api/v1/suppliers", `{"name":"HTTP-Supplier"}`); rec.Code != http.StatusConflict {
		t.Errorf("duplicate supplier = %d", rec.Code)
	}
	rec = do(h, "POST", "/api/v1/purchase-orders", `{"supplierId":"`+supplier+`","notes":"urgent"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("order = %d %s", rec.Code, rec.Body)
	}
	order := idOf(t, rec)
	if rec := do(h, "POST", "/api/v1/purchase-orders/"+order+"/lines", `{"productId":"`+product+`","quantity":4,"unitPriceCents":1999}`); rec.Code != http.StatusCreated {
		t.Fatalf("line = %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "GET", "/api/v1/purchase-orders/"+order, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"totalCents":7996`) || !strings.Contains(rec.Body.String(), `"supplierName":"http-supplier"`) ||
		!strings.Contains(rec.Body.String(), `"linesEditable":true`) || !strings.Contains(rec.Body.String(), `"submit"`) {
		t.Errorf("detail = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/v1/purchase-orders/"+order+"/submit", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("submit without approver = %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/v1/purchase-orders/"+order+"/submit", `{"approverUserId":"`+someone+`"}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"pending_approval"`) {
		t.Errorf("submit = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/v1/purchase-orders/"+order+"/send", `{}`); rec.Code != http.StatusConflict {
		t.Errorf("send before approval = %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/v1/purchase-orders/"+order+"/lines", `{"productId":"`+product+`","quantity":1}`); rec.Code != http.StatusConflict {
		t.Errorf("editing a pending order = %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/v1/purchase-orders/"+order+"/cancel", `{"reason":"changed plans"}`); rec.Code != http.StatusOK {
		t.Errorf("cancel = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/v1/procurement-requests", `{"productId":"`+product+`","quantity":2,"originType":"service_request","originId":"`+someone+`"}`); rec.Code != http.StatusCreated {
		t.Errorf("need = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/v1/procurement-requests?status=weird", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown status filter = %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/v1/purchase-orders/not-a-uuid", ""); rec.Code != http.StatusNotFound {
		t.Errorf("malformed id = %d", rec.Code)
	}
}

func TestProcurementPermissionsOverHTTP(t *testing.T) {
	view := serve(t, as(someone, "procurement.view"))
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/v1/suppliers"}, {"PATCH", "/api/v1/suppliers/" + admin}, {"POST", "/api/v1/procurement-requests"},
		{"POST", "/api/v1/purchase-orders"}, {"POST", "/api/v1/purchase-orders/" + admin + "/lines"}, {"POST", "/api/v1/purchase-orders/" + admin + "/submit"},
		{"POST", "/api/v1/purchase-orders/" + admin + "/send"}, {"POST", "/api/v1/purchase-orders/" + admin + "/cancel"},
		{"DELETE", "/api/v1/purchase-orders/" + admin + "/lines/" + someone},
	} {
		if rec := do(view, c.method, c.path, `{}`); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with procurement.view = %d, want 403", c.method, c.path, rec.Code)
		}
	}
	for _, path := range []string{"/api/v1/suppliers", "/api/v1/procurement-requests", "/api/v1/purchase-orders"} {
		if rec := do(view, "GET", path, ""); rec.Code != http.StatusOK {
			t.Errorf("GET %s with procurement.view = %d", path, rec.Code)
		}
	}
	nobody := serve(t, as(someone))
	for _, path := range []string{"/api/v1/suppliers", "/api/v1/procurement-requests", "/api/v1/purchase-orders"} {
		if rec := do(nobody, "GET", path, ""); rec.Code != http.StatusForbidden {
			t.Errorf("GET %s without permission = %d", path, rec.Code)
		}
	}
	if rec := do(serve(t, fakeAuth{}), "GET", "/api/v1/purchase-orders", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d", rec.Code)
	}
}
