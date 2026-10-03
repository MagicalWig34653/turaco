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

	"github.com/MagicalWig34653/turaco/backend/internal/modules/infrastructure/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/infrastructure/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/infrastructure/transport"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
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
	asset = "00000000-0000-7000-8000-0000000000f6"
)

type dir struct{}

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

type assets struct{}

func (assets) Assets(_ context.Context, ids []string) (map[string]application.AssetInfo, error) {
	out := map[string]application.AssetInfo{}
	for _, id := range ids {
		if id == asset {
			out[id] = application.AssetInfo{ID: id, Reference: "AST-1", Status: "assigned"}
		}
	}
	return out, nil
}

func serve(t *testing.T, a authorization.Authenticator) http.Handler {
	t.Helper()
	pool := dbtest.Pool(t)
	svc := application.NewService(repository.New(pool), dir{}, assets{})
	mux := http.NewServeMux()
	transport.Register(mux, svc, a, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() {
		ctx := context.Background()
		const racks = `SELECT k.id FROM infrastructure.racks k JOIN infrastructure.rooms m ON m.id = k.room_id JOIN infrastructure.buildings b ON b.id = m.building_id WHERE b.site_location_id = $1::uuid`
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure.rack_unit_occupancy WHERE rack_id IN (`+racks+`)`, site)
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure.rack_placements WHERE rack_id IN (`+racks+`) AND previous_placement_id IS NOT NULL`, site)
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure.rack_placements WHERE rack_id IN (`+racks+`)`, site)
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure.racks WHERE id IN (`+racks+`)`, site)
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure.rooms WHERE building_id IN (SELECT id FROM infrastructure.buildings WHERE site_location_id = $1::uuid)`, site)
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure.buildings WHERE site_location_id = $1::uuid`, site)
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure.virtual_machines WHERE name LIKE 'http-%'`)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE actor_id = $1::uuid`, admin)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE actor_id = $1::uuid`, admin)
	})
	return httpx(mux)
}

// httpx wraps the mux with a request id like the production middleware does.
func httpx(next http.Handler) http.Handler {
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
	return rec.Code, out
}

func TestPermissionsAndNotFound(t *testing.T) {
	anon := serve(t, fakeAuth{})
	if code, _ := call(t, anon, "GET", "/api/v1/buildings", nil); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
	none := serve(t, as(admin))
	for _, path := range []string{"/api/v1/buildings", "/api/v1/infrastructure/tree", "/api/v1/virtual-machines", "/api/v1/racks/" + site, "/api/v1/assets/" + asset + "/location"} {
		if code, _ := call(t, none, "GET", path, nil); code != http.StatusForbidden {
			t.Errorf("GET %s without permission: %d", path, code)
		}
	}
	viewer := serve(t, as(admin, "infrastructure.view"))
	if code, _ := call(t, viewer, "POST", "/api/v1/buildings", map[string]any{"siteLocationId": site, "name": "http-x"}); code != http.StatusForbidden {
		t.Errorf("viewer create: %d", code)
	}
	if code, _ := call(t, viewer, "POST", "/api/v1/rack-placements", map[string]any{}); code != http.StatusForbidden {
		t.Errorf("viewer place: %d", code)
	}
	if code, _ := call(t, viewer, "GET", "/api/v1/racks/"+site, nil); code != http.StatusNotFound {
		t.Errorf("unknown rack: %d", code)
	}
	if code, _ := call(t, viewer, "GET", "/api/v1/racks/not-a-uuid", nil); code != http.StatusNotFound {
		t.Errorf("invalid rack id: %d", code)
	}
	if code, _ := call(t, viewer, "GET", "/api/v1/buildings?limit=0", nil); code != http.StatusBadRequest {
		t.Errorf("limit 0: %d", code)
	}
	// Location lookup needs assets.view as well.
	if code, _ := call(t, viewer, "GET", "/api/v1/assets/"+asset+"/location", nil); code != http.StatusForbidden {
		t.Errorf("location without assets.view: %d", code)
	}
}

func TestTopologyPlacementAndVMFlow(t *testing.T) {
	h := serve(t, as(admin, "infrastructure.manage", "assets.view"))
	code, b := call(t, h, "POST", "/api/v1/buildings", map[string]any{"siteLocationId": site, "name": "http-HQ", "addressNote": "Main street 1"})
	if code != http.StatusCreated {
		t.Fatalf("create building: %d %v", code, b)
	}
	bid := b["id"].(string)
	if code, _ := call(t, h, "POST", "/api/v1/buildings", map[string]any{"siteLocationId": site, "name": "http-hq"}); code != http.StatusConflict {
		t.Fatalf("duplicate: %d", code)
	}
	if code, _ := call(t, h, "POST", "/api/v1/buildings", map[string]any{"siteLocationId": asset, "name": "http-no"}); code != http.StatusBadRequest {
		t.Fatalf("unknown site: %d", code)
	}
	if code, _ := call(t, h, "PATCH", "/api/v1/buildings/"+bid, map[string]any{"name": "http-HQ2"}); code != http.StatusBadRequest {
		t.Fatalf("patch without expectedVersion: %d", code)
	}
	if code, b = call(t, h, "PATCH", "/api/v1/buildings/"+bid, map[string]any{"name": "http-HQ2", "expectedVersion": 1}); code != http.StatusOK || b["version"].(float64) != 2 {
		t.Fatalf("rename: %d %v", code, b)
	}
	if code, _ := call(t, h, "PATCH", "/api/v1/buildings/"+bid, map[string]any{"name": "http-HQ3", "expectedVersion": 1}); code != http.StatusConflict {
		t.Fatalf("stale: %d", code)
	}
	code, room := call(t, h, "POST", "/api/v1/buildings/"+bid+"/rooms", map[string]any{"name": "Server room", "floor": "-1"})
	if code != http.StatusCreated {
		t.Fatalf("room: %d %v", code, room)
	}
	code, rack := call(t, h, "POST", "/api/v1/rooms/"+room["id"].(string)+"/racks", map[string]any{"name": "A1", "heightU": 42})
	if code != http.StatusCreated {
		t.Fatalf("rack: %d %v", code, rack)
	}
	rid := rack["id"].(string)
	code, pl := call(t, h, "POST", "/api/v1/rack-placements", map[string]any{"rackId": rid, "assetId": asset, "uPosition": 40, "heightU": 2, "face": "front"})
	if code != http.StatusCreated {
		t.Fatalf("place: %d %v", code, pl)
	}
	if code, _ := call(t, h, "POST", "/api/v1/rack-placements", map[string]any{"rackId": rid, "assetId": asset, "uPosition": 1, "heightU": 1, "face": "front"}); code != http.StatusConflict {
		t.Fatalf("second placement: %d", code)
	}
	if code, _ := call(t, h, "POST", "/api/v1/rack-placements", map[string]any{"rackId": rid, "assetId": site, "uPosition": 1, "heightU": 1, "face": "front"}); code != http.StatusBadRequest {
		t.Fatalf("unknown asset: %d", code)
	}
	code, d := call(t, h, "GET", "/api/v1/racks/"+rid, nil)
	places, _ := d["placements"].([]any)
	if code != http.StatusOK || len(places) != 1 || places[0].(map[string]any)["assetReference"] != "AST-1" {
		t.Fatalf("rack detail: %d %v", code, d)
	}
	code, loc := call(t, h, "GET", "/api/v1/assets/"+asset+"/location", nil)
	if code != http.StatusOK || loc["placed"] != true || loc["siteName"] != "Headquarters" || loc["rackName"] != "A1" {
		t.Fatalf("location: %d %v", code, loc)
	}
	if code, _ := call(t, h, "GET", "/api/v1/assets/"+site+"/location", nil); code != http.StatusNotFound {
		t.Fatalf("location of unknown asset: %d", code)
	}
	code, tree := call(t, h, "GET", "/api/v1/infrastructure/tree", nil)
	if code != http.StatusOK || !strings.Contains(toJSON(tree), "Headquarters") {
		t.Fatalf("tree: %d %v", code, tree)
	}
	pid := pl["id"].(string)
	if code, _ := call(t, h, "POST", "/api/v1/rack-placements/"+pid+"/remove", map[string]any{"reason": "oops", "expectedVersion": 1}); code != http.StatusBadRequest {
		t.Fatalf("free-text reason: %d", code)
	}
	for _, path := range []string{"/remove", "/move"} {
		if code, _ := call(t, h, "POST", "/api/v1/rack-placements/"+pid+path, map[string]any{"reason": "relocated", "rackId": rid, "uPosition": 1, "heightU": 1, "face": "front"}); code != http.StatusBadRequest {
			t.Fatalf("%s without expectedVersion: %d", path, code)
		}
	}
	// A visitor without assets.view sees the placement as occupied, without the asset.
	viewOnly := serve(t, as(admin, "infrastructure.view"))
	code, vd := call(t, viewOnly, "GET", "/api/v1/racks/"+rid, nil)
	vp, _ := vd["placements"].([]any)
	if code != http.StatusOK || len(vp) != 1 || strings.Contains(toJSON(vd), asset) || strings.Contains(toJSON(vd), "AST-1") || vp[0].(map[string]any)["heightU"].(float64) != 2 {
		t.Fatalf("rack detail without assets.view leaks the asset: %d %v", code, vd)
	}
	if code, l := call(t, viewOnly, "GET", "/api/v1/racks/"+rid+"/placements", nil); code != http.StatusOK || strings.Contains(toJSON(l), asset) || strings.Contains(toJSON(l), "AST-1") {
		t.Fatalf("placement list without assets.view leaks the asset: %d %v", code, l)
	}
	if code, one := call(t, viewOnly, "GET", "/api/v1/rack-placements/"+pid, nil); code != http.StatusOK || strings.Contains(toJSON(one), asset) {
		t.Fatalf("placement without assets.view leaks the asset: %d %v", code, one)
	}
	if code, _ := call(t, viewOnly, "GET", "/api/v1/infrastructure/placement-warnings", nil); code != http.StatusForbidden {
		t.Fatalf("warnings without assets.view: %d", code)
	}
	if code, wl := call(t, h, "GET", "/api/v1/infrastructure/placement-warnings", nil); code != http.StatusOK || wl["truncated"] != false {
		t.Fatalf("warnings: %d %v", code, wl)
	}
	if code, tr := call(t, h, "GET", "/api/v1/infrastructure/tree", nil); code != http.StatusOK || tr["truncated"] != false {
		t.Fatalf("tree truncated flag: %d %v", code, tr)
	}
	if code, _ = call(t, h, "POST", "/api/v1/rack-placements/"+pid+"/remove", map[string]any{"reason": "relocated", "expectedVersion": pl["version"]}); code != http.StatusOK {
		t.Fatalf("remove: %d", code)
	}
	if code, _ = call(t, h, "POST", "/api/v1/rack-placements/"+pid+"/remove", map[string]any{"reason": "relocated", "expectedVersion": pl["version"]}); code != http.StatusOK {
		t.Fatalf("retried remove must be idempotent: %d", code)
	}
	if code, _ = call(t, h, "POST", "/api/v1/rack-placements/"+pid+"/remove", map[string]any{"reason": "other", "expectedVersion": pl["version"]}); code != http.StatusConflict {
		t.Fatalf("remove twice with another reason: %d", code)
	}
	if code, loc = call(t, h, "GET", "/api/v1/assets/"+asset+"/location", nil); code != http.StatusOK || loc["placed"] != false {
		t.Fatalf("location after removal: %d %v", code, loc)
	}

	code, vm := call(t, h, "POST", "/api/v1/virtual-machines", map[string]any{"name": "http-vm", "state": "running", "vcpu": 2, "memoryMb": 4096, "managementAddress": "vm.example.org", "hypervisorAssetId": asset})
	if code != http.StatusCreated || vm["hypervisorAssetReference"] != "AST-1" {
		t.Fatalf("vm: %d %v", code, vm)
	}
	vid := vm["id"].(string)
	if code, _ := call(t, h, "POST", "/api/v1/virtual-machines/"+vid+"/state", map[string]any{"state": "stopped"}); code != http.StatusBadRequest {
		t.Fatalf("state without expectedVersion: %d", code)
	}
	for _, c := range []struct {
		path string
		body map[string]any
	}{{"/hypervisor", map[string]any{}}, {"/decommission", map[string]any{"reason": "retired"}}} {
		if code, _ := call(t, h, "POST", "/api/v1/virtual-machines/"+vid+c.path, c.body); code != http.StatusBadRequest {
			t.Fatalf("%s without expectedVersion: %d", c.path, code)
		}
	}
	for _, path := range []string{"/archive", "/unarchive"} {
		if code, _ := call(t, h, "POST", "/api/v1/racks/"+rid+path, map[string]any{}); code != http.StatusBadRequest {
			t.Fatalf("rack %s without expectedVersion: %d", path, code)
		}
	}
	if code, _ := call(t, h, "POST", "/api/v1/virtual-machines/"+vid+"/state", map[string]any{"state": "stopped", "expectedVersion": 1}); code != http.StatusOK {
		t.Fatalf("state: %d", code)
	}
	if code, l := call(t, h, "GET", "/api/v1/virtual-machines?state=stopped&q=http-vm", nil); code != http.StatusOK || len(l["items"].([]any)) != 1 {
		t.Fatalf("list: %d %v", code, l)
	}
	if code, _ := call(t, h, "POST", "/api/v1/virtual-machines/"+vid+"/decommission", map[string]any{"reason": "retired", "expectedVersion": 2}); code != http.StatusOK {
		t.Fatalf("decommission: %d", code)
	}
	if code, _ := call(t, h, "PATCH", "/api/v1/virtual-machines/"+vid, map[string]any{"vcpu": 4, "expectedVersion": 4}); code != http.StatusConflict {
		t.Fatalf("patch decommissioned: %d", code)
	}
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
