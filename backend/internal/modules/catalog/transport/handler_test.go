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

	"github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

type fakeAuth struct {
	perms map[string]struct{}
	ok    bool
}

func (f fakeAuth) Authenticate(*http.Request) (authorization.Principal, bool, error) {
	return authorization.Principal{UserID: "00000000-0000-7000-8000-0000000000a1", Permissions: f.perms}, f.ok, nil
}

func as(perms ...string) fakeAuth {
	m := map[string]struct{}{}
	for _, p := range perms {
		m[p] = struct{}{}
	}
	return fakeAuth{perms: m, ok: true}
}

type store struct {
	application.Store
	item  application.Item
	calls int
	err   error
}

func (s *store) Insert(_ context.Context, _ application.Caller, n application.NewItem) (application.Item, error) {
	s.calls++
	return application.Item{ID: "1", Key: n.Key, Title: n.Title, Active: true, Version: 1}, s.err
}
func (s *store) Get(context.Context, string) (application.Item, error) { return s.item, nil }
func (s *store) Change(_ context.Context, _ application.Caller, _ string, decide func(application.Item) (application.Change, error)) (application.Item, error) {
	s.calls++
	ch, err := decide(s.item)
	return ch.Next, err
}
func (s *store) List(context.Context, application.ListQuery) (application.Result, error) {
	return application.Result{Items: []application.Item{s.item}}, nil
}

type dir struct{}

func (dir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	m := map[string]bool{}
	for _, i := range ids {
		m[i] = true
	}
	return m, nil
}
func (dir) ActiveTeams(ctx context.Context, ids []string) (map[string]bool, error) {
	return dir{}.ActiveUsers(ctx, ids)
}

type prods struct{}

func (prods) ActiveProducts(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (prods) ProductNames(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (prods) CategoriesExist(_ context.Context, ids []string) (map[string]bool, error) {
	m := map[string]bool{}
	for _, i := range ids {
		m[i] = true
	}
	return m, nil
}
func (prods) ActiveInCategory(context.Context, string, int) ([]application.ProductChoice, error) {
	return []application.ProductChoice{{ID: "p1", Name: "Latitude"}}, nil
}

func serve(t *testing.T, s *store, a authorization.Authenticator, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, application.NewService(s, dir{}, prods{}), a, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-ID", "req-1")
	var rd io.Reader
	if body != "" {
		rd = bytes.NewBufferString(body)
	}
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, rd))
	return rec
}

const catID = "00000000-0000-7000-8000-0000000000c1"
const teamID = "00000000-0000-7000-8000-0000000000e1"
const itemPath = "/api/v1/catalog-items/00000000-0000-7000-8000-000000000001"

func newStore() *store {
	def, _ := application.ParseDefinition([]byte(`{"fields":[
		{"key":"reason","type":"text","label":"Reason","required":true},
		{"key":"device","type":"product","label":"Device","categoryId":"` + catID + `"}],
		"approvals":[{"approverTeamId":"` + teamID + `"}],"fulfillment":[{"title":"Prepare","assignedTeamId":"` + teamID + `"}],"allowRequestedFor":true}`))
	return &store{item: application.Item{ID: "1", Key: "laptop", Title: "Laptop", Definition: def, Active: true, Version: 1}}
}

const createBodyOK = `{"key":"laptop","title":"Laptop","description":"d","definition":{"fields":[]}}`

func TestRoutePermissions(t *testing.T) {
	for _, r := range []struct {
		method, path, body string
		manage             bool
	}{
		{"GET", "/api/v1/catalog-items", "", false},
		{"GET", itemPath, "", false},
		{"POST", "/api/v1/catalog-items", createBodyOK, true},
		{"PATCH", itemPath, `{"expectedVersion":1,"title":"x"}`, true},
		{"POST", itemPath + "/activate", `{}`, true},
		{"POST", itemPath + "/deactivate", `{}`, true},
	} {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			if rec := serve(t, newStore(), authorization.DenyAll{}, r.method, r.path, r.body); rec.Code != 401 {
				t.Errorf("unauthenticated = %d", rec.Code)
			}
			if r.manage {
				s := newStore()
				if rec := serve(t, s, as("products.manage", "tasks.manage"), r.method, r.path, r.body); rec.Code != 403 || s.calls != 0 {
					t.Errorf("without catalog.manage = %d (calls %d)", rec.Code, s.calls)
				}
				if rec := serve(t, newStore(), as("catalog.manage"), r.method, r.path, r.body); rec.Code == 401 || rec.Code == 403 || rec.Code >= 500 {
					t.Errorf("with catalog.manage = %d: %s", rec.Code, rec.Body)
				}
			} else if rec := serve(t, newStore(), as(), r.method, r.path, r.body); rec.Code != 200 {
				t.Errorf("any signed-in user = %d: %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestEmployeesGetTheFormButNotApprovalOrFulfillmentDetails(t *testing.T) {
	rec := serve(t, newStore(), as(), "GET", itemPath, "")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"label":"Reason"`) || !strings.Contains(body, `"productOptions":[{"id":"p1","name":"Latitude"}]`) || !strings.Contains(body, `"allowRequestedFor":true`) {
		t.Fatalf("employee form = %d %s", rec.Code, body)
	}
	for _, leaked := range []string{"approvals", "fulfillment", teamID, "Prepare", catID, `"definition"`} {
		if strings.Contains(body, leaked) {
			t.Errorf("the employee form leaks %q: %s", leaked, body)
		}
	}
	mgr := serve(t, newStore(), as("catalog.manage"), "GET", itemPath, "").Body.String()
	for _, want := range []string{`"definition"`, "approvals", "fulfillment", teamID} {
		if !strings.Contains(mgr, want) {
			t.Errorf("the manager view lacks %q: %s", want, mgr)
		}
	}
}

func TestInactiveItemsAreHiddenFromEmployees(t *testing.T) {
	s := newStore()
	s.item.Active = false
	if rec := serve(t, s, as(), "GET", itemPath, ""); rec.Code != 404 {
		t.Errorf("employee = %d", rec.Code)
	}
	if rec := serve(t, s, as("catalog.manage"), "GET", itemPath, ""); rec.Code != 200 {
		t.Errorf("manager = %d", rec.Code)
	}
}

func TestErrorMappingAndBodyValidation(t *testing.T) {
	cases := map[string]struct {
		method, path, body string
		err                error
		status             int
		code               string
	}{
		"duplicate key":    {"POST", "/api/v1/catalog-items", createBodyOK, application.ErrConflict, 409, "catalog.conflict"},
		"bad definition":   {"POST", "/api/v1/catalog-items", `{"key":"laptop","title":"x","definition":{"fields":[{"key":"A"}]}}`, nil, 400, "catalog.invalid_request"},
		"dangling ref":     {"POST", "/api/v1/catalog-items", `{"key":"laptop","title":"x","definition":{"approvals":[{"approverUserId":"00000000-0000-7000-8000-0000000000ee"}]}}`, nil, 201, ""},
		"unknown property": {"POST", "/api/v1/catalog-items", `{"key":"laptop","title":"x","definition":{},"active":false}`, nil, 400, "catalog.invalid_request"},
		"missing version":  {"PATCH", itemPath, `{"title":"x"}`, nil, 400, "catalog.invalid_request"},
		"stale version":    {"PATCH", itemPath, `{"expectedVersion":9,"title":"x"}`, nil, 409, "catalog.version_conflict"},
		"bad limit":        {"GET", "/api/v1/catalog-items?limit=0", "", nil, 400, "catalog.invalid_limit"},
		"internal":         {"POST", "/api/v1/catalog-items", createBodyOK, io.ErrUnexpectedEOF, 500, "platform.internal_error"},
	}
	for name, c := range cases {
		s := newStore()
		s.err = c.err
		rec := serve(t, s, as("catalog.manage"), c.method, c.path, c.body)
		if rec.Code != c.status || (c.code != "" && !strings.Contains(rec.Body.String(), c.code)) {
			t.Errorf("%s: status=%d body=%s, want %d %s", name, rec.Code, rec.Body, c.status, c.code)
		}
		if c.err == io.ErrUnexpectedEOF && strings.Contains(rec.Body.String(), "unexpected EOF") {
			t.Errorf("internal error text leaked: %s", rec.Body)
		}
	}
}

func TestResponsesAreNotCached(t *testing.T) {
	if rec := serve(t, newStore(), as(), "GET", "/api/v1/catalog-items", ""); rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
}
