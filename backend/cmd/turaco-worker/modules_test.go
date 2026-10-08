package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	endpointsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/modules"
)

// With Endpoints switched off the deployment tick still runs (safety path: kill-switch sweep) but starts no new
// rollout work. The capability audit record is written only by the full tick.
func TestDeploymentTickRunsOnlyTheSafetySweepWhileEndpointsIsOff(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := pool.Exec(ctx, `DELETE FROM platform.module_switches`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM platform.module_switches`) })
	mods := modules.NewService(pool, modules.Options{CacheTTL: time.Millisecond})
	admin := modules.Caller{UserID: "9b0a8c1e-0000-4000-8000-0000000000aa", CorrelationID: "r", CanManage: true}
	zero, one := 0, 1
	for _, key := range []string{"remoteaccess", "security", "endpoints"} {
		if _, err := mods.Disable(ctx, admin, key, "maintenance", &zero); err != nil {
			t.Fatal(err)
		}
	}
	runner := jobs.NewRunner(pool, jobs.RunnerOptions{Gate: mods.JobGate}, logger)
	if err := registerDeploymentEngine(runner, pool, true, mods); err != nil {
		t.Fatal(err)
	}
	audits := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE action='endpoints.deploy_write.enabled'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	drain := func() {
		if _, _, err := jobs.Enqueue(ctx, pool, jobs.EnqueueRequest{Type: endpointsapp.DeploymentTickJobType}); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 20; i++ {
			if ok, err := runner.RunOnce(ctx); err != nil || !ok {
				return
			}
		}
	}
	before := audits()
	drain()
	var done int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.jobs WHERE job_type=$1 AND status='pending'`, endpointsapp.DeploymentTickJobType).Scan(&done); err != nil || done != 0 {
		t.Fatalf("the tick must run (not stay pending) while endpoints is off: pending=%d %v", done, err)
	}
	if got := audits(); got != before {
		t.Fatalf("the full tick ran while endpoints was off (audit records %d -> %d)", before, got)
	}
	if _, err := mods.Enable(ctx, admin, "endpoints", "business_need", &one); err != nil {
		t.Fatal(err)
	}
	drain()
	if got := audits(); got != before+1 {
		t.Fatalf("the full tick must run once endpoints is on again (audit records %d -> %d)", before, got)
	}
}
