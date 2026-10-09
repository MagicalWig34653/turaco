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

	"github.com/MagicalWig34653/turaco/backend/internal/modules/changes/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/changes/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/changes/transport"
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
	manager  = "00000000-0000-7000-8000-0000000000c3"
	assessor = "00000000-0000-7000-8000-0000000000c5"
	outsider = "00000000-0000-7000-8000-0000000000c4"
	svcID    = "00000000-0000-7000-8000-0000000000a1"
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
func (dir) ActiveLocations(context.Context, []string) (map[string]bool, error) { return nil, nil }
func (dir) LocationNames(context.Context, []string) (map[string]string, error) { return nil, nil }
func (dir) UserNames(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		out[id] = "Some User"
	}
	return out, nil
}
func (dir) CurrentMemberIDs(context.Context, string) ([]string, error) { return nil, nil }

type services struct{}

func (services) Lookup(_ context.Context, ids []string) (map[string]application.ServiceInfo, error) {
	out := map[string]application.ServiceInfo{}
	for _, id := range ids {
		if id == svcID {
			out[id] = application.ServiceInfo{ID: id, Reference: "SVC-000001", Name: "http-service", Status: "operational"}
		}
	}
	return out, nil
}
func (services) Search(_ context.Context, text string, _ int) ([]application.LookupHit, error) {
	if strings.Contains("http-service", strings.ToLower(text)) {
		return []application.LookupHit{{Type: "service", ID: svcID, Reference: "SVC-000001", Name: "http-service", Detail: "high"}}, nil
	}
	return nil, nil
}
func (services) Impact(_ context.Context, _ application.Principal, t, id string, _ int) (application.Impact, error) {
	return application.Impact{Type: t, ID: id}, nil
}

type infra struct{}

func (infra) VMs(context.Context, []string) (map[string]application.VMInfo, error) { return nil, nil }

type assets struct{}

func (assets) Assets(context.Context, []string) (map[string]application.AssetInfo, error) {
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

type tasks struct{}

func (tasks) CreateInTx(ctx context.Context, tx pgx.Tx, _ audit.Actor, _ string, _ application.TaskInput) (string, error) {
	var id string
	return id, tx.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id)
}
func (tasks) CancelByContextInTx(context.Context, pgx.Tx, audit.Actor, string, string, string) (int, error) {
	return 0, nil
}
func (tasks) StatusesInTx(context.Context, pgx.Tx, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (tasks) Tasks(context.Context, []string) ([]application.TaskInfo, error) { return nil, nil }

func serve(t *testing.T, a authorization.Authenticator) http.Handler {
	t.Helper()
	pool := dbtest.Pool(t)
	reg := relationships.NewRegistry()
	reg.Register(application.Triples...)
	svc := application.NewService(repository.New(pool), relationships.New(reg), dir{}, services{}, infra{}, assets{}, approvals{}, tasks{})
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	transport.Register(mux, svc, a, logger)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM platform.relationships WHERE source_type = 'change' AND source_id IN (SELECT id FROM changes.changes WHERE title LIKE 'http-%')`)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE actor_id = ANY($1::uuid[])`, []string{manager, assessor, outsider})
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE actor_id = ANY($1::uuid[])`, []string{manager, assessor, outsider})
		// Transitions and Task links are append-only: skip the triggers on this connection only.
		conn, err := pool.Acquire(ctx)
		if err != nil {
			return
		}
		defer conn.Release()
		_, _ = conn.Exec(ctx, `SET session_replication_role = replica`)
		for _, table := range []string{"change_transitions", "change_tasks"} {
			_, _ = conn.Exec(ctx, `DELETE FROM changes.`+table+` WHERE change_id IN (SELECT id FROM changes.changes WHERE title LIKE 'http-%')`)
		}
		_, _ = conn.Exec(ctx, `DELETE FROM changes.changes WHERE title LIKE 'http-%'`)
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
		{"GET", "/api/v1/changes"}, {"POST", "/api/v1/changes"}, {"GET", "/api/v1/changes/" + svcID}, {"POST", "/api/v1/changes/" + svcID + "/start"},
		{"GET", "/api/v1/changes/" + svcID + "/impact"}, {"GET", "/api/v1/changes/" + svcID + "/transitions"},
	} {
		if code, _ := do(t, h, r.method, r.path, "{}"); code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d", r.method, r.path, code)
		}
	}
}

func TestHTTPLifecycleAndAccess(t *testing.T) {
	mgr := as(manager, "changes.manage", "services.view")
	h := serve(t, mgr)
	hOut := serve(t, as(outsider))
	hExec := serve(t, as(outsider, "changes.execute"))

	if code, body := do(t, hOut, "POST", "/api/v1/changes", `{"title":"http-x","kind":"normal"}`); code != http.StatusForbidden {
		t.Fatalf("create without manage = %d %v", code, body)
	}
	if code, _ := do(t, h, "POST", "/api/v1/changes", `{"title":"http-x","kind":"normal","bogus":1}`); code != http.StatusBadRequest {
		t.Errorf("unknown field = %d", code)
	}
	if code, body := do(t, h, "POST", "/api/v1/changes", `{"title":"http-x","kind":"nope"}`); code != http.StatusBadRequest || errCode(body) != "changes.invalid_request" {
		t.Errorf("invalid kind = %d %v", code, body)
	}
	code, body := do(t, h, "POST", "/api/v1/changes", `{"title":"http-upgrade","kind":"normal","risk":"low","ownerUserId":"`+outsider+`","window":{"start":"2099-01-01T10:00:00Z","end":"2099-01-01T12:00:00Z"}}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, body)
	}
	id, _ := body["id"].(string)
	version := int(body["version"].(float64))
	base := "/api/v1/changes/" + id

	if code, _ := do(t, h, "PATCH", base, `{"title":"http-upgrade 2"}`); code != http.StatusBadRequest {
		t.Errorf("patch without expectedVersion = %d", code)
	}
	if code, body := do(t, h, "POST", base+"/affected", `{"type":"service","id":"`+svcID+`"}`); code != http.StatusCreated {
		t.Fatalf("add affected = %d %v", code, body)
	}
	if code, body := do(t, h, "POST", base+"/affected", `{"type":"service","id":"`+svcID+`"}`); code != http.StatusOK || body["created"] != false {
		t.Errorf("repeat add affected = %d %v", code, body)
	}
	if code, body := do(t, h, "POST", base+"/affected", `{"type":"vm","id":"`+svcID+`"}`); code != http.StatusBadRequest || errCode(body) != "changes.invalid_reference" {
		t.Errorf("vm without infrastructure.view = %d %v", code, body)
	}
	_ = version
	code, body = do(t, h, "GET", base, "")
	if code != http.StatusOK || len(body["affected"].([]any)) != 1 || body["allowedOperations"] == nil {
		t.Fatalf("get = %d %v", code, body)
	}
	// ver reads the current version; every lifecycle operation requires it.
	ver := func() string {
		t.Helper()
		_, b := do(t, h, "GET", base, "")
		return itoa(int(b["version"].(float64)))
	}
	// Removing an affected resource takes the version as a query parameter.
	if code, body := do(t, h, "DELETE", base+"/affected/service/"+svcID, ""); code != http.StatusBadRequest || errCode(body) != "changes.invalid_request" {
		t.Errorf("remove affected without expectedVersion = %d %v", code, body)
	}
	if code, body := do(t, h, "DELETE", base+"/affected/service/"+svcID+"?expectedVersion=x", ""); code != http.StatusBadRequest {
		t.Errorf("remove affected with a malformed version = %d %v", code, body)
	}
	if code, body := do(t, h, "DELETE", base+"/affected/vm/"+svcID+"?expectedVersion="+ver(), ""); code != http.StatusBadRequest || errCode(body) != "changes.invalid_reference" {
		t.Errorf("remove a hidden type = %d %v", code, body)
	}
	if code, body := do(t, h, "POST", base+"/submit", `{}`); code != http.StatusBadRequest || errCode(body) != "changes.invalid_request" {
		t.Errorf("submit without expectedVersion = %d %v", code, body)
	}
	if code, body := do(t, h, "POST", base+"/submit", `{"expectedVersion":`+ver()+`}`); code != http.StatusOK || body["status"] != "assessment" {
		t.Fatalf("submit = %d %v", code, body)
	}
	if code, body := do(t, h, "POST", base+"/submit", `{"expectedVersion":`+ver()+`}`); code != http.StatusConflict || errCode(body) != "changes.invalid_transition" {
		t.Errorf("second submit = %d %v", code, body)
	}
	// The requester never assesses their own change.
	if code, body := do(t, h, "POST", base+"/assess", `{"expectedVersion":`+ver()+`,"risk":"low"}`); code != http.StatusForbidden || errCode(body) != "changes.separation_of_duties" {
		t.Errorf("assess by the requester = %d %v", code, body)
	}
	hAssess := serve(t, as(assessor, "changes.manage"))
	if code, body := do(t, hAssess, "POST", base+"/assess", `{"expectedVersion":`+ver()+`,"risk":"low"}`); code != http.StatusOK || body["status"] != "approved" {
		t.Fatalf("assess = %d %v", code, body)
	}
	if code, body := do(t, h, "POST", base+"/schedule", `{"expectedVersion":`+ver()+`}`); code != http.StatusOK || body["status"] != "scheduled" {
		t.Fatalf("schedule = %d %v", code, body)
	}

	// The owner sees and runs it without any permission; strangers get 404; an executor can start it too.
	hOwner := serve(t, as(outsider))
	if code, _ := do(t, hOwner, "GET", base, ""); code != http.StatusOK {
		t.Errorf("owner get = %d", code)
	}
	hStranger := serve(t, as("00000000-0000-7000-8000-0000000000d9"))
	if code, body := do(t, hStranger, "GET", base, ""); code != http.StatusNotFound || errCode(body) != "changes.not_found" {
		t.Errorf("stranger get = %d %v", code, body)
	}
	if code, _ := do(t, hStranger, "POST", base+"/start", `{"expectedVersion":`+ver()+`}`); code != http.StatusNotFound {
		t.Errorf("stranger start = %d", code)
	}
	if code, _ := do(t, hStranger, "GET", base+"/transitions", ""); code != http.StatusNotFound {
		t.Errorf("stranger transitions = %d", code)
	}
	if code, body := do(t, hStranger, "GET", "/api/v1/changes", ""); code != http.StatusOK || len(body["items"].([]any)) != 0 {
		t.Errorf("stranger list = %d %v", code, body)
	}
	if code, body := do(t, hOwner, "GET", "/api/v1/changes?status=scheduled", ""); code != http.StatusOK || len(body["items"].([]any)) != 1 {
		t.Errorf("owner list = %d %v", code, body)
	}
	if code, _ := do(t, hOwner, "GET", "/api/v1/changes?status=nope", ""); code != http.StatusBadRequest {
		t.Errorf("bad status filter = %d", code)
	}
	if code, _ := do(t, hOwner, "GET", "/api/v1/changes?windowFrom=yesterday", ""); code != http.StatusBadRequest {
		t.Errorf("bad time filter = %d", code)
	}
	if code, _ := do(t, hOwner, "POST", base+"/cancel", `{"expectedVersion":`+ver()+`,"reason":"other"}`); code != http.StatusForbidden {
		t.Errorf("owner cancel without manage = %d", code)
	}
	if code, body := do(t, hOwner, "GET", base+"/impact", ""); code != http.StatusForbidden {
		t.Errorf("impact without services.view = %d %v", code, body)
	}
	if code, body := do(t, h, "GET", base+"/impact?depth=9", ""); code != http.StatusBadRequest {
		t.Errorf("impact depth 9 = %d %v", code, body)
	}
	if code, body := do(t, h, "GET", base+"/impact", ""); code != http.StatusOK || len(body["starts"].([]any)) != 1 {
		t.Errorf("impact = %d %v", code, body)
	}
	if code, body := do(t, hExec, "POST", base+"/start", `{}`); code != http.StatusBadRequest || errCode(body) != "changes.invalid_request" {
		t.Errorf("start without expectedVersion = %d %v", code, body)
	}
	if code, body := do(t, hExec, "POST", base+"/start", `{"expectedVersion":`+ver()+`}`); code != http.StatusOK || body["status"] != "in_progress" {
		t.Fatalf("executor start = %d %v", code, body)
	}
	if code, body := do(t, hOwner, "POST", base+"/tasks", `{"title":"step one"}`); code != http.StatusBadRequest || errCode(body) != "changes.invalid_request" {
		t.Errorf("add task without expectedVersion = %d %v", code, body)
	}
	if code, body := do(t, hOwner, "POST", base+"/tasks", `{"expectedVersion":`+ver()+`,"title":"step one"}`); code != http.StatusCreated || body["taskId"] == nil {
		t.Errorf("add task = %d %v", code, body)
	}
	if code, body := do(t, hOwner, "POST", base+"/fail", `{"expectedVersion":`+ver()+`,"reason":"bogus"}`); code != http.StatusBadRequest {
		t.Errorf("fail with bad reason = %d %v", code, body)
	}
	if code, body := do(t, hOwner, "POST", base+"/complete", `{"expectedVersion":`+ver()+`}`); code != http.StatusOK || body["status"] != "completed" {
		t.Errorf("complete = %d %v", code, body)
	}
	code, body = do(t, h, "GET", base+"/transitions", "")
	if code != http.StatusOK || len(body["items"].([]any)) != 6 {
		t.Errorf("transitions = %d %v", code, body)
	}
	if strings.Contains(mustJSON(body), "title") {
		t.Error("transitions must not carry free text")
	}
	if code, body := do(t, h, "POST", base+"/review", `{"expectedVersion":`+ver()+`}`); code != http.StatusBadRequest {
		t.Errorf("review without an outcome note = %d %v", code, body)
	}
	if code, body := do(t, h, "POST", base+"/close", `{"expectedVersion":`+ver()+`}`); code != http.StatusOK || body["status"] != "closed" || body["closedAt"] == nil {
		t.Errorf("close = %d %v", code, body)
	}
	// A closed change still lists its affected resources (its links ended with it).
	if code, body := do(t, h, "GET", base, ""); code != http.StatusOK || len(body["affected"].([]any)) != 1 {
		t.Errorf("closed change affected = %d %v", code, body)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestSubmitErrorNamesTheMissingFieldsAndTheLookupFindsResources(t *testing.T) {
	h := serve(t, as(manager, "changes.manage", "services.view"))
	code, body := do(t, h, "POST", "/api/v1/changes", `{"title":"http-wizard","kind":"normal","risk":"medium"}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, body)
	}
	id, _ := body["id"].(string)
	// A draft without window, rollback plan and affected resource names every missing field at once.
	code, body = do(t, h, "POST", "/api/v1/changes/"+id+"/submit", `{"expectedVersion":1}`)
	if code != http.StatusBadRequest || errCode(body) != "changes.invalid_request" {
		t.Fatalf("submit = %d %v", code, body)
	}
	e, _ := body["error"].(map[string]any)
	details, _ := e["details"].(map[string]any)
	fields, _ := details["fields"].([]any)
	got := map[string]bool{}
	for _, f := range fields {
		m, _ := f.(map[string]any)
		got[m["field"].(string)] = m["code"] == "required"
	}
	if !got["affectedResources"] || !got["windowStart"] || !got["rollbackPlan"] {
		t.Errorf("details.fields = %v", fields)
	}
	if msg, _ := e["message"].(string); !strings.Contains(msg, "affected resource") {
		t.Errorf("message = %q", msg)
	}
	// The lookup finds the service, refuses a bad request, and a caller who may not see services learns nothing.
	code, body = do(t, h, "GET", "/api/v1/changes/affected-lookup?type=service&q=HTTP", "")
	items, _ := body["items"].([]any)
	if code != http.StatusOK || len(items) != 1 {
		t.Fatalf("lookup = %d %v", code, body)
	}
	if code, body := do(t, h, "GET", "/api/v1/changes/affected-lookup?type=bogus&q=x", ""); code != http.StatusBadRequest {
		t.Errorf("unknown type = %d %v", code, body)
	}
	if code, _ := do(t, h, "GET", "/api/v1/changes/affected-lookup?type=service", ""); code != http.StatusBadRequest {
		t.Errorf("missing q = %d", code)
	}
	if code, _ := do(t, h, "GET", "/api/v1/changes/affected-lookup?type=asset&q=ws", ""); code != http.StatusOK {
		t.Errorf("asset lookup without assets.view = %d", code)
	}
	hBlind := serve(t, as(manager, "changes.manage"))
	if code, body := do(t, hBlind, "GET", "/api/v1/changes/affected-lookup?type=service&q=http", ""); code != http.StatusOK || len(body["items"].([]any)) != 0 {
		t.Errorf("a caller without services.view must get nothing: %d %v", code, body)
	}
	if code, _ := do(t, serve(t, as(outsider, "changes.view")), "GET", "/api/v1/changes/affected-lookup?type=service&q=http", ""); code != http.StatusForbidden {
		t.Errorf("lookup without changes.manage = %d", code)
	}
}
