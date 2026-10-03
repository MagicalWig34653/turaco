package transport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
func (a assets) ByID(_ context.Context, id string) (application.AssetInfo, bool, error) {
	return application.AssetInfo{ID: id, Status: "available"}, a.existing[id], nil
}

const (
	admin = "00000000-0000-7000-8000-0000000000c3"
	asset = "00000000-0000-7000-8000-0000000000d4"
)

func serve(t *testing.T, a authorization.Authenticator, provider intune.Provider, syncEnabled bool) http.Handler {
	t.Helper()
	return serveCooldown(t, a, provider, syncEnabled, 0)
}

func serveCooldown(t *testing.T, a authorization.Authenticator, provider intune.Provider, syncEnabled bool, cooldown time.Duration) http.Handler {
	t.Helper()
	pool := dbtest.Pool(t)
	svc := application.NewService(repository.New(pool), assets{existing: map[string]bool{asset: true}}, provider, syncEnabled, nil).WithSyncCooldown(cooldown)
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	transport.Register(mux, svc, a, logger)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.management_observations WHERE device_id IN (SELECT id FROM endpoints.devices WHERE provider = 'intune' AND external_id LIKE 'http-%')`)
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.device_group_memberships WHERE device_id IN (SELECT id FROM endpoints.devices WHERE provider = 'intune' AND external_id LIKE 'http-%')`)
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.devices WHERE provider = 'intune' AND external_id LIKE 'http-%'`)
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.provider_sync_state WHERE provider = 'intune'`)
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
	manage := serve(t, as(admin, "endpoints.manage", "assets.view"), fake, true)

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

	if rec := do(manage, "POST", "/api/v1/devices/"+id+"/link", `{"assetId":"`+asset+`","reason":"free text","expectedVersion":1}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad reason = %d", rec.Code)
	}
	// expectedVersion is required on link and unlink.
	if rec := do(manage, "POST", "/api/v1/devices/"+id+"/link", `{"assetId":"`+asset+`","reason":"correction"}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "endpoints.invalid_request") {
		t.Errorf("link without version = %d %s", rec.Code, rec.Body)
	}
	if rec := do(manage, "POST", "/api/v1/devices/"+id+"/unlink", `{"reason":"correction"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unlink without version = %d", rec.Code)
	}
	// Linking needs assets.view as well as endpoints.manage.
	noAssets := serve(t, as(admin, "endpoints.manage"), fake, true)
	if rec := do(noAssets, "POST", "/api/v1/devices/"+id+"/link", fmt.Sprintf(`{"assetId":"%s","reason":"correction","expectedVersion":%d}`, asset, list.Items[0].Version)); rec.Code != http.StatusForbidden {
		t.Errorf("link without assets.view = %d", rec.Code)
	}
	if rec := do(manage, "POST", "/api/v1/devices/"+id+"/link", `{"assetId":"`+asset+`","reason":"correction","expectedVersion":99}`); rec.Code != http.StatusConflict {
		t.Errorf("stale version = %d", rec.Code)
	}
	rec = do(manage, "POST", "/api/v1/devices/"+id+"/link", fmt.Sprintf(`{"assetId":"%s","reason":"correction","expectedVersion":%d}`, asset, list.Items[0].Version))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"assetLinkSource":"manual"`) {
		t.Fatalf("link = %d %s", rec.Code, rec.Body)
	}
	rec = do(manage, "POST", "/api/v1/devices/"+id+"/unlink", fmt.Sprintf(`{"reason":"wrong_asset","expectedVersion":%d}`, list.Items[0].Version+1))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"autoLinkBlocked":true`) {
		t.Fatalf("unlink = %d %s", rec.Code, rec.Body)
	}
	if rec := do(manage, "POST", "/api/v1/devices/"+id+"/unlink", fmt.Sprintf(`{"reason":"wrong_asset","expectedVersion":%d}`, list.Items[0].Version+2)); rec.Code != http.StatusConflict {
		t.Errorf("unlink twice = %d", rec.Code)
	}
	if rec := do(manage, "POST", "/api/v1/devices/"+id+"/link", fmt.Sprintf(`{"assetId":"00000000-0000-7000-8000-0000000000ff","reason":"correction","expectedVersion":%d}`, list.Items[0].Version+2)); rec.Code != http.StatusBadRequest {
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

func TestManagementEndpointsOverHTTP(t *testing.T) {
	fake := intune.NewFake()
	fake.SetDevices(intune.DeviceRecord{ExternalID: "http-m1", Name: "HTTP-MGMT", SerialNumber: "HTTP-MSN", OSPlatform: "windows"})
	fake.SetManagement(intune.ManagementSnapshot{
		Filters: []intune.FilterRecord{{ExternalID: "http-f1", Name: "Http Filter", Platform: "windows", Rule: `(device.model -eq "X")`}},
		Artifacts: []intune.ArtifactRecord{{ExternalID: "http-a1", Kind: "configuration_profile", Name: "Http Profile", Platform: "windows", AssignmentsKnown: true,
			Assignments: []intune.AssignmentRecord{{ProviderAssignmentID: "x1", TargetKind: "group", TargetGroupExternalID: "g1", Mode: "include", FilterExternalID: "http-f1", FilterMode: "include"}}}},
		Observations: []intune.ObservationRecord{{ExternalDeviceID: "http-m1", ArtifactExternalID: "http-a1", State: "failed", RawStatus: "Error"}},
	})
	defer func() {
		pool := dbtest.Pool(t)
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.management_observations WHERE device_id IN (SELECT id FROM endpoints.devices WHERE provider = 'intune' AND external_id LIKE 'http-%')`)
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.device_group_memberships WHERE device_id IN (SELECT id FROM endpoints.devices WHERE provider = 'intune' AND external_id LIKE 'http-%')`)
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.devices WHERE provider = 'intune' AND external_id LIKE 'http-%'`)
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.provider_sync_state WHERE provider = 'intune'`)
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.management_assignments WHERE artifact_id IN (SELECT id FROM endpoints.management_artifacts WHERE provider = 'intune' AND external_id LIKE 'http-%')`)
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.management_artifacts WHERE provider = 'intune' AND external_id LIKE 'http-%'`)
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.management_filters WHERE provider = 'intune' AND external_id LIKE 'http-%'`)
	}()
	manage := serve(t, as(admin, "endpoints.manage"), fake, true)
	rec := do(manage, "POST", "/api/v1/endpoint-sync", `{}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"artifactsCreated":1`) || !strings.Contains(rec.Body.String(), `"assignmentsOpened":1`) ||
		!strings.Contains(rec.Body.String(), `"observationsCreated":1`) || !strings.Contains(rec.Body.String(), `"providerFindingsRaised":1`) {
		t.Fatalf("sync = %d %s", rec.Code, rec.Body)
	}
	rec = do(manage, "GET", "/api/v1/management-artifacts?q=http%20pro&kind=configuration_profile&platform=windows", "")
	var list struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != http.StatusOK || len(list.Items) != 1 {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	id := list.Items[0].ID
	view := serve(t, as(admin, "endpoint.management.view", "organization.directory.view"), fake, true)
	// Without organization.directory.view the provider group id is not returned.
	noDir := serve(t, as(admin, "endpoint.management.view"), fake, true)
	rec = do(noDir, "GET", "/api/v1/management-artifacts/"+id, "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"g1"`) || !strings.Contains(rec.Body.String(), `"targetGroupExternalId":null`) {
		t.Fatalf("get without directory view = %d %s", rec.Code, rec.Body)
	}
	rec = do(view, "GET", "/api/v1/management-artifacts/"+id, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"current":true`) || !strings.Contains(rec.Body.String(), `"targetGroupExternalId":"g1"`) ||
		!strings.Contains(rec.Body.String(), `"name":"Http Filter"`) || !strings.Contains(rec.Body.String(), `"failed":1`) {
		t.Fatalf("get = %d %s", rec.Code, rec.Body)
	}
	if rec := do(view, "GET", "/api/v1/management-filters?q=http", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"rule":"(device.model -eq \"X\")"`) {
		t.Errorf("filters = %d %s", rec.Code, rec.Body)
	}
	if rec := do(view, "GET", "/api/v1/management-artifacts?kind=bogus", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad kind = %d", rec.Code)
	}
	if rec := do(view, "GET", "/api/v1/management-artifacts/00000000-0000-7000-8000-0000000000e5", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown artifact = %d", rec.Code)
	}
	// Device observations need device access as well.
	rec = do(manage, "GET", "/api/v1/devices?q=HTTP-MGMT", "")
	var devs struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &devs)
	if len(devs.Items) != 1 {
		t.Fatalf("devices = %s", rec.Body)
	}
	path := "/api/v1/devices/" + devs.Items[0].ID + "/management-observations"
	if rec := do(view, "GET", path, ""); rec.Code != http.StatusForbidden {
		t.Errorf("management view without device access = %d", rec.Code)
	}
	if rec := do(serve(t, as(admin, "endpoints.view"), fake, true), "GET", path, ""); rec.Code != http.StatusForbidden {
		t.Errorf("device view without management access = %d", rec.Code)
	}
	if rec := do(serve(t, as(admin, "endpoints.view", "endpoint.management.view"), fake, true), "GET", path, ""); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"normalizedState":"failed"`) || !strings.Contains(rec.Body.String(), `"artifactName":"Http Profile"`) {
		t.Errorf("observations = %d %s", rec.Code, rec.Body)
	}
	if rec := do(manage, "GET", "/api/v1/devices/00000000-0000-7000-8000-0000000000e5/management-observations", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown device = %d", rec.Code)
	}
	// Without any permission nothing is readable.
	none := serve(t, as(admin), fake, true)
	for _, p := range []string{"/api/v1/management-artifacts", "/api/v1/management-artifacts/" + id, "/api/v1/management-filters", path} {
		if rec := do(none, "GET", p, ""); rec.Code != http.StatusForbidden {
			t.Errorf("no permission: GET %s = %d", p, rec.Code)
		}
	}
}

func TestSyncCooldownReturns429(t *testing.T) {
	manage := serveCooldown(t, as(admin, "endpoints.manage"), intune.NewFake(), true, time.Minute)
	if rec := do(manage, "POST", "/api/v1/endpoint-sync", `{}`); rec.Code != http.StatusOK {
		t.Fatalf("first sync = %d %s", rec.Code, rec.Body)
	}
	rec := do(manage, "POST", "/api/v1/endpoint-sync", `{}`)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "endpoints.sync_cooldown") {
		t.Fatalf("second sync = %d %s", rec.Code, rec.Body)
	}
}
