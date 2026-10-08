package wiring_test

import (
	"context"
	"testing"

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
	if _, err := pool.Exec(ctx, `UPDATE presence.settings SET dpia_recorded_on=current_date`); err != nil {
		t.Fatal(err)
	}
	if got := blockedReason(t, on, "presence"); got != "" {
		t.Errorf("presence with startup gate and DPIA: %q", got)
	}
	if got := blockedReason(t, on, "remoteaccess"); got != "" {
		t.Errorf("remoteaccess with providers: %q", got)
	}
}
