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

	"github.com/MagicalWig34653/turaco/backend/internal/modules/services/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/services/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/services/transport"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
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
	admin = "00000000-0000-7000-8000-0000000000c3"
	site  = "00000000-0000-7000-8000-0000000000e5"
	vmID  = "00000000-0000-7000-8000-0000000000a1"
	hv    = "00000000-0000-7000-8000-0000000000f6"
)

type dir struct{}

func (dir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = id == admin
	}
	return out, nil
}
func (dir) ActiveTeams(_ context.Context, ids []string) (map[string]bool, error) {
	return map[string]bool{}, nil
}
func (dir) ActiveLocations(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = id == site
	}
	return out, nil
}
func (dir) LocationNames(_ context.Context, ids []string) (map[string]string, error) {
	return map[string]string{site: "Headquarters"}, nil
}

type infra struct{}

func (infra) VMs(_ context.Context, ids []string) (map[string]application.VMInfo, error) {
	out := map[string]application.VMInfo{}
	for _, id := range ids {
		if id == vmID {
			out[id] = application.VMInfo{ID: id, Name: "http-vm-secret", State: "running"}
		}
	}
	return out, nil
}

func (infra) VMIDsWithHypervisor(context.Context, string, int) ([]string, error) { return nil, nil }

type assets struct{}

func (assets) Assets(_ context.Context, ids []string) (map[string]application.AssetInfo, error) {
	out := map[string]application.AssetInfo{}
	for _, id := range ids {
		if id == hv {
			out[id] = application.AssetInfo{ID: id, Reference: "AST-SECRET", Status: "assigned"}
		}
	}
	return out, nil
}

func serve(t *testing.T, a authorization.Authenticator) http.Handler {
	t.Helper()
	pool := dbtest.Pool(t)
	reg := relationships.NewRegistry()
	reg.Register(application.Triples...)
	graph := relationships.New(reg)
	app := application.NewApp(repository.New(pool), graph, dir{}, infra{}, assets{})
	mux := http.NewServeMux()
	transport.Register(mux, app, a, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM platform.relationships WHERE created_by = $1::uuid OR source_id IN (SELECT id FROM services.services WHERE name LIKE 'http-%')`, admin)
		_, _ = pool.Exec(ctx, `DELETE FROM services.services WHERE name LIKE 'http-%'`)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE actor_id = $1::uuid`, admin)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE actor_id = $1::uuid`, admin)
	})
	return stamp(mux)
}

func stamp(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "http-test")
		next.ServeHTTP(w, r)
	})
}

func call(t *testing.T, h http.Handler, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out == nil {
		out = map[string]any{"_raw": rec.Body.String()}
	}
	return rec.Code, out
}

func TestPermissionsAndNotFound(t *testing.T) {
	if code, _ := call(t, serve(t, fakeAuth{}), "GET", "/api/v1/services", nil); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
	none := serve(t, as(admin, "infrastructure.view", "assets.view"))
	for _, path := range []string{"/api/v1/services", "/api/v1/services/" + vmID, "/api/v1/impact?type=location&id=" + site} {
		if code, _ := call(t, none, "GET", path, nil); code != http.StatusForbidden {
			t.Errorf("GET %s without services.view: %d", path, code)
		}
	}
	viewer := serve(t, as(admin, "services.view"))
	writes := []struct{ method, path string }{
		{"POST", "/api/v1/services"}, {"PATCH", "/api/v1/services/" + vmID}, {"POST", "/api/v1/services/" + vmID + "/status"},
		{"POST", "/api/v1/services/" + vmID + "/retire"}, {"POST", "/api/v1/services/" + vmID + "/dependencies"},
		{"DELETE", "/api/v1/services/" + vmID + "/dependencies/" + site + "?reason=other"},
	}
	for _, w := range writes {
		if code, _ := call(t, viewer, w.method, w.path, map[string]any{}); code != http.StatusForbidden {
			t.Errorf("viewer %s %s: %d", w.method, w.path, code)
		}
	}
	if code, _ := call(t, viewer, "GET", "/api/v1/services/not-a-uuid", nil); code != http.StatusNotFound {
		t.Errorf("invalid id: %d", code)
	}
	if code, _ := call(t, viewer, "GET", "/api/v1/services/"+vmID, nil); code != http.StatusNotFound {
		t.Errorf("unknown id: %d", code)
	}
	if code, _ := call(t, viewer, "GET", "/api/v1/services?limit=0", nil); code != http.StatusBadRequest {
		t.Errorf("limit 0: %d", code)
	}
	// The impact start record of a type the caller may not see is 404, not 403 or 200.
	for _, q := range []string{"type=vm&id=" + vmID, "type=location&id=" + site, "type=asset&id=" + hv} {
		if code, _ := call(t, viewer, "GET", "/api/v1/impact?"+q, nil); code != http.StatusNotFound {
			t.Errorf("impact %s without type permission: %d", q, code)
		}
	}
	for _, q := range []string{"type=ticket&id=" + vmID, "type=service&id=" + vmID + "&direction=sideways", "type=service&id=" + vmID + "&depth=7", "type=service&id=" + vmID + "&depth=x"} {
		if code, _ := call(t, viewer, "GET", "/api/v1/impact?"+q, nil); code != http.StatusBadRequest {
			t.Errorf("impact %s: %d", q, code)
		}
	}
}

func TestServiceFlowAndImpactRedaction(t *testing.T) {
	h := serve(t, as(admin, "services.manage", "infrastructure.view"))
	manageOnly := serve(t, as(admin, "services.manage"))
	code, b := call(t, h, "POST", "/api/v1/services", map[string]any{"name": "http-Billing", "criticality": "high", "ownerUserId": admin, "description": "Invoices"})
	if code != http.StatusCreated || b["reference"] == nil || b["version"].(float64) != 1 {
		t.Fatalf("create: %d %v", code, b)
	}
	id := b["id"].(string)
	if code, _ := call(t, h, "POST", "/api/v1/services", map[string]any{"name": "HTTP-billing", "criticality": "low"}); code != http.StatusConflict {
		t.Fatalf("duplicate: %d", code)
	}
	if code, _ := call(t, h, "POST", "/api/v1/services", map[string]any{"name": "http-x", "criticality": "low", "ownerUserId": vmID}); code != http.StatusBadRequest {
		t.Fatalf("unknown owner: %d", code)
	}
	if code, _ := call(t, h, "PATCH", "/api/v1/services/"+id, map[string]any{"criticality": "critical"}); code != http.StatusBadRequest {
		t.Fatalf("patch without expectedVersion: %d", code)
	}
	if code, b = call(t, h, "PATCH", "/api/v1/services/"+id, map[string]any{"criticality": "critical", "expectedVersion": 1}); code != http.StatusOK || b["version"].(float64) != 2 {
		t.Fatalf("patch: %d %v", code, b)
	}
	if code, _ := call(t, h, "PATCH", "/api/v1/services/"+id, map[string]any{"criticality": "low", "expectedVersion": 1}); code != http.StatusConflict {
		t.Fatalf("stale patch: %d", code)
	}
	if code, _ := call(t, h, "POST", "/api/v1/services/"+id+"/status", map[string]any{"status": "outage", "reason": "incident"}); code != http.StatusBadRequest {
		t.Fatalf("status without expectedVersion: %d", code)
	}
	if code, b = call(t, h, "POST", "/api/v1/services/"+id+"/status", map[string]any{"status": "outage", "reason": "incident", "expectedVersion": 2}); code != http.StatusOK || b["status"] != "outage" {
		t.Fatalf("status: %d %v", code, b)
	}

	// Dependencies: 201 first, 200 for the duplicate, 400 for unknown records, 409 for a cycle.
	dep := map[string]any{"targetType": "vm", "targetId": vmID}
	code, l := call(t, h, "POST", "/api/v1/services/"+id+"/dependencies", dep)
	if code != http.StatusCreated || l["confidence"] != "declared" {
		t.Fatalf("add dependency: %d %v", code, l)
	}
	// A caller without infrastructure.view cannot add a VM dependency (and cannot probe which VMs exist).
	if code, b := call(t, manageOnly, "POST", "/api/v1/services/"+id+"/dependencies", dep); code != http.StatusBadRequest || errCode(b) != "services.invalid_reference" {
		t.Fatalf("add vm without infrastructure.view: %d %v", code, b)
	}
	if code, _ := call(t, h, "POST", "/api/v1/services/"+id+"/dependencies", dep); code != http.StatusOK {
		t.Fatalf("duplicate dependency: %d", code)
	}
	if code, _ := call(t, h, "POST", "/api/v1/services/"+id+"/dependencies", map[string]any{"targetType": "vm", "targetId": hv}); code != http.StatusBadRequest {
		t.Fatalf("unknown vm: %d", code)
	}
	if code, _ := call(t, h, "POST", "/api/v1/services/"+id+"/dependencies", map[string]any{"targetType": "service", "targetId": id}); code != http.StatusBadRequest {
		t.Fatalf("self: %d", code)
	}
	code, b2 := call(t, h, "POST", "/api/v1/services", map[string]any{"name": "http-Portal", "criticality": "medium"})
	if code != http.StatusCreated {
		t.Fatal(b2)
	}
	portal := b2["id"].(string)
	if code, _ := call(t, h, "POST", "/api/v1/services/"+portal+"/dependencies", map[string]any{"targetType": "service", "targetId": id}); code != http.StatusCreated {
		t.Fatalf("service dependency: %d", code)
	}
	if code, _ := call(t, h, "POST", "/api/v1/services/"+id+"/dependencies", map[string]any{"targetType": "service", "targetId": portal}); code != http.StatusConflict {
		t.Fatalf("cycle: %d", code)
	}

	// Detail lists both directions (services.manage includes read access).
	code, d := call(t, h, "GET", "/api/v1/services/"+id, nil)
	deps, dependents := d["dependencies"].([]any), d["dependents"].([]any)
	if code != http.StatusOK || len(deps) != 1 || len(dependents) != 1 || dependents[0].(map[string]any)["node"].(map[string]any)["name"] != "http-Portal" {
		t.Fatalf("detail: %d %v", code, d)
	}
	// Without infrastructure.view the VM is shown under a placeholder id, with no name, and its real id appears nowhere.
	code, d = call(t, manageOnly, "GET", "/api/v1/services/"+id, nil)
	node := d["dependencies"].([]any)[0].(map[string]any)["node"].(map[string]any)
	if code != http.StatusOK || node["id"] != "hidden-1" || node["hidden"] != true || node["name"] != nil || node["type"] != "vm" {
		t.Fatalf("hidden dependency node: %d %v", code, node)
	}
	if raw, _ := json.Marshal(d); strings.Contains(string(raw), vmID) {
		t.Fatalf("real vm id leaked: %s", raw)
	}
	// Impact of the VM needs infrastructure.view: manage-only gets 404, a full reader sees names.
	if code, _ := call(t, manageOnly, "GET", "/api/v1/impact?type=vm&id="+vmID, nil); code != http.StatusNotFound {
		t.Fatalf("impact without infrastructure.view: %d", code)
	}
	// Paging the dependencies of a service.
	code, pg := call(t, h, "GET", "/api/v1/services/"+id+"/dependencies?direction=out&limit=1", nil)
	if code != http.StatusOK || len(pg["items"].([]any)) != 1 || pg["nextCursor"] != nil {
		t.Fatalf("dependencies page: %d %v", code, pg)
	}
	if code, pg = call(t, h, "GET", "/api/v1/services/"+id+"/dependencies?direction=in", nil); code != http.StatusOK || len(pg["items"].([]any)) != 1 {
		t.Fatalf("dependents page: %d %v", code, pg)
	}
	if code, _ := call(t, h, "GET", "/api/v1/services/"+id+"/dependencies?direction=sideways", nil); code != http.StatusBadRequest {
		t.Fatalf("bad direction: %d", code)
	}
	if code, b := call(t, h, "GET", "/api/v1/services/"+id+"/dependencies?cursor=nope", nil); code != http.StatusBadRequest || errCode(b) != "services.invalid_cursor" {
		t.Fatalf("bad cursor: %d %v", code, b)
	}
	full := serve(t, as(admin, "services.view", "infrastructure.view", "assets.view"))
	code, imp := call(t, full, "GET", "/api/v1/impact?type=vm&id="+vmID, nil)
	items := imp["items"].([]any)
	if code != http.StatusOK || len(items) != 2 || imp["direction"] != "downstream" || imp["truncated"] != false {
		t.Fatalf("impact: %d %v", code, imp)
	}
	first := items[0].(map[string]any)
	if first["id"] != id || first["depth"].(float64) != 1 || first["criticality"] != "critical" || first["status"] != "outage" || len(first["path"].([]any)) != 1 {
		t.Fatalf("impact first: %v", first)
	}
	if start := imp["start"].(map[string]any); start["name"] != "http-vm-secret" {
		t.Fatalf("impact start: %v", start)
	}
	code, imp = call(t, full, "GET", "/api/v1/impact?type=service&id="+portal+"&direction=upstream&depth=1", nil)
	if code != http.StatusOK || len(imp["items"].([]any)) != 1 || imp["depthLimited"] != true || imp["truncated"] != true {
		t.Fatalf("upstream depth 1: %d %v", code, imp)
	}

	// Removal needs a known reason and the dependency's own service.
	relID := l["relationshipId"].(string)
	if code, _ := call(t, h, "DELETE", "/api/v1/services/"+id+"/dependencies/"+relID, nil); code != http.StatusBadRequest {
		t.Fatalf("remove without reason: %d", code)
	}
	if code, _ := call(t, h, "DELETE", "/api/v1/services/"+portal+"/dependencies/"+relID+"?reason=other", nil); code != http.StatusNotFound {
		t.Fatalf("remove through another service: %d", code)
	}
	if code, _ := call(t, h, "DELETE", "/api/v1/services/"+id+"/dependencies/"+relID+"?reason=no_longer_needed", nil); code != http.StatusNoContent {
		t.Fatalf("remove: %d", code)
	}
	if code, _ := call(t, h, "DELETE", "/api/v1/services/"+id+"/dependencies/"+relID+"?reason=no_longer_needed", nil); code != http.StatusNoContent {
		t.Fatalf("retried remove: %d", code)
	}

	// Retire needs expectedVersion and is terminal.
	if code, _ := call(t, h, "POST", "/api/v1/services/"+id+"/retire", map[string]any{"reason": "replaced"}); code != http.StatusBadRequest {
		t.Fatalf("retire without expectedVersion: %d", code)
	}
	if code, b = call(t, h, "POST", "/api/v1/services/"+id+"/retire", map[string]any{"reason": "replaced", "expectedVersion": 3}); code != http.StatusOK || b["status"] != "retired" {
		t.Fatalf("retire: %d %v", code, b)
	}
	if code, _ := call(t, h, "POST", "/api/v1/services/"+id+"/status", map[string]any{"status": "operational", "reason": "recovered", "expectedVersion": 4}); code != http.StatusConflict {
		t.Fatalf("status after retire: %d", code)
	}
	// Retired services are hidden from the default list.
	code, list := call(t, h, "GET", "/api/v1/services?q=http-", nil)
	if code != http.StatusOK || len(list["items"].([]any)) != 1 {
		t.Fatalf("list: %d %v", code, list)
	}
	if code, list = call(t, h, "GET", "/api/v1/services?q=http-&includeRetired=true", nil); code != http.StatusOK || len(list["items"].([]any)) != 2 {
		t.Fatalf("list with retired: %d %v", code, list)
	}
	if strings.Contains(list["items"].([]any)[0].(map[string]any)["name"].(string), "secret") {
		t.Fatal("unexpected content")
	}
}

func errCode(b map[string]any) string {
	e, _ := b["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}
