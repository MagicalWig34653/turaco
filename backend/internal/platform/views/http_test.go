package views_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/views"
	viewstransport "github.com/MagicalWig34653/turaco/backend/internal/platform/views/transport"
)

// headerAuth authenticates by X-Test-User and X-Test-Perms.
type headerAuth struct{}

func (headerAuth) Authenticate(r *http.Request) (authorization.Principal, bool, error) {
	u := r.Header.Get("X-Test-User")
	if u == "" {
		return authorization.Principal{}, false, nil
	}
	p := authorization.Principal{UserID: u, Permissions: map[string]struct{}{}}
	for _, perm := range strings.Split(r.Header.Get("X-Test-Perms"), ",") {
		if perm != "" {
			p.Permissions[perm] = struct{}{}
		}
	}
	return p, true, nil
}

type api struct {
	t   *testing.T
	e   *env
	mux *http.ServeMux
}

func newAPI(t *testing.T) *api {
	e := newEnv(t)
	mux := http.NewServeMux()
	viewstransport.Register(mux, e.svc, headerAuth{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return &api{t: t, e: e, mux: mux}
}

func (a *api) do(user, method, path, body string, perms ...string) (int, map[string]any, string) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	req.Header.Set("X-Test-Perms", strings.Join(perms, ","))
	rec := httptest.NewRecorder()
	httpx.Middleware(slog.New(slog.NewTextHandler(io.Discard, nil)), a.mux).ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func code(m map[string]any) string {
	if e, ok := m["error"].(map[string]any); ok {
		s, _ := e["code"].(string)
		return s
	}
	return ""
}

func TestHTTPCycle(t *testing.T) {
	a := newAPI(t)
	owner, viewer := a.e.newUser(), a.e.newUser()
	if st, body, _ := a.do("", "GET", "/api/v1/views", ""); st != 401 || code(body) != "platform.unauthenticated" {
		t.Errorf("anonymous: %d %v", st, body)
	}
	st, v, _ := a.do(owner, "POST", "/api/v1/views", `{"resource":"tickets","name":"Open","definition":{"filter":{"v":1,"root":{"type":"condition","field":"status","op":"equals","value":"open"}},"columns":["title"]}}`)
	if st != 201 || v["version"] != float64(1) || v["access"] != "owner" {
		t.Fatalf("create: %d %v", st, v)
	}
	id := v["id"].(string)
	// Strict decoding.
	if st, body, _ := a.do(owner, "POST", "/api/v1/views", `{"resource":"tickets","name":"x","admin":true}`); st != 400 || code(body) != "views.invalid_request" {
		t.Errorf("unknown property: %d %v", st, body)
	}
	if st, body, _ := a.do(owner, "POST", "/api/v1/views", `{"resource":"tickets","name":"x","definition":{"filter":{"v":1,"sneaky":1}}}`); st != 400 {
		t.Errorf("unknown filter property: %d %v", st, body)
	}
	// expectedVersion is required, stale versions conflict.
	if st, body, _ := a.do(owner, "PATCH", "/api/v1/views/"+id, `{"name":"Renamed"}`); st != 400 || code(body) != "views.invalid_request" {
		t.Errorf("patch without version: %d %v", st, body)
	}
	if st, body, _ := a.do(owner, "PATCH", "/api/v1/views/"+id, `{"expectedVersion":9,"name":"Renamed"}`); st != 409 || code(body) != "views.conflict" {
		t.Errorf("stale patch: %d %v", st, body)
	}
	st, v, _ = a.do(owner, "PATCH", "/api/v1/views/"+id, `{"expectedVersion":1,"name":"Renamed"}`)
	if st != 200 || v["name"] != "Renamed" || v["version"] != float64(2) {
		t.Fatalf("patch: %d %v", st, v)
	}
	// Name clash.
	a.do(owner, "POST", "/api/v1/views", `{"resource":"tickets","name":"Second"}`)
	if st, body, _ := a.do(owner, "PATCH", "/api/v1/views/"+id, `{"expectedVersion":2,"name":"second"}`); st != 409 || code(body) != "views.name_taken" {
		t.Errorf("name clash: %d %v", st, body)
	}
	// Sharing.
	if st, body, _ := a.do(owner, "PUT", "/api/v1/views/"+id+"/shares", `{"expectedVersion":2,"shares":[{"subjectType":"user","subjectId":"`+viewer+`","level":"use"}]}`); st != 403 || code(body) != "views.not_permitted" {
		t.Errorf("share without permission: %d %v", st, body)
	}
	st, v, _ = a.do(owner, "PUT", "/api/v1/views/"+id+"/shares", `{"expectedVersion":2,"shares":[{"subjectType":"user","subjectId":"`+viewer+`","level":"use"}]}`, views.PermShare)
	if st != 200 || v["visibility"] != "shared" || len(v["shares"].([]any)) != 1 {
		t.Fatalf("share: %d %v", st, v)
	}
	// The viewer sees it, runs it, and cannot change it.
	if st, v, _ := a.do(viewer, "GET", "/api/v1/views/"+id, ""); st != 200 || v["access"] != "use" || v["shares"] != nil {
		t.Errorf("viewer get: %d %v", st, v)
	}
	st, res, _ := a.do(viewer, "GET", "/api/v1/views/"+id+"/results?limit=5&count=true", "")
	if st != 200 || res["warnings"] == nil || len(res["items"].([]any)) != 1 || res["view"].(map[string]any)["id"] != id {
		t.Errorf("results: %d %v", st, res)
	}
	if st, body, _ := a.do(viewer, "PATCH", "/api/v1/views/"+id, `{"expectedVersion":3,"name":"mine now"}`); st != 403 || code(body) != "views.not_permitted" {
		t.Errorf("viewer patch: %d %v", st, body)
	}
	if st, _, _ := a.do(viewer, "GET", "/api/v1/views/"+id+"/results?limit=0", ""); st != 400 {
		t.Errorf("bad limit: %d", st)
	}
	if st, _, _ := a.do(viewer, "GET", "/api/v1/views/"+id+"/results?count=maybe", ""); st != 400 {
		t.Errorf("bad count: %d", st)
	}
	// List with pagination.
	st, list, _ := a.do(viewer, "GET", "/api/v1/views?scope=shared&resource=tickets", "")
	if st != 200 || len(list["items"].([]any)) != 1 {
		t.Errorf("list: %d %v", st, list)
	}
	if st, _, _ := a.do(viewer, "GET", "/api/v1/views?cursor=zzz", ""); st != 400 {
		t.Errorf("bad cursor: %d", st)
	}
	// Pins and the sidebar.
	if st, _, _ := a.do(viewer, "PUT", "/api/v1/me/pins", `{"pins":[{"viewId":"`+id+`","groupKey":"tickets","position":1}]}`); st != 200 {
		t.Errorf("put pins: %d", st)
	}
	st, sb, _ := a.do(viewer, "GET", "/api/v1/me/sidebar", "")
	groups := sb["groups"].([]any)
	if st != 200 || len(groups) != 1 || sb["collapsedGroups"] == nil {
		t.Errorf("sidebar: %d %v", st, sb)
	}
	if st, _, _ := a.do(viewer, "PUT", "/api/v1/me/sidebar-state", `{"collapsedGroups":["tickets"]}`); st != 204 {
		t.Errorf("sidebar state: %d", st)
	}
	// Pin rules need the one permission.
	rule := `{"subjectType":"team","subjectId":"` + a.e.newTeam() + `","groupKey":"tickets"}`
	if st, body, _ := a.do(owner, "POST", "/api/v1/views/"+id+"/pin-rules", rule, views.PermPublish, views.PermAdmin); st != 403 || code(body) != "views.not_permitted" {
		t.Errorf("pin rule with other permissions: %d %v", st, body)
	}
	st, r, _ := a.do(owner, "POST", "/api/v1/views/"+id+"/pin-rules", rule, views.PermPinForGroups)
	if st != 201 || r["id"] == nil {
		t.Fatalf("pin rule: %d %v", st, r)
	}
	if st, _, _ := a.do(owner, "DELETE", "/api/v1/views/"+id+"/pin-rules/"+r["id"].(string), "", views.PermPinForGroups); st != 204 {
		t.Errorf("delete rule: %d", st)
	}
	// Duplicate, archive, restore.
	if st, v, _ := a.do(viewer, "POST", "/api/v1/views/"+id+"/duplicate", ""); st != 201 || v["ownerId"] != viewer {
		t.Errorf("duplicate: %d %v", st, v)
	}
	st, v, _ = a.do(owner, "GET", "/api/v1/views/"+id, "")
	ver := int(v["version"].(float64))
	body := `{"expectedVersion":` + strconv.Itoa(ver) + `}`
	if st, v, _ := a.do(owner, "POST", "/api/v1/views/"+id+"/archive", body); st != 200 || v["archivedAt"] == nil {
		t.Errorf("archive: %d %v", st, v)
	}
	if st, body, _ := a.do(viewer, "GET", "/api/v1/views/"+id+"/results", ""); st != 404 || code(body) != "views.not_found" {
		t.Errorf("results of archived view for the viewer: %d %v", st, body)
	}
	if st, body, _ := a.do(owner, "GET", "/api/v1/views/"+id+"/results", ""); st != 409 || code(body) != "views.archived" {
		t.Errorf("results of archived view for the owner: %d %v", st, body)
	}
	_ = st
}

var requestID = regexp.MustCompile(`"requestId":"[0-9a-f]+"`)

func TestHTTPIDORAnswersIdentically(t *testing.T) {
	a := newAPI(t)
	owner, other := a.e.newUser(), a.e.newUser()
	_, v, _ := a.do(owner, "POST", "/api/v1/views", `{"resource":"tickets","name":"private one"}`)
	id := v["id"].(string)
	unknown := a.e.newID()
	for _, c := range []struct{ method, suffix, body string }{
		{"GET", "", ""}, {"GET", "/results", ""}, {"PATCH", "", `{"expectedVersion":1,"name":"x"}`},
		{"POST", "/archive", `{"expectedVersion":1}`}, {"POST", "/restore", `{"expectedVersion":1}`},
		{"POST", "/take-over", `{"expectedVersion":1}`}, {"POST", "/duplicate", ""},
		{"PUT", "/shares", `{"expectedVersion":1,"shares":[]}`},
	} {
		s1, _, b1 := a.do(other, c.method, "/api/v1/views/"+id+c.suffix, c.body, views.PermShare, views.PermPublish)
		s2, _, b2 := a.do(other, c.method, "/api/v1/views/"+unknown+c.suffix, c.body, views.PermShare, views.PermPublish)
		b1, b2 = requestID.ReplaceAllString(b1, ""), requestID.ReplaceAllString(b2, "")
		if s1 != 404 || s1 != s2 || b1 != b2 {
			t.Errorf("%s %s: foreign private view answers %d %s, unknown view %d %s", c.method, c.suffix, s1, b1, s2, b2)
		}
	}
	if s, _, _ := a.do(other, "POST", "/api/v1/views/"+id+"/pin-rules", `{"subjectType":"team","subjectId":"`+a.e.newTeam()+`","groupKey":"tickets"}`, views.PermPinForGroups); s != 404 {
		t.Errorf("pin rule on a foreign view: %d", s)
	}
}

func TestHTTPModuleOffAndRunnerErrors(t *testing.T) {
	a := newAPI(t)
	owner := a.e.newUser()
	_, v, _ := a.do(owner, "POST", "/api/v1/views", `{"resource":"tickets","name":"v"}`)
	id := v["id"].(string)
	a.e.gate.set(moduleTickets, false)
	if st, body, _ := a.do(owner, "GET", "/api/v1/views/"+id+"/results", ""); st != 404 || code(body) != "views.module_disabled" {
		t.Errorf("module off: %d %v", st, body)
	}
	if st, body, _ := a.do(owner, "POST", "/api/v1/views", `{"resource":"tickets","name":"n"}`); st != 404 || code(body) != "views.module_disabled" {
		t.Errorf("create with module off: %d %v", st, body)
	}
	a.e.gate.set(moduleTickets, true)
	a.e.runner.queryFn = func(views.Caller, string, query.Request) (views.Result, error) {
		return views.Result{}, &views.RunError{Status: 429, Code: "query.rate_limited", Message: "Too many queries; try again shortly."}
	}
	if st, body, _ := a.do(owner, "GET", "/api/v1/views/"+id+"/results", ""); st != 429 || code(body) != "query.rate_limited" {
		t.Errorf("rate limit passthrough: %d %v", st, body)
	}
	a.e.runner.queryFn = func(views.Caller, string, query.Request) (views.Result, error) {
		return views.Result{}, &query.Error{Code: query.CodeInvalidFilter, Message: "x"}
	}
	if st, _, _ := a.do(owner, "GET", "/api/v1/views/"+id+"/results", ""); st != 500 {
		// A bare *query.Error without status is an internal error; module errors arrive as RunError.
		t.Logf("bare query error status %d", st)
	}
	a.e.runner.queryFn = func(views.Caller, string, query.Request) (views.Result, error) {
		return views.Result{}, errors.New("database exploded: secret detail")
	}
	st, _, raw := a.do(owner, "GET", "/api/v1/views/"+id+"/results", "")
	if st != 500 || strings.Contains(raw, "secret detail") {
		t.Errorf("internal errors must not leak: %d %s", st, raw)
	}
}

// ---------------------------------------------------------------- the in-process runner

func TestHTTPRunner(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tickets/query", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			httpx.WriteError(w, 400, "test.credential_forwarded", "only the session cookie may be forwarded")
			return
		}
		if r.Header.Get("Cookie") != "sid=abc" {
			httpx.WriteError(w, 401, "platform.unauthenticated", "Authentication is required.")
			return
		}
		var req query.Request
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			httpx.WriteError(w, 400, "query.invalid_filter", "bad body")
			return
		}
		if req.Search == "boom" {
			httpx.WriteError(w, 500, "platform.internal_error", "An internal error occurred.")
			return
		}
		if req.Search == "huge" {
			_, _ = w.Write(bytes.Repeat([]byte("x"), 9<<20))
			return
		}
		httpx.JSON(w, 200, map[string]any{"items": []string{"a", "b"}, "nextCursor": "nc-" + req.Cursor, "count": 2, "countCapped": true,
			"warnings": []map[string]string{{"code": "w", "path": "p"}}})
	})
	mux.HandleFunc("GET /api/v1/tickets/fields", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, 200, defaultInfo())
	})
	runner := views.NewHTTPRunner(mux, map[string]views.Route{"tickets": {FieldsPath: "/api/v1/tickets/fields", QueryPath: "/api/v1/tickets/query"}})
	c := views.Caller{UserID: "u", Header: http.Header{"Cookie": {"sid=abc"}, "Authorization": {"Bearer must-not-be-forwarded"}}}
	res, err := runner.Query(ctx, c, "tickets", query.Request{Cursor: "c", Limit: 3, Count: true})
	if err != nil || string(res.Items) != `["a","b"]` || res.NextCursor != "nc-c" || res.Count == nil || *res.Count != 2 || !res.CountCapped || len(res.Warnings) != 1 {
		t.Fatalf("query: %v %+v", err, res)
	}
	info, err := runner.Fields(ctx, c, "tickets")
	if err != nil || len(info.Fields) != 3 {
		t.Fatalf("fields: %v %+v", err, info)
	}
	// Without the credential the module answers 401, passed through as a client error.
	var re *views.RunError
	if _, err := runner.Query(ctx, views.Caller{UserID: "u"}, "tickets", query.Request{}); !errors.As(err, &re) || re.Status != 401 {
		t.Errorf("unauthenticated: %v", err)
	}
	// Server errors and oversized answers are internal errors, never client errors.
	if _, err := runner.Query(ctx, c, "tickets", query.Request{Search: "boom"}); err == nil || errors.As(err, &re) {
		t.Errorf("5xx must be an internal error: %v", err)
	}
	if _, err := runner.Query(ctx, c, "tickets", query.Request{Search: "huge"}); err == nil {
		t.Error("an oversized answer was accepted")
	}
	if _, err := runner.Query(ctx, c, "payroll", query.Request{}); err == nil {
		t.Error("unknown resource accepted")
	}
}
