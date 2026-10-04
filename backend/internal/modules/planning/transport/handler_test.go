package transport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/planning/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/planning/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/planning/transport"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
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

const (
	manager  = "00000000-0000-7000-8000-0000000000d3"
	owner    = "00000000-0000-7000-8000-0000000000d5"
	outsider = "00000000-0000-7000-8000-0000000000d4"
	chgID    = "00000000-0000-7000-8000-0000000000e1"
)

type dir struct{}

func (dir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
func (dir) ActiveTeams(context.Context, []string) (map[string]bool, error)     { return nil, nil }
func (dir) LocationNames(context.Context, []string) (map[string]string, error) { return nil, nil }
func (dir) UserNames(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		out[id] = "Some User"
	}
	return out, nil
}

type changes struct{}

func window() (*time.Time, *time.Time) {
	s := time.Date(2099, 1, 10, 10, 0, 0, 0, time.UTC)
	e := s.Add(2 * time.Hour)
	return &s, &e
}

func (changes) Lookup(_ context.Context, ids []string) (map[string]application.ChangeInfo, error) {
	out := map[string]application.ChangeInfo{}
	for _, id := range ids {
		if id == chgID {
			s, e := window()
			out[id] = application.ChangeInfo{ID: id, Reference: "CHG-000001", Title: "Firmware", Status: "scheduled", RequesterID: manager, WindowStart: s, WindowEnd: e}
		}
	}
	return out, nil
}
func (c changes) Calendar(ctx context.Context, _, _ time.Time, _ int) ([]application.ChangeCalendarEntry, bool, error) {
	found, _ := c.Lookup(ctx, []string{chgID})
	return []application.ChangeCalendarEntry{{Change: found[chgID]}}, false, nil
}

type tasks struct{}

func (tasks) Tasks(context.Context, []string) ([]application.TaskInfo, error) { return nil, nil }

type procurement struct{}

func (procurement) Requests(context.Context, []string) (map[string]application.RequestInfo, error) {
	return nil, nil
}

type services struct{}

func (services) Lookup(context.Context, []string) (map[string]application.ServiceInfo, error) {
	return nil, nil
}

type approvals struct{}

func (approvals) RequestInTx(ctx context.Context, tx pgx.Tx, _ audit.Actor, _ string, _ application.ApprovalRequest) (string, error) {
	var id string
	return id, tx.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id)
}
func (approvals) CancelBySubjectInTx(context.Context, pgx.Tx, audit.Actor, string, string) error {
	return nil
}
func (approvals) ForSubject(context.Context, string) ([]application.ApprovalInfo, error) {
	return nil, nil
}
func (approvals) CanView(context.Context, string, string) (bool, error) { return false, nil }

func serve(t *testing.T, a authorization.Authenticator) http.Handler {
	t.Helper()
	pool := dbtest.Pool(t)
	reg := relationships.NewRegistry()
	reg.Register(application.Triples...)
	svc := application.NewService(repository.New(pool), relationships.New(reg), dir{}, changes{}, tasks{}, procurement{}, services{}, approvals{})
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	transport.Register(mux, svc, a, logger)
	t.Cleanup(func() {
		ctx := context.Background()
		mine := `SELECT id FROM planning.initiatives WHERE title LIKE 'http-%'`
		_, _ = pool.Exec(ctx, `DELETE FROM platform.relationships WHERE source_type = 'initiative' AND source_id IN (`+mine+`)`)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE actor_id = ANY($1::uuid[])`, []string{manager, owner, outsider})
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE actor_id = ANY($1::uuid[])`, []string{manager, owner, outsider})
		conn, err := pool.Acquire(ctx)
		if err != nil {
			return
		}
		defer conn.Release()
		_, _ = conn.Exec(ctx, `SET session_replication_role = replica`)
		_, _ = conn.Exec(ctx, `DELETE FROM planning.initiative_transitions WHERE initiative_id IN (`+mine+`)`)
		_, _ = conn.Exec(ctx, `DELETE FROM planning.milestones WHERE initiative_id IN (`+mine+`)`)
		_, _ = conn.Exec(ctx, `DELETE FROM planning.initiatives WHERE title LIKE 'http-%'`)
		_, _ = conn.Exec(ctx, `RESET session_replication_role`)
	})
	return httpx.Middleware(logger, mux)
}

func do(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func errCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func TestRoutesRequireAuthentication(t *testing.T) {
	h := serve(t, fakeAuth{})
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/v1/initiatives"}, {"POST", "/api/v1/initiatives"}, {"GET", "/api/v1/initiatives/" + chgID},
		{"POST", "/api/v1/initiatives/" + chgID + "/activate"}, {"GET", "/api/v1/maintenance-calendar"},
	} {
		if code, _ := do(t, h, r.method, r.path, "{}"); code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d", r.method, r.path, code)
		}
	}
}

func TestHTTPInitiativeAndCalendar(t *testing.T) {
	h := serve(t, as(manager, "planning.manage", "changes.view"))
	hOwner := serve(t, as(owner))
	hOut := serve(t, as(outsider))

	if code, _ := do(t, hOut, "POST", "/api/v1/initiatives", `{"title":"http-x"}`); code != http.StatusForbidden {
		t.Errorf("create without manage = %d", code)
	}
	if code, body := do(t, h, "POST", "/api/v1/initiatives", `{"title":"http-x","targetDate":"31.12.2027"}`); code != http.StatusBadRequest || errCode(body) != "planning.invalid_request" {
		t.Errorf("bad date = %d %v", code, body)
	}
	code, body := do(t, h, "POST", "/api/v1/initiatives", `{"title":"http-refresh","goal":"g","ownerUserId":"`+owner+`","targetDate":"2027-12-31"}`)
	if code != http.StatusCreated || body["targetDate"] != "2027-12-31" || body["status"] != "idea" {
		t.Fatalf("create = %d %v", code, body)
	}
	id := body["id"].(string)
	base := "/api/v1/initiatives/" + id
	if code, body := do(t, h, "POST", base+"/start-planning", `{}`); code != http.StatusBadRequest {
		t.Errorf("without version = %d %v", code, body)
	}
	if code, body := do(t, h, "POST", base+"/start-planning", `{"expectedVersion":7}`); code != http.StatusConflict || errCode(body) != "planning.version_conflict" {
		t.Errorf("stale version = %d %v", code, body)
	}
	if code, body := do(t, h, "POST", base+"/activate", `{"expectedVersion":1}`); code != http.StatusConflict || errCode(body) != "planning.invalid_transition" {
		t.Errorf("activate idea = %d %v", code, body)
	}
	if code, body := do(t, h, "POST", base+"/start-planning", `{"expectedVersion":1}`); code != http.StatusOK || body["status"] != "planning" {
		t.Fatalf("start planning = %d %v", code, body)
	}
	if code, body := do(t, h, "POST", base+"/milestones", `{"expectedVersion":2,"title":"Design","dueDate":"2027-06-30"}`); code != http.StatusCreated || body["dueDate"] != "2027-06-30" {
		t.Errorf("add milestone = %d %v", code, body)
	}
	if code, body := do(t, h, "POST", base+"/items", `{"expectedVersion":2,"type":"change","id":"`+chgID+`"}`); code != http.StatusCreated {
		t.Errorf("add item = %d %v", code, body)
	}
	if code, _ := do(t, h, "DELETE", base+"/items/change/"+chgID, ""); code != http.StatusBadRequest {
		t.Errorf("remove without version = %d", code)
	}
	// Reads: the owner sees it, an outsider gets 404.
	if code, body := do(t, hOwner, "GET", base, ""); code != http.StatusOK || body["allowedOperations"] != nil {
		t.Errorf("owner get = %d %v", code, body)
	}
	if code, body := do(t, hOut, "GET", base, ""); code != http.StatusNotFound || errCode(body) != "planning.not_found" {
		t.Errorf("outsider get = %d %v", code, body)
	}
	code, body = do(t, h, "GET", base, "")
	items, _ := body["items"].(map[string]any)
	list, _ := items["items"].([]any)
	if code != http.StatusOK || len(list) != 1 || len(body["milestones"].([]any)) != 1 {
		t.Fatalf("get = %d %v", code, body)
	}
	if code, body := do(t, h, "GET", "/api/v1/initiatives?q=http-refresh&status=planning", ""); code != http.StatusOK || len(body["items"].([]any)) == 0 {
		t.Errorf("list = %d %v", code, body)
	}
	// Calendar.
	if code, _ := do(t, hOut, "GET", "/api/v1/maintenance-calendar?from=2099-01-01T00:00:00Z&to=2099-02-01T00:00:00Z", ""); code != http.StatusForbidden {
		t.Errorf("calendar without permission = %d", code)
	}
	if code, body := do(t, h, "GET", "/api/v1/maintenance-calendar?from=2099-01-01T00:00:00Z&to=2099-06-01T00:00:00Z", ""); code != http.StatusBadRequest {
		t.Errorf("calendar range = %d %v", code, body)
	}
	if code, _ := do(t, h, "GET", "/api/v1/maintenance-calendar?from=yesterday&to=2099-02-01T00:00:00Z", ""); code != http.StatusBadRequest {
		t.Errorf("calendar time = %d", code)
	}
	code, body = do(t, h, "GET", "/api/v1/maintenance-calendar?from=2099-01-01T00:00:00Z&to=2099-02-01T00:00:00Z", "")
	cal, _ := body["items"].([]any)
	if code != http.StatusOK || len(cal) != 1 {
		t.Fatalf("calendar = %d %v", code, body)
	}
	entry := cal[0].(map[string]any)
	inits, _ := entry["initiatives"].([]any)
	if entry["title"] != "Firmware" || len(inits) != 1 {
		t.Errorf("calendar entry = %v", entry)
	}
	viewer := serve(t, as(outsider, "planning.view"))
	_, body = do(t, viewer, "GET", "/api/v1/maintenance-calendar?from=2099-01-01T00:00:00Z&to=2099-02-01T00:00:00Z", "")
	if e := body["items"].([]any)[0].(map[string]any); e["title"] != nil {
		t.Errorf("title leaked to planning.view only: %v", e)
	}
}
