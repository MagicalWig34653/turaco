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

	"github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type fakeAuth struct {
	perms map[string]struct{}
	user  string
	ok    bool
}

func (f fakeAuth) Authenticate(*http.Request) (authorization.Principal, bool, error) {
	return authorization.Principal{UserID: f.user, Permissions: f.perms}, f.ok, nil
}

func with(perms ...string) fakeAuth {
	m := map[string]struct{}{}
	for _, p := range perms {
		m[p] = struct{}{}
	}
	return fakeAuth{perms: m, user: "00000000-0000-7000-8000-0000000000a1", ok: true}
}

// stubStore records what the service asks and returns one fixed task.
type stubStore struct {
	application.Store // the transactional methods are not used by these tests
	task              application.Task
	calls             int
	query             application.ListQuery
}

func (s *stubStore) Insert(_ context.Context, _ application.Caller, n application.NewTask) (application.Task, error) {
	s.calls++
	t := s.task
	t.Title = n.Title
	return t, nil
}
func (s *stubStore) Get(context.Context, string) (application.Task, error) { return s.task, nil }
func (s *stubStore) Change(_ context.Context, _ application.Caller, _ string, decide func(application.Task) (application.Change, error)) (application.Task, error) {
	s.calls++
	ch, err := decide(s.task)
	if err != nil {
		return application.Task{}, err
	}
	if ch.NoChange {
		return s.task, nil
	}
	return ch.Next, nil
}
func (s *stubStore) List(_ context.Context, q application.ListQuery) (application.Result[application.Task], error) {
	s.query = q
	return application.Result[application.Task]{Items: []application.Task{s.task}}, nil
}

type stubDir struct{}

func (stubDir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	m := map[string]bool{}
	for _, i := range ids {
		m[i] = true
	}
	return m, nil
}
func (stubDir) ActiveTeams(ctx context.Context, ids []string) (map[string]bool, error) {
	return stubDir{}.ActiveUsers(ctx, ids)
}
func (stubDir) UserNames(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (stubDir) TeamNames(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (stubDir) CurrentTeamIDs(context.Context, string) ([]string, error) { return nil, nil }
func (stubDir) CurrentMemberIDs(context.Context, string) ([]string, error) {
	return nil, nil
}

func serve(t *testing.T, store *stubStore, a authorization.Authenticator, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, application.NewService(store, stubDir{}, nil), a, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-ID", "req-1")
	var rd io.Reader
	if body != "" {
		rd = bytes.NewBufferString(body)
	}
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, rd))
	return rec
}

func newStore() *stubStore {
	return &stubStore{task: application.Task{ID: "00000000-0000-7000-8000-000000000001", Title: "t", Status: "open", Priority: "normal", Version: 1}}
}

const taskPath = "/api/v1/tasks/00000000-0000-7000-8000-000000000001"

func TestRoutePermissions(t *testing.T) {
	routes := []struct {
		method, path, body string
		managerOnly        bool
	}{
		{"GET", "/api/v1/tasks", "", false},
		{"GET", "/api/v1/my-work", "", false},
		{"GET", taskPath, "", false},
		{"POST", "/api/v1/tasks", `{"title":"x"}`, true},
		{"PATCH", taskPath, `{"expectedVersion":1,"title":"x"}`, true},
		{"POST", taskPath + "/assign", `{"userId":"00000000-0000-7000-8000-0000000000b1"}`, true},
		{"POST", taskPath + "/unassign", `{}`, true},
		{"POST", taskPath + "/cancel", `{"reason":"r"}`, true},
		{"POST", taskPath + "/reopen", `{"reason":"r"}`, true},
		{"POST", taskPath + "/start", `{}`, false},
		{"POST", taskPath + "/block", `{"reason":"r"}`, false},
		{"POST", taskPath + "/unblock", `{}`, false},
		{"POST", taskPath + "/complete", `{}`, false},
	}
	for _, r := range routes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			if rec := serve(t, newStore(), authorization.DenyAll{}, r.method, r.path, r.body); rec.Code != 401 {
				t.Errorf("unauthenticated = %d, want 401", rec.Code)
			}
			if rec := serve(t, newStore(), with("organization.view", "tickets.view"), r.method, r.path, r.body); rec.Code != 403 {
				t.Errorf("unrelated permissions = %d, want 403", rec.Code)
			}
			if r.managerOnly {
				store := newStore()
				for _, p := range []string{"tasks.view", "tasks.work"} {
					if rec := serve(t, store, with(p), r.method, r.path, r.body); rec.Code != 403 {
						t.Errorf("%s on a manager-only route = %d, want 403", p, rec.Code)
					}
				}
				if store.calls != 0 {
					t.Errorf("store called %d times without tasks.manage", store.calls)
				}
			}
			// The fixed task is open, so some transitions are rejected with 409;
			// what matters here is that tasks.manage passes the permission gate.
			if rec := serve(t, newStore(), with("tasks.manage"), r.method, r.path, r.body); rec.Code == 401 || rec.Code == 403 || rec.Code >= 500 {
				t.Errorf("tasks.manage = %d: %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestWorkerSeesNothingElsesTask(t *testing.T) {
	// The task is assigned to somebody else: a worker gets 404, a viewer gets it.
	store := newStore()
	other := "00000000-0000-7000-8000-0000000000c1"
	store.task.AssignedUserID = &other
	if rec := serve(t, store, with("tasks.work"), "GET", taskPath, ""); rec.Code != 404 {
		t.Errorf("worker = %d, want 404", rec.Code)
	}
	if rec := serve(t, store, with("tasks.work"), "POST", taskPath+"/start", `{}`); rec.Code != 404 {
		t.Errorf("worker start = %d, want 404", rec.Code)
	}
	if rec := serve(t, store, with("tasks.view"), "GET", taskPath, ""); rec.Code != 200 {
		t.Errorf("viewer = %d, want 200", rec.Code)
	}
	if rec := serve(t, store, with("tasks.view"), "POST", taskPath+"/start", `{}`); rec.Code != 403 {
		t.Errorf("viewer start = %d, want 403", rec.Code)
	}
}

func TestWorkerCanStartOwnTask(t *testing.T) {
	store := newStore()
	me := "00000000-0000-7000-8000-0000000000a1"
	store.task.AssignedUserID = &me
	rec := serve(t, store, with("tasks.work"), "POST", taskPath+"/start", `{"expectedVersion":1}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"in_progress"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		name, method, path, body string
		mutate                   func(*stubStore)
		status                   int
		code                     string
	}{
		{"invalid transition", "POST", taskPath + "/complete", `{}`, func(s *stubStore) { s.task.Status = "cancelled" }, 409, "tasks.invalid_transition"},
		{"version conflict", "POST", taskPath + "/start", `{"expectedVersion":7}`, nil, 409, "tasks.version_conflict"},
		{"missing reason", "POST", taskPath + "/cancel", `{"reason":""}`, nil, 400, "tasks.invalid_request"},
		{"unknown field", "POST", taskPath + "/start", `{"status":"completed"}`, nil, 400, "tasks.invalid_request"},
		{"bad priority", "POST", "/api/v1/tasks", `{"title":"x","priority":"asap"}`, nil, 400, "tasks.invalid_request"},
		{"bad cursor", "GET", "/api/v1/tasks?cursor=%25%25", "", nil, 200, ""},
		{"bad limit", "GET", "/api/v1/tasks?limit=0", "", nil, 400, "tasks.invalid_limit"},
		{"bad status filter", "GET", "/api/v1/tasks?status=done", "", nil, 400, "tasks.invalid_request"},
		{"bad overdue", "GET", "/api/v1/tasks?overdue=maybe", "", nil, 400, "tasks.invalid_request"},
		{"empty update", "PATCH", taskPath, `{}`, nil, 400, "tasks.invalid_request"},
		{"assign nobody", "POST", taskPath + "/assign", `{}`, nil, 400, "tasks.invalid_request"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newStore()
			if c.mutate != nil {
				c.mutate(store)
			}
			rec := serve(t, store, with("tasks.manage"), c.method, c.path, c.body)
			if rec.Code != c.status || (c.code != "" && !strings.Contains(rec.Body.String(), c.code)) {
				t.Errorf("status=%d body=%s, want %d %s", rec.Code, rec.Body, c.status, c.code)
			}
		})
	}
}

func TestListPassesFiltersAndRestrictsWorkers(t *testing.T) {
	store := newStore()
	rec := serve(t, store, with("tasks.view"), "GET", "/api/v1/tasks?status=open,blocked&status=in_progress&priority=high&overdue=true&q=ab", "")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := strings.Join(store.query.Statuses, ","); got != "open,blocked,in_progress" || store.query.Priority != "high" || !store.query.Overdue || store.query.TitlePrefix != "ab" {
		t.Errorf("query = %+v", store.query)
	}
	if store.query.Mine != nil {
		t.Error("a viewer must not be restricted to its own tasks")
	}
	serve(t, store, with("tasks.work"), "GET", "/api/v1/tasks", "")
	if store.query.Mine == nil {
		t.Error("a worker must always be restricted to its own tasks")
	}
	serve(t, store, with("tasks.view"), "GET", "/api/v1/tasks?mine=true", "")
	if store.query.Mine == nil {
		t.Error("mine=true must restrict a viewer to its own tasks")
	}
}

func TestResponsesAreNotCached(t *testing.T) {
	rec := serve(t, newStore(), with("tasks.view"), "GET", "/api/v1/tasks", "")
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
}

func TestDueAtNullClearsAndAbsentKeeps(t *testing.T) {
	var b updateBody
	if err := decodeBodyForTest(`{"title":"x"}`, &b); err != nil || b.DueAt.Set {
		t.Errorf("absent dueAt: %+v %v", b.DueAt, err)
	}
	b = updateBody{}
	if err := decodeBodyForTest(`{"dueAt":null}`, &b); err != nil || !b.DueAt.Set || b.DueAt.Value != nil {
		t.Errorf("null dueAt: %+v %v", b.DueAt, err)
	}
	b = updateBody{}
	if err := decodeBodyForTest(`{"dueAt":"2026-11-01T09:00:00+01:00"}`, &b); err != nil || b.DueAt.Value == nil {
		t.Errorf("dueAt: %+v %v", b.DueAt, err)
	}
	if err := decodeBodyForTest(`{"dueAt":"tomorrow"}`, &updateBody{}); err == nil {
		t.Error("invalid dueAt must be rejected")
	}
}

func decodeBodyForTest(body string, dst any) error {
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	return httpx.DecodeJSON(httptest.NewRecorder(), req, dst, maxBody)
}

func TestPatchRequiresTheExpectedVersion(t *testing.T) {
	store := newStore()
	rec := serve(t, store, with("tasks.manage"), "PATCH", taskPath, `{"title":"x"}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "expectedVersion") || store.calls != 0 {
		t.Errorf("status=%d calls=%d body=%s, want 400 without touching the store", rec.Code, store.calls, rec.Body)
	}
	if rec := serve(t, store, with("tasks.manage"), "PATCH", taskPath, `{"expectedVersion":1,"title":"x"}`); rec.Code != 200 {
		t.Errorf("with the version = %d: %s", rec.Code, rec.Body)
	}
}
