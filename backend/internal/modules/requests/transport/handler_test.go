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

	"github.com/jackc/pgx/v5"

	approvalspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/public"
	catalogpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/requests/application"
	taskspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

type fakeAuth struct {
	user  string
	perms map[string]struct{}
	ok    bool
}

func (f fakeAuth) Authenticate(*http.Request) (authorization.Principal, bool, error) {
	return authorization.Principal{UserID: f.user, Permissions: f.perms}, f.ok, nil
}

const me = "00000000-0000-7000-8000-0000000000a1"
const stranger = "00000000-0000-7000-8000-0000000000b9"

func as(user string, perms ...string) fakeAuth {
	m := map[string]struct{}{}
	for _, p := range perms {
		m[p] = struct{}{}
	}
	return fakeAuth{user: user, perms: m, ok: true}
}

type store struct {
	application.Store
	req   application.Request
	query application.ListQuery
}

func (s *store) Get(context.Context, string) (application.Request, error) { return s.req, nil }
func (s *store) References(context.Context, string) ([]catalogpublic.Reference, error) {
	return nil, nil
}
func (s *store) Tasks(context.Context, string) ([]application.RequestTask, error) { return nil, nil }
func (s *store) List(_ context.Context, q application.ListQuery) (application.Result, error) {
	s.query = q
	return application.Result{Items: []application.Request{s.req}}, nil
}
func (s *store) InTx(ctx context.Context, fn func(tx pgx.Tx) error) error { return fn(nil) }
func (s *store) LockTx(context.Context, pgx.Tx, string) (application.Request, error) {
	return s.req, nil
}
func (s *store) UpdateTx(_ context.Context, _ pgx.Tx, r application.Request) (application.Request, error) {
	r.Version++
	return r, nil
}

type catalog struct{ sub catalogpublic.Submission }

func (c catalog) ForSubmission(context.Context, string) (catalogpublic.Submission, error) {
	return c.sub, nil
}

type approvals struct{ canView bool }

func (approvals) RequestInTx(context.Context, pgx.Tx, approvalspublic.Caller, approvalspublic.Request) (string, error) {
	return "a1", nil
}
func (approvals) CancelBySubjectInTx(context.Context, pgx.Tx, approvalspublic.Caller, string, string) (int, error) {
	return 0, nil
}
func (approvals) ForSubject(context.Context, string, string) ([]approvalspublic.Approval, error) {
	return nil, nil
}
func (a approvals) CanView(context.Context, string, string, string) (bool, error) {
	return a.canView, nil
}

type tasks struct{}

func (tasks) CreateInTx(context.Context, pgx.Tx, taskspublic.Caller, taskspublic.CreateInput) (string, error) {
	return "t1", nil
}
func (tasks) CancelByContextInTx(context.Context, pgx.Tx, taskspublic.Caller, string, string, string) (int, error) {
	return 0, nil
}
func (tasks) Tasks(context.Context, []string) ([]taskspublic.Task, error) { return nil, nil }
func (tasks) StatusesInTx(context.Context, pgx.Tx, []string) (map[string]string, error) {
	return nil, nil
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
func (dir) UserNames(context.Context, []string) (map[string]string, error) {
	return map[string]string{me: "Ada"}, nil
}
func (dir) TeamNames(context.Context, []string) (map[string]string, error)  { return nil, nil }
func (dir) ManagerIDs(context.Context, []string) (map[string]string, error) { return nil, nil }

type lookups struct{}

func (lookups) ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error) {
	return dir{}.ActiveUsers(ctx, ids)
}
func (lookups) ActiveProducts(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func newReq() application.Request {
	def, _ := catalogpublic.ParseSnapshot([]byte(`{"fields":[{"key":"reason","type":"text","label":"Reason","required":true}],
		"approvals":[{"approverTeamId":"00000000-0000-7000-8000-0000000000e1"}],"fulfillment":[{"title":"Secret fulfillment"}]}`))
	return application.Request{
		ID: "r1", Reference: "REQ-2026-000001", CatalogItemTitle: "Laptop", Definition: def, Answers: map[string]any{"reason": "private reason"},
		RequesterID: me, RequestedForID: me, Status: application.StatusPendingApproval, Version: 1,
	}
}

func serve(t *testing.T, s *store, canView bool, a authorization.Authenticator, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	sub := catalogpublic.Submission{ID: "i1", Key: "laptop", Title: "Laptop", Active: true,
		Definition: catalogpublic.Definition{Fields: []catalogpublic.Field{{Key: "reason", Type: "text", Label: "Reason", Required: true}}}, Snapshot: []byte(`{"fields":[{"key":"reason","type":"text","label":"Reason","required":true}],"approvals":[],"fulfillment":[]}`)}
	svc := application.NewService(s, catalog{sub}, approvals{canView}, tasks{}, dir{}, lookups{}, lookups{}, nil)
	mux := http.NewServeMux()
	Register(mux, svc, a, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-ID", "req-1")
	var rd io.Reader
	if body != "" {
		rd = bytes.NewBufferString(body)
	}
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, rd))
	return rec
}

const rPath = "/api/v1/service-requests/00000000-0000-7000-8000-000000000001"

func TestEveryRouteNeedsASignedInUser(t *testing.T) {
	for _, r := range []struct{ method, path, body string }{
		{"POST", "/api/v1/service-requests", `{"catalogItemId":"i1","answers":{"reason":"x"}}`},
		{"GET", "/api/v1/service-requests", ""},
		{"GET", rPath, ""},
		{"POST", rPath + "/cancel", `{"reason":"x"}`},
		{"POST", rPath + "/hold", `{"reason":"stock"}`},
		{"POST", rPath + "/resume", `{}`},
		{"POST", rPath + "/complete", `{}`},
	} {
		if rec := serve(t, &store{req: newReq()}, false, authorization.DenyAll{}, r.method, r.path, r.body); rec.Code != 401 {
			t.Errorf("%s %s unauthenticated = %d", r.method, r.path, rec.Code)
		}
	}
}

func TestSubmitReturnsFieldErrorsAndHidesInternals(t *testing.T) {
	s := &store{req: newReq()}
	rec := serve(t, s, false, as(me), "POST", "/api/v1/service-requests", `{"catalogItemId":"i1","answers":{}}`)
	body := rec.Body.String()
	if rec.Code != 422 || !strings.Contains(body, "requests.invalid_answers") || !strings.Contains(body, `"reason":"required"`) {
		t.Errorf("status=%d body=%s", rec.Code, body)
	}
	if rec := serve(t, s, false, as(me), "POST", "/api/v1/service-requests", `{"catalogItemId":"i1","answers":{"reason":"x"},"status":"completed"}`); rec.Code != 400 {
		t.Errorf("unknown field = %d", rec.Code)
	}
	if rec := serve(t, s, false, as(me), "POST", "/api/v1/service-requests", `x`); rec.Code != 400 {
		t.Errorf("not json = %d", rec.Code)
	}
}

func TestRequestDetailIsOnlyForPeopleInvolvedAndHidesApprovalAndFulfillmentDefinition(t *testing.T) {
	s := &store{req: newReq()}
	if rec := serve(t, s, false, as(stranger), "GET", rPath, ""); rec.Code != 404 {
		t.Errorf("stranger = %d, want 404", rec.Code)
	}
	if rec := serve(t, s, true, as(stranger), "GET", rPath, ""); rec.Code != 200 {
		t.Errorf("an approver = %d", rec.Code)
	}
	if rec := serve(t, s, false, as(stranger, "requests.view"), "GET", rPath, ""); rec.Code != 200 {
		t.Errorf("requests.view = %d", rec.Code)
	}
	rec := serve(t, s, false, as(me), "GET", rPath, "")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "private reason") || !strings.Contains(body, `"label":"Reason"`) || !strings.Contains(body, `"actions":["cancel"]`) || !strings.Contains(body, `"Ada"`) {
		t.Fatalf("requester view = %d %s", rec.Code, body)
	}
	for _, leaked := range []string{"Secret fulfillment", "approverTeamId", "00000000-0000-7000-8000-0000000000e1"} {
		if strings.Contains(body, leaked) {
			t.Errorf("the detail leaks %q: %s", leaked, body)
		}
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("requests contain personal data and must not be cached")
	}
}

func TestListScopes(t *testing.T) {
	s := &store{req: newReq()}
	if rec := serve(t, s, false, as(me), "GET", "/api/v1/service-requests", ""); rec.Code != 200 || s.query.All || s.query.UserID != me {
		t.Errorf("default scope: %d %+v", rec.Code, s.query)
	}
	if rec := serve(t, s, false, as(me), "GET", "/api/v1/service-requests?scope=all", ""); rec.Code != 403 {
		t.Errorf("scope=all without requests.view = %d", rec.Code)
	}
	if rec := serve(t, s, false, as(me, "requests.view"), "GET", "/api/v1/service-requests?scope=all&status=waiting", ""); rec.Code != 200 || !s.query.All || s.query.Status != "waiting" {
		t.Errorf("scope=all with requests.view: %d %+v", rec.Code, s.query)
	}
	for name, target := range map[string]string{"bad scope": "?scope=x", "bad status": "?status=approved", "bad limit": "?limit=0"} {
		if rec := serve(t, s, false, as(me, "requests.view"), "GET", "/api/v1/service-requests"+target, ""); rec.Code != 400 {
			t.Errorf("%s = %d", name, rec.Code)
		}
	}
}

// Success paths write audit and outbox rows through a database transaction and
// are covered by the end-to-end workflow tests in cmd/turaco-worker; here only
// the refusals, which happen before any write, are checked.
func TestOperationsAreRefusedForTheWrongPrincipalOrStatus(t *testing.T) {
	s := &store{req: newReq()}
	if rec := serve(t, s, false, as(stranger), "POST", rPath+"/cancel", `{"reason":"x"}`); rec.Code != 404 {
		t.Errorf("stranger cancel = %d, want 404", rec.Code)
	}
	if rec := serve(t, s, false, as(me), "POST", rPath+"/cancel", `{}`); rec.Code != 400 {
		t.Errorf("cancel without reason = %d", rec.Code)
	}
	if rec := serve(t, s, false, as(me), "POST", rPath+"/cancel", `{"reason":"x","expectedVersion":9}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), "requests.version_conflict") {
		t.Errorf("stale version = %d %s", rec.Code, rec.Body)
	}
	for _, op := range []string{"hold", "resume", "complete"} {
		if rec := serve(t, s, false, as(me), "POST", rPath+"/"+op, `{"reason":"stock"}`); rec.Code != 403 {
			t.Errorf("%s without requests.manage = %d, want 403", op, rec.Code)
		}
	}
	if rec := serve(t, s, false, as(me, "requests.manage"), "POST", rPath+"/hold", `{"reason":"lunch"}`); rec.Code != 400 {
		t.Errorf("unknown waiting reason = %d", rec.Code)
	}
	if rec := serve(t, s, false, as(me, "requests.manage"), "POST", rPath+"/hold", `{"reason":"stock"}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), "requests.invalid_transition") {
		t.Errorf("hold while pending approval = %d %s", rec.Code, rec.Body)
	}
	s.req.Status = application.StatusCompleted
	if rec := serve(t, s, false, as(me, "requests.manage"), "POST", rPath+"/cancel", `{"reason":"x"}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), "requests.invalid_transition") {
		t.Errorf("cancel completed = %d %s", rec.Code, rec.Body)
	}
	s.req.Status = application.StatusInFulfillment
	if rec := serve(t, s, false, as(me), "POST", rPath+"/cancel", `{"reason":"x"}`); rec.Code != 403 {
		t.Errorf("the requester cancelling a request in fulfillment = %d, want 403", rec.Code)
	}
}
