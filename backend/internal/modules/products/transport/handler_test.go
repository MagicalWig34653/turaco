package transport

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/products/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

type fakeAuth struct {
	perms map[string]struct{}
	ok    bool
}

func (f fakeAuth) Authenticate(*http.Request) (authorization.Principal, bool, error) {
	return authorization.Principal{UserID: "00000000-0000-7000-8000-0000000000a1", Permissions: f.perms}, f.ok, nil
}

func with(perms ...string) fakeAuth {
	m := map[string]struct{}{}
	for _, p := range perms {
		m[p] = struct{}{}
	}
	return fakeAuth{perms: m, ok: true}
}

type store struct {
	application.Store
	calls   int
	err     error
	filter  application.ProductFilter
	product application.Product
}

func (s *store) InsertManufacturer(_ context.Context, _ application.Caller, n string) (application.Manufacturer, error) {
	s.calls++
	return application.Manufacturer{ID: "1", Name: n, Version: 1}, s.err
}
func (s *store) ChangeManufacturer(_ context.Context, _ application.Caller, _ string, decide func(application.Manufacturer) (application.Change[application.Manufacturer], error)) (application.Manufacturer, error) {
	s.calls++
	ch, err := decide(application.Manufacturer{Name: "old", Version: 1})
	return ch.Next, err
}
func (s *store) ListManufacturers(context.Context, string, application.Page) (application.Result[application.Manufacturer], error) {
	return application.Result[application.Manufacturer]{Items: []application.Manufacturer{{ID: "1", Name: "Dell", Version: 1}}}, nil
}
func (s *store) ListCategories(context.Context, string, application.Page) (application.Result[application.Category], error) {
	return application.Result[application.Category]{}, nil
}
func (s *store) InsertCategory(_ context.Context, _ application.Caller, n string, p *string) (application.Category, error) {
	s.calls++
	return application.Category{ID: "2", Name: n, ParentID: p, Version: 1}, s.err
}
func (s *store) ChangeCategory(_ context.Context, _ application.Caller, _ string, decide func(application.Category) (application.Change[application.Category], error)) (application.Category, error) {
	s.calls++
	ch, err := decide(application.Category{Name: "old", Version: 1})
	return ch.Next, err
}
func (s *store) InsertProduct(_ context.Context, _ application.Caller, n application.NewProduct) (application.Product, error) {
	s.calls++
	return application.Product{ID: "3", Name: n.Name, Active: true, Version: 1}, s.err
}
func (s *store) GetProduct(context.Context, string) (application.Product, error) {
	return s.product, nil
}
func (s *store) ChangeProduct(_ context.Context, _ application.Caller, _ string, decide func(application.Product) (application.Change[application.Product], error)) (application.Product, error) {
	s.calls++
	ch, err := decide(s.product)
	return ch.Next, err
}
func (s *store) ListProducts(_ context.Context, f application.ProductFilter) (application.Result[application.Product], error) {
	s.filter = f
	return application.Result[application.Product]{Items: []application.Product{s.product}}, nil
}

func serve(t *testing.T, s *store, a authorization.Authenticator, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, application.NewService(s), a, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-ID", "req-1")
	var rd io.Reader
	if body != "" {
		rd = bytes.NewBufferString(body)
	}
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, rd))
	return rec
}

const pid = "/api/v1/products/00000000-0000-7000-8000-000000000001"

func newStore() *store {
	return &store{product: application.Product{ID: "3", Name: "Latitude", Version: 1, Active: true}}
}

func TestRoutePermissions(t *testing.T) {
	for _, r := range []struct {
		method, path, body string
		write              bool
	}{
		{"GET", "/api/v1/manufacturers", "", false},
		{"GET", "/api/v1/product-categories", "", false},
		{"GET", "/api/v1/products", "", false},
		{"GET", pid, "", false},
		{"POST", "/api/v1/manufacturers", `{"name":"Dell"}`, true},
		{"PATCH", "/api/v1/manufacturers/x", `{"name":"Dell","expectedVersion":1}`, true},
		{"POST", "/api/v1/product-categories", `{"name":"Laptops"}`, true},
		{"PATCH", "/api/v1/product-categories/x", `{"name":"L","expectedVersion":1}`, true},
		{"POST", "/api/v1/products", `{"name":"Latitude"}`, true},
		{"PATCH", pid, `{"expectedVersion":1,"name":"x"}`, true},
		{"POST", pid + "/activate", `{}`, true},
		{"POST", pid + "/deactivate", `{}`, true},
	} {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			if rec := serve(t, newStore(), authorization.DenyAll{}, r.method, r.path, r.body); rec.Code != 401 {
				t.Errorf("unauthenticated = %d", rec.Code)
			}
			if rec := serve(t, newStore(), with("tasks.manage", "organization.view"), r.method, r.path, r.body); rec.Code != 403 {
				t.Errorf("unrelated = %d", rec.Code)
			}
			if r.write {
				s := newStore()
				if rec := serve(t, s, with("products.view"), r.method, r.path, r.body); rec.Code != 403 || s.calls != 0 {
					t.Errorf("products.view on a write route = %d (calls %d)", rec.Code, s.calls)
				}
			}
			if rec := serve(t, newStore(), with("products.manage"), r.method, r.path, r.body); rec.Code == 401 || rec.Code == 403 || rec.Code >= 500 {
				t.Errorf("products.manage = %d: %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestErrorMappingAndValidation(t *testing.T) {
	cases := map[string]struct {
		method, path, body string
		err                error
		status             int
		code               string
	}{
		"duplicate":        {"POST", "/api/v1/manufacturers", `{"name":"x"}`, application.ErrConflict, 409, "products.conflict"},
		"bad reference":    {"POST", "/api/v1/products", `{"name":"x"}`, application.ErrReferenceNotFound, 400, "products.invalid_reference"},
		"not found":        {"POST", "/api/v1/product-categories", `{"name":"x"}`, application.ErrNotFound, 404, "products.not_found"},
		"internal":         {"POST", "/api/v1/manufacturers", `{"name":"x"}`, io.ErrUnexpectedEOF, 500, "platform.internal_error"},
		"blank name":       {"POST", "/api/v1/manufacturers", `{"name":" "}`, nil, 400, "products.invalid_request"},
		"unknown field":    {"POST", "/api/v1/products", `{"name":"x","active":false}`, nil, 400, "products.invalid_request"},
		"missing version":  {"PATCH", "/api/v1/manufacturers/x", `{"name":"x"}`, nil, 400, "products.invalid_request"},
		"missing version2": {"PATCH", pid, `{"name":"x"}`, nil, 400, "products.invalid_request"},
		"stale version":    {"PATCH", "/api/v1/manufacturers/x", `{"name":"y","expectedVersion":9}`, nil, 409, "products.version_conflict"},
		"bad limit":        {"GET", "/api/v1/products?limit=0", "", nil, 400, "products.invalid_limit"},
		"bad active":       {"GET", "/api/v1/products?active=maybe", "", nil, 400, "products.invalid_request"},
	}
	for name, c := range cases {
		s := newStore()
		s.err = c.err
		rec := serve(t, s, with("products.manage"), c.method, c.path, c.body)
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.code) {
			t.Errorf("%s: status=%d body=%s, want %d %s", name, rec.Code, rec.Body, c.status, c.code)
		}
		if c.err == io.ErrUnexpectedEOF && strings.Contains(rec.Body.String(), "unexpected EOF") {
			t.Errorf("internal error text leaked: %s", rec.Body)
		}
	}
}

func TestListFiltersAreForwarded(t *testing.T) {
	s := newStore()
	rec := serve(t, s, with("products.view"), "GET", "/api/v1/products?q=lat&categoryId=c1&manufacturerId=m1&active=false", "")
	if rec.Code != 200 || s.filter.TitlePrefix != "lat" || s.filter.CategoryID != "c1" || s.filter.ManufacturerID != "m1" || s.filter.Active == nil || *s.filter.Active {
		t.Errorf("status %d filter %+v", rec.Code, s.filter)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("responses must not be cached")
	}
}
