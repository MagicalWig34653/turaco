package transport_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/modules"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/modules/transport"
)

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// headerAuth authenticates from test headers: X-User and X-Perms (comma separated). No X-User means anonymous.
type headerAuth struct{}

func (headerAuth) Authenticate(r *http.Request) (authorization.Principal, bool, error) {
	u := r.Header.Get("X-User")
	if u == "" {
		return authorization.Principal{}, false, nil
	}
	perms := map[string]struct{}{}
	for _, p := range strings.Split(r.Header.Get("X-Perms"), ",") {
		if p != "" {
			perms[p] = struct{}{}
		}
	}
	return authorization.Principal{UserID: u, Permissions: perms}, true, nil
}

func setup(t *testing.T) http.Handler {
	t.Helper()
	pool := dbtest.Pool(t)
	if _, err := pool.Exec(context.Background(), `DELETE FROM platform.module_switches`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM platform.module_switches`) })
	svc := modules.NewService(pool, modules.Options{CacheTTL: time.Millisecond, Preconditions: map[string]modules.Precondition{
		"presence": func(context.Context) (string, error) { return "startup_gate_off", nil },
	}})
	mux := http.NewServeMux()
	transport.Register(mux, svc, headerAuth{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return httpx.Middleware(slog.New(slog.NewTextHandler(io.Discard, nil)), mux)
}

func do(h http.Handler, method, path, perms, body string) (int, map[string]any) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if perms != "-" {
		req.Header.Set("X-User", newID())
		req.Header.Set("X-Perms", perms)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" && rec.Code < 500 && rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		out["_cacheControl"] = cc
	}
	return rec.Code, out
}

func errCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func find(items []any, key string) map[string]any {
	for _, it := range items {
		if m := it.(map[string]any); m["key"] == key {
			return m
		}
	}
	return nil
}

func TestAuthorization(t *testing.T) {
	h := setup(t)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/admin/modules"},
		{"POST", "/api/v1/admin/modules/briefing/disable"},
		{"POST", "/api/v1/admin/modules/briefing/enable"},
	} {
		if code, _ := do(h, tc.method, tc.path, "-", `{}`); code != http.StatusUnauthorized {
			t.Errorf("anonymous %s %s = %d", tc.method, tc.path, code)
		}
		// Every other permission, including platform.admin, is not modules.manage.
		if code, _ := do(h, tc.method, tc.path, "platform.admin,platform.roles.manage,tickets.manage", `{"expectedVersion":0,"reasonCode":"not_needed"}`); code != http.StatusForbidden {
			t.Errorf("user without modules.manage %s %s = %d", tc.method, tc.path, code)
		}
	}
	if code, _ := do(h, "GET", "/api/v1/modules/status", "-", ""); code != http.StatusUnauthorized {
		t.Errorf("anonymous status = %d", code)
	}
}

func TestStatusIsKeysAndEnabledOnly(t *testing.T) {
	h := setup(t)
	code, body := do(h, "GET", "/api/v1/modules/status", "", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	items := body["items"].([]any)
	if sd := find(items, "servicedesk"); sd == nil || sd["enabled"] != true || len(sd) != 2 {
		t.Errorf("servicedesk status = %v (only key and enabled may be exposed)", sd)
	}
	if p := find(items, "presence"); p == nil || p["enabled"] != false {
		t.Errorf("presence = %v", p)
	}
	if o := find(items, "organization"); o == nil || o["enabled"] != true {
		t.Errorf("core organization must be listed as enabled: %v", o)
	}
}

func TestListContract(t *testing.T) {
	h := setup(t)
	code, body := do(h, "GET", "/api/v1/admin/modules", "modules.manage", "")
	if code != http.StatusOK || body["_cacheControl"] != nil {
		t.Fatalf("list = %d %v", code, body["_cacheControl"])
	}
	items := body["items"].([]any)
	changes := find(items, "changes")
	if changes == nil || changes["nameKey"] != "modules.changes.name" || changes["category"] != "infrastructure_operations" || changes["core"] != false ||
		changes["enabled"] != true || changes["state"] != "enabled" || changes["version"] != float64(0) || changes["changedAt"] != nil ||
		changes["blockedReason"] != nil {
		t.Fatalf("changes = %v", changes)
	}
	if got := strings.Join(toStrings(changes["requiredBy"]), ","); got != "endpoints,planning,security" {
		t.Errorf("requiredBy = %s", got)
	}
	if got := strings.Join(toStrings(changes["requires"]), ","); got != "assets,infrastructure,services" {
		t.Errorf("requires = %s", got)
	}
	presence := find(items, "presence")
	if presence["state"] != "disabled" || presence["blockedReason"] != "startup_gate_off" || toStrings(presence["startupGates"])[0] != "PRESENCE_ENABLED" {
		t.Errorf("presence = %v", presence)
	}
	if org := find(items, "organization"); org["core"] != true || org["state"] != "enabled" {
		t.Errorf("organization = %v", org)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestEnableDisableFlow(t *testing.T) {
	h := setup(t)
	const p = "modules.manage"
	code, body := do(h, "POST", "/api/v1/admin/modules/briefing/disable", p, `{"expectedVersion":0,"reasonCode":"not_needed"}`)
	if code != http.StatusOK || body["state"] != "disabled" || body["enabled"] != false || body["version"] != float64(1) ||
		body["reasonCode"] != "not_needed" || body["changedByUserId"] == nil || body["changedAt"] == nil {
		t.Fatalf("disable = %d %v", code, body)
	}
	_, status := do(h, "GET", "/api/v1/modules/status", "", "")
	if find(status["items"].([]any), "briefing")["enabled"] != false {
		t.Errorf("status must reflect the switch immediately")
	}
	if code, body := do(h, "POST", "/api/v1/admin/modules/briefing/disable", p, `{"expectedVersion":1,"reasonCode":"not_needed"}`); code != http.StatusConflict || errCode(body) != "platform.modules.no_change" {
		t.Errorf("repeat disable = %d %v", code, body)
	}
	if code, body := do(h, "POST", "/api/v1/admin/modules/briefing/enable", p, `{"expectedVersion":0,"reasonCode":"business_need"}`); code != http.StatusConflict || errCode(body) != "platform.modules.version_conflict" {
		t.Errorf("stale enable = %d %v", code, body)
	}
	if code, body := do(h, "POST", "/api/v1/admin/modules/briefing/enable", p, `{"expectedVersion":1,"reasonCode":"business_need"}`); code != http.StatusOK || body["state"] != "enabled" || body["version"] != float64(2) {
		t.Errorf("enable = %d %v", code, body)
	}
}

func TestRefusals(t *testing.T) {
	h := setup(t)
	const p = "modules.manage"
	code, body := do(h, "POST", "/api/v1/admin/modules/assets/disable", p, `{"expectedVersion":0,"reasonCode":"not_needed"}`)
	e := body["error"].(map[string]any)
	if code != http.StatusConflict || e["code"] != "platform.modules.required_by_enabled" || len(e["blockers"].([]any)) < 3 || !strings.Contains(e["message"].(string), "infrastructure") {
		t.Errorf("disable with enabled dependents = %d %v", code, body)
	}
	if code, body := do(h, "POST", "/api/v1/admin/modules/organization/disable", p, `{"expectedVersion":0,"reasonCode":"not_needed"}`); code != http.StatusConflict || errCode(body) != "platform.modules.not_switchable" {
		t.Errorf("core = %d %v", code, body)
	}
	if code, body := do(h, "POST", "/api/v1/admin/modules/nope/disable", p, `{"expectedVersion":0,"reasonCode":"not_needed"}`); code != http.StatusNotFound || errCode(body) != "platform.modules.not_found" {
		t.Errorf("unknown = %d %v", code, body)
	}
	if code, body := do(h, "POST", "/api/v1/admin/modules/presence/enable", p, `{"expectedVersion":0,"reasonCode":"business_need"}`); code != http.StatusConflict || errCode(body) != "platform.modules.blocked" {
		t.Errorf("blocked enable = %d %v", code, body)
	}
	for name, payload := range map[string]string{
		"no version":       `{"reasonCode":"not_needed"}`,
		"bad reason":       `{"expectedVersion":0,"reasonCode":"x"}`,
		"unknown field":    `{"expectedVersion":0,"reasonCode":"not_needed","enabled":true}`,
		"not json":         `nope`,
		"negative version": `{"expectedVersion":-1,"reasonCode":"not_needed"}`,
	} {
		if code, body := do(h, "POST", "/api/v1/admin/modules/briefing/disable", p, payload); code != http.StatusBadRequest {
			t.Errorf("%s = %d %v", name, code, body)
		}
	}
}
