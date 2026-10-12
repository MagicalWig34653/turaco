package wiring_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/modules"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

func blockedReason(t *testing.T, svc *modules.Service, key string) string {
	t.Helper()
	list, err := svc.List(context.Background(), modules.Caller{UserID: "u", CorrelationID: "r", CanManage: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range list {
		if in.Module.Key == key {
			return in.BlockedReason
		}
	}
	t.Fatalf("module %s missing", key)
	return ""
}

func TestModulePreconditionsKeepTheirOwnGates(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()

	off := wiring.Modules(pool, wiring.ModuleGates{})
	for _, key := range []string{"presence", "ai"} {
		if got := blockedReason(t, off, key); got != wiring.BlockedStartupGateOff {
			t.Errorf("%s without its startup gate: blocked reason %q", key, got)
		}
	}
	if got := blockedReason(t, off, "remoteaccess"); got != wiring.BlockedNoProvidersConfigure {
		t.Errorf("remoteaccess without providers: %q", got)
	}
	if got := blockedReason(t, off, "servicedesk"); got != "" {
		t.Errorf("servicedesk has no precondition, got %q", got)
	}

	var dpia *string
	if err := pool.QueryRow(ctx, `SELECT dpia_recorded_on::text FROM presence.settings`).Scan(&dpia); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE presence.settings SET dpia_recorded_on=$1::date`, dpia)
	})
	gates := wiring.ModuleGates{PresenceEnabled: true, AIEnabled: true, RemoteAccessProviders: []string{"hoptodesk"}}
	on := wiring.Modules(pool, gates)
	if _, err := pool.Exec(ctx, `UPDATE presence.settings SET dpia_recorded_on=NULL, external_sources_enabled=false`); err != nil {
		t.Fatal(err)
	}
	if got := blockedReason(t, on, "presence"); got != wiring.BlockedDPIANotRecorded {
		t.Errorf("presence without recorded DPIA: %q", got)
	}
	if _, err := pool.Exec(ctx, `UPDATE presence.settings SET dpia_recorded_on=current_date, enabled=false`); err != nil {
		t.Fatal(err)
	}
	if got := blockedReason(t, on, "presence"); got != wiring.BlockedRuntimeSettingOff {
		t.Errorf("presence whose own runtime setting is off must be blocked, never reported on: %q", got)
	}
	if _, err := pool.Exec(ctx, `UPDATE presence.settings SET enabled=true`); err != nil {
		t.Fatal(err)
	}
	if got := blockedReason(t, on, "presence"); got != "" {
		t.Errorf("presence with startup gate and DPIA: %q", got)
	}
	if got := blockedReason(t, on, "remoteaccess"); got != "" {
		t.Errorf("remoteaccess with providers: %q", got)
	}
}

// First-time configuration must be possible while the module switch is off: the admin routes stay reachable, the
// functional routes do not, and the switch can be enabled once the module's own setting is on.
func TestPresenceCanBeConfiguredBeforeItsSwitchIsOn(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DELETE FROM platform.module_switches`); err != nil {
		t.Fatal(err)
	}
	var dpia *string
	var enabled bool
	if err := pool.QueryRow(ctx, `SELECT dpia_recorded_on::text, enabled FROM presence.settings`).Scan(&dpia, &enabled); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform.module_switches`)
		_, _ = pool.Exec(context.Background(), `UPDATE presence.settings SET dpia_recorded_on=$1::date, enabled=$2`, dpia, enabled)
	})
	if _, err := pool.Exec(ctx, `UPDATE presence.settings SET dpia_recorded_on=NULL, enabled=false`); err != nil {
		t.Fatal(err)
	}
	svc := wiring.Modules(pool, wiring.ModuleGates{PresenceEnabled: true})
	admin := modules.Caller{UserID: "9b0a8c1e-0000-4000-8000-0000000000bb", CorrelationID: "r", CanManage: true}
	zero := 0

	auth := stubAuth{}
	h := svc.Gate(auth, slog.New(slog.NewTextHandler(io.Discard, nil)), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	code := func(method, path string) int {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("X-User", "u")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if _, err := svc.Enable(ctx, admin, "presence", "business_need", &zero); err == nil {
		t.Fatal("enable must be refused while the module's own setting is unmet")
	}
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/v1/presence/settings", http.StatusNoContent}, {"PUT", "/api/v1/presence/settings", http.StatusNoContent},
		{"POST", "/api/v1/presence/settings/purge", http.StatusNoContent}, {"GET", "/api/v1/presence/entries", http.StatusNotFound},
		{"GET", "/api/v1/presence/availability", http.StatusNotFound},
	} {
		if got := code(c.method, c.path); got != c.want {
			t.Errorf("%s %s with the switch off = %d, want %d", c.method, c.path, got, c.want)
		}
	}
	// The administrator records the DPIA and turns the module's own setting on (what PUT /presence/settings does) ...
	if _, err := pool.Exec(ctx, `UPDATE presence.settings SET dpia_recorded_on=current_date, enabled=true`); err != nil {
		t.Fatal(err)
	}
	// ... and can now enable the switch.
	if got, err := svc.Enable(ctx, admin, "presence", "business_need", &zero); err != nil || !got.Enabled {
		t.Fatalf("enable after configuration = %+v, %v", got, err)
	}
	if got := code("GET", "/api/v1/presence/entries"); got != http.StatusNoContent {
		t.Errorf("functional route with the switch on = %d", got)
	}
}

type stubAuth struct{}

func (stubAuth) Authenticate(r *http.Request) (authorization.Principal, bool, error) {
	return authorization.Principal{UserID: r.Header.Get("X-User")}, r.Header.Get("X-User") != "", nil
}

// Time Tracking cannot be switched on before the works-council co-determination basis is recorded (F17 design 7.4).
func TestTimeTrackingNeedsRecordedCodetermination(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE timetracking.settings SET codetermination_basis=NULL, codetermination_recorded_on=NULL`)
	})
	svc := wiring.Modules(pool, wiring.ModuleGates{})
	if _, err := pool.Exec(ctx, `UPDATE timetracking.settings SET codetermination_basis=NULL, codetermination_recorded_on=NULL`); err != nil {
		t.Fatal(err)
	}
	if got := blockedReason(t, svc, "timetracking"); got != wiring.BlockedCodeterminationNotRecorded {
		t.Errorf("without a recorded basis: %q", got)
	}
	if _, err := pool.Exec(ctx, `UPDATE timetracking.settings SET codetermination_basis='not_applicable', codetermination_recorded_on=current_date`); err != nil {
		t.Fatal(err)
	}
	if got := blockedReason(t, svc, "timetracking"); got != "" {
		t.Errorf("with a recorded basis: %q", got)
	}
}
