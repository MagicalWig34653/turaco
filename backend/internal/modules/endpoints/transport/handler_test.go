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

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/transport"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
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

type assets struct{ existing map[string]bool }

func (assets) FindBySerial(context.Context, string) (application.AssetInfo, error) {
	return application.AssetInfo{}, application.ErrAssetNotFound
}
func (a assets) Exists(_ context.Context, id string) (bool, error) { return a.existing[id], nil }

const (
	admin = "00000000-0000-7000-8000-0000000000c3"
	asset = "00000000-0000-7000-8000-0000000000d4"
)

func serve(t *testing.T, a authorization.Authenticator, provider intune.Provider, syncEnabled bool) http.Handler {
	t.Helper()
	pool := dbtest.Pool(t)
	svc := application.NewService(repository.New(pool), assets{existing: map[string]bool{asset: true}}, provider, syncEnabled, nil)
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	transport.Register(mux, svc, a, logger)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.devices WHERE provider = 'intune' AND external_id LIKE 'http-%'`)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE actor_id = $1::uuid`, admin)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE actor_id = $1::uuid`, admin)
	})
	return httpx.Middleware(logger, mux)
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestEndpointsRequirePermissions(t *testing.T) {
	none := serve(t, as(admin), nil, true)
	viewer := serve(t, as(admin, "endpoints.view"), nil, true)
	id := "00000000-0000-7000-8000-0000000000e5"
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/v1/devices", ""},
		{"GET", "/api/v1/devices/" + id, ""},
		{"GET", "/api/v1/endpoint-findings", ""},
		{"POST", "/api/v1/devices/" + id + "/link", `{"assetId":"` + asset + `","reason":"correction"}`},
		{"POST", "/api/v1/devices/" + id + "/unlink", `{"reason":"correction"}`},
		{"POST", "/api/v1/endpoint-sync", `{}`},
	} {
		if rec := do(none, tc.method, tc.path, tc.body); rec.Code != http.StatusForbidden {
			t.Errorf("no permission: %s %s = %d", tc.method, tc.path, rec.Code)
		}
	}
	// A viewer reads but cannot change anything.
	if rec := do(viewer, "GET", "/api/v1/devices", ""); rec.Code != http.StatusOK {
		t.Errorf("viewer list = %d %s", rec.Code, rec.Body)
	}
	if rec := do(viewer, "GET", "/api/v1/devices/"+id, ""); rec.Code != http.StatusNotFound {
		t.Errorf("viewer unknown device = %d", rec.Code)
	}
	if rec := do(viewer, "GET", "/api/v1/devices/not-a-uuid", ""); rec.Code != http.StatusNotFound {
		t.Errorf("viewer bad id = %d", rec.Code)
	}
	for _, path := range []string{"/api/v1/devices/" + id + "/link", "/api/v1/devices/" + id + "/unlink", "/api/v1/endpoint-sync"} {
		if rec := do(viewer, "POST", path, `{"assetId":"`+asset+`","reason":"correction"}`); rec.Code != http.StatusForbidden {
			t.Errorf("viewer POST %s = %d", path, rec.Code)
		}
	}
	unauthenticated := serve(t, fakeAuth{}, nil, true)
	if rec := do(unauthenticated, "GET", "/api/v1/devices", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated = %d", rec.Code)
	}
}

func TestSyncLinkAndUnlinkOverHTTP(t *testing.T) {
	fake := intune.NewFake()
	fake.SetDevices(intune.DeviceRecord{ExternalID: "http-1", Name: "HTTP-PC", SerialNumber: "HTTP-SN", OSPlatform: "windows"})
	fake.SetSoftware("http-1", intune.SoftwareRecord{Name: "Unknown Http Tool", Version: "1"})
	manage := serve(t, as(admin, "endpoints.manage"), fake, true)

	rec := do(manage, "POST", "/api/v1/endpoint-sync", `{}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"devicesCreated":1`) {
		t.Fatalf("sync = %d %s", rec.Code, rec.Body)
	}
	rec = do(manage, "GET", "/api/v1/devices?q=HTTP-PC&linked=false", "")
	var list struct {
		Items []struct {
			ID      string `json:"id"`
			Version int    `json:"version"`
		} `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != http.StatusOK || len(list.Items) != 1 {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	id := list.Items[0].ID

	rec = do(manage, "GET", "/api/v1/devices/"+id, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"rawName":"Unknown Http Tool"`) ||
		!strings.Contains(rec.Body.String(), `"kind":"no_asset_match"`) || !strings.Contains(rec.Body.String(), `"kind":"unmatched_software"`) {
		t.Fatalf("get = %d %s", rec.Code, rec.Body)
	}
	if rec := do(manage, "GET", "/api/v1/endpoint-findings?kind=no_asset_match&deviceId="+id, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"deviceName":"HTTP-PC"`) {
		t.Errorf("findings = %d %s", rec.Code, rec.Body)
	}
	if rec := do(manage, "GET", "/api/v1/endpoint-findings?kind=bogus", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad kind = %d", rec.Code)
	}

	if rec := do(manage, "POST", "/api/v1/devices/"+id+"/link", `{"assetId":"`+asset+`","reason":"free text"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad reason = %d", rec.Code)
	}
	if rec := do(manage, "POST", "/api/v1/devices/"+id+"/link", `{"assetId":"`+asset+`","reason":"correction","expectedVersion":99}`); rec.Code != http.StatusConflict {
		t.Errorf("stale version = %d", rec.Code)
	}
	rec = do(manage, "POST", "/api/v1/devices/"+id+"/link", `{"assetId":"`+asset+`","reason":"correction"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"assetLinkSource":"manual"`) {
		t.Fatalf("link = %d %s", rec.Code, rec.Body)
	}
	rec = do(manage, "POST", "/api/v1/devices/"+id+"/unlink", `{"reason":"wrong_asset"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"autoLinkBlocked":true`) {
		t.Fatalf("unlink = %d %s", rec.Code, rec.Body)
	}
	if rec := do(manage, "POST", "/api/v1/devices/"+id+"/unlink", `{"reason":"wrong_asset"}`); rec.Code != http.StatusConflict {
		t.Errorf("unlink twice = %d", rec.Code)
	}
	if rec := do(manage, "POST", "/api/v1/devices/"+id+"/link", `{"assetId":"00000000-0000-7000-8000-0000000000ff","reason":"correction"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown asset = %d", rec.Code)
	}
}

func TestSyncDisabledAndNotConfigured(t *testing.T) {
	if rec := do(serve(t, as(admin, "endpoints.manage"), intune.NewFake(), false), "POST", "/api/v1/endpoint-sync", `{}`); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), "endpoints.sync_disabled") {
		t.Errorf("disabled = %d %s", rec.Code, rec.Body)
	}
	if rec := do(serve(t, as(admin, "endpoints.manage"), intune.NotConfigured{}, true), "POST", "/api/v1/endpoint-sync", `{}`); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), "endpoints.provider_not_configured") {
		t.Errorf("not configured = %d %s", rec.Code, rec.Body)
	}
}
