package repository_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
)

// Review fixes of F9 G4: error codes, hysteresis, cleaned grouping, follow-up isolation and flood control, export
// limits and audit, security context cache, correlation order.

func (x *exEnv) forceSQL(sql string, args ...any) {
	x.t.Helper()
	ctx := context.Background()
	conn, err := x.pool.Acquire(ctx)
	if err != nil {
		x.t.Fatal(err)
	}
	defer conn.Release()
	// The history triggers forbid changes of targets and ring runs; the tests fabricate the situations they need.
	if _, err := conn.Exec(ctx, `SET session_replication_role = replica`); err != nil {
		x.t.Fatal(err)
	}
	defer func() { _, _ = conn.Exec(ctx, `RESET session_replication_role`) }()
	if _, err := conn.Exec(ctx, sql, args...); err != nil {
		x.t.Fatalf("%s: %v", sql, err)
	}
}

func (x *exEnv) retireObservations(retired bool) {
	x.t.Helper()
	x.forceSQL(`UPDATE endpoints.management_observations SET retired_at = CASE WHEN $2 THEN now() ELSE NULL END
		WHERE artifact_id IN (SELECT management_artifact_id FROM endpoints.deployment_ring_runs WHERE deployment_id = $1::uuid)`, x.dep.ID, retired)
}

func (x *exEnv) reportRows() []application.ReportRow {
	x.t.Helper()
	rows, err := x.repo().ReportTargets(context.Background(), x.dep.ID, "", 100)
	if err != nil {
		x.t.Fatal(err)
	}
	return rows
}

func TestErrorCodeFollowsExpiredReasonAndOnlyCurrentFailedObservations(t *testing.T) {
	x, _, _ := clusterEnv(t, "Code App")
	failed := map[string]application.ReportRow{}
	for _, r := range x.reportRows() {
		if r.State == "failed" {
			if r.ErrorCode != "0x87D1041C" {
				t.Fatalf("failed target with a current failed observation: code %q", r.ErrorCode)
			}
			failed[r.ID] = r
		}
	}
	if len(failed) != 4 {
		t.Fatalf("failed targets = %d", len(failed))
	}
	// An expired target never shows the provider's raw status, even when the observation still says failed.
	var expiredID string
	for id := range failed {
		expiredID = id
		break
	}
	x.forceSQL(`UPDATE endpoints.deployment_targets SET state = 'expired', state_reason = 'observation_expired' WHERE id = $1::uuid`, expiredID)
	// A retired observation no longer speaks for the target: Turaco's own reason code is used for the failed ones.
	x.retireObservations(true)
	for _, r := range x.reportRows() {
		switch {
		case r.ID == expiredID:
			if r.ErrorCode != "observation_expired" {
				t.Fatalf("expired target code %q", r.ErrorCode)
			}
		case r.State == "failed":
			if r.StateReason == nil || r.ErrorCode != *r.StateReason || strings.Contains(r.ErrorCode, "0x87") {
				t.Fatalf("failed target with a retired observation: code %q reason %v", r.ErrorCode, r.StateReason)
			}
		}
	}
	// The report's top reasons and the cluster grouping use the same rule.
	rep, err := x.svc.DeploymentReport(context.Background(), x.reader, x.dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range rep.TopFailureReasons {
		if strings.Contains(f.Code, "0x87") {
			t.Fatalf("report still shows the retired raw status: %+v", rep.TopFailureReasons)
		}
	}
	x.correlate()
	for k := range x.openClusters() {
		if strings.Contains(k, "0x87") {
			t.Fatalf("cluster on a retired raw status: %s", k)
		}
	}
}

func (x *exEnv) belowRuns(key string) (open bool, runs int) {
	x.t.Helper()
	err := x.pool.QueryRow(context.Background(), `SELECT below_runs FROM endpoints.findings WHERE kind = 'deployment_failure_cluster'
		AND deployment_id = $1::uuid AND cluster_key = $2 AND status = 'open'`, x.dep.ID, key).Scan(&runs)
	return err == nil, runs
}

func TestClusterIsResolvedOnlyAfterThreeConsecutiveRunsBelowTheThreshold(t *testing.T) {
	x, _, _ := clusterEnv(t, "Hysteresis App")
	const key = "error_code:0x87D1041C"
	x.correlate()
	if open, _ := x.belowRuns(key); !open {
		t.Fatal("cluster not raised")
	}
	x.retireObservations(true) // the raw status no longer counts: the group is below the threshold
	for run := 1; run <= 2; run++ {
		x.correlate()
		if open, n := x.belowRuns(key); !open || n != run {
			t.Fatalf("run %d: open=%v below_runs=%d", run, open, n)
		}
	}
	// A run back above the threshold resets the count.
	x.retireObservations(false)
	x.correlate()
	if open, n := x.belowRuns(key); !open || n != 0 {
		t.Fatalf("after recovery: open=%v below_runs=%d", open, n)
	}
	x.retireObservations(true)
	for run := 1; run <= 2; run++ {
		x.correlate()
		if open, _ := x.belowRuns(key); !open {
			t.Fatalf("resolved after only %d runs below the threshold", run)
		}
	}
	x.correlate()
	if open, _ := x.belowRuns(key); open {
		t.Fatal("not resolved after the third run below the threshold")
	}
	if x.depAudits("endpoints.deployment.failure_cluster_resolved") < 1 {
		t.Fatal("resolution not audited")
	}
}

func TestClusterGroupsOnTheCleanedValueSoBidiVariantsCannotCollide(t *testing.T) {
	x, _, _ := clusterEnv(t, "Clean App")
	// One failed Device's model carries a right-to-left override and a control character: after cleaning it is the
	// same model as the others, and exactly one cluster "model:Latitude" with all four failures exists.
	x.forceSQL(`UPDATE endpoints.devices SET model = 'Lati' || chr(8238) || 'tude' || chr(1) WHERE id = (
		SELECT device_id FROM endpoints.deployment_targets WHERE deployment_id = $1::uuid AND state = 'failed' ORDER BY id LIMIT 1)`, x.dep.ID)
	x.correlate()
	got := x.openClusters()
	d, ok := got["model:Latitude"]
	if !ok || !strings.Contains(d, `"failed": 4`) {
		t.Fatalf("clusters = %v", got)
	}
	for k := range got {
		if strings.ContainsRune(k, '‮') || strings.ContainsRune(k, '\x01') {
			t.Fatalf("uncleaned key %q", k)
		}
	}
}

func TestFailingFollowUpIsIsolatedAndDoesNotRollBackTheFindings(t *testing.T) {
	x, ft, _ := clusterEnv(t, "Isolation App")
	ft.fail = true
	x.cleanupCorrelation()
	x.grantOwner()
	if err := x.svc.CorrelateDeployment(context.Background(), x.dep.ID, x.corr); err != nil {
		t.Fatalf("correlate: %v", err)
	}
	if len(x.openClusters()) != 2 {
		t.Fatal("findings rolled back")
	}
	if n := x.count(`SELECT count(*) FROM endpoints.deployment_followups WHERE deployment_id = $1::uuid`, x.dep.ID); n != 0 {
		t.Fatalf("followups = %d", n)
	}
	// The next run creates the follow-up once the Tasks contract works again.
	ft.fail = false
	x.correlate()
	if n := x.count(`SELECT count(*) FROM endpoints.deployment_followups WHERE deployment_id = $1::uuid`, x.dep.ID); n != 1 {
		t.Fatalf("followups after recovery = %d", n)
	}
}

func TestGateReasonsGetOneFollowUpPerDeploymentAndTheRunCapIsHonoured(t *testing.T) {
	x := newExEnv(t, "Flood App")
	ft, fn := &fakeFollowTasks{x: x}, &fakeFollowNotes{}
	x.svc.WithFollowUps(ft, fn).WithFollowUpCap(1)
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1", "d2", "d3", "d4"}, threshold: 50})
	x.start()
	x.tick()
	x.readBack()
	for _, e := range []string{"d1", "d2", "d3"} {
		x.observe(e, "failed", time.Time{})
	}
	x.bridge()
	x.tick()
	x.wantRing(1, "halted")
	// The halt reason is a gate (a Deployment-wide reason), not one of the ring's own numbers.
	x.forceSQL(`UPDATE endpoints.deployment_ring_runs SET status_reason = 'package_gate_closed' WHERE deployment_id = $1::uuid AND status = 'halted'`, x.dep.ID)
	x.cleanupCorrelation()
	x.grantOwner()
	run := func() {
		t.Helper()
		if err := x.svc.CorrelateDeployment(context.Background(), x.dep.ID, x.corr); err != nil {
			t.Fatal(err)
		}
	}
	// Two candidates (cluster, gate) but a cap of one new Task per run.
	run()
	if len(ft.tasks) != 1 {
		t.Fatalf("tasks after run 1 = %d, want 1 (cap)", len(ft.tasks))
	}
	run()
	run()
	if len(ft.tasks) != 2 {
		t.Fatalf("tasks after run 3 = %d, want 2", len(ft.tasks))
	}
	var ringNull int
	if err := x.pool.QueryRow(context.Background(), `SELECT count(*) FROM endpoints.deployment_followups WHERE deployment_id = $1::uuid
		AND reason = 'package_gate_closed' AND ring_id IS NULL`, x.dep.ID).Scan(&ringNull); err != nil || ringNull != 1 {
		t.Fatalf("gate follow-up without ring = %d (%v)", ringNull, err)
	}
}

func TestFollowUpForAnOwnerWithoutAccessOrInactiveIsUnassignedAndCounted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(x *exEnv, ft *fakeFollowTasks)
	}{
		{"no deployments read access", func(*exEnv, *fakeFollowTasks) {}},
		{"inactive", func(x *exEnv, ft *fakeFollowTasks) { x.grantOwner(); ft.inactive = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, ft, fn := clusterEnv(t, "Unassigned App "+tc.name)
			tc.setup(x, ft)
			before, err := x.repo().RolloutOverview(context.Background(), 5)
			if err != nil {
				t.Fatal(err)
			}
			x.cleanupCorrelation()
			if err := x.svc.CorrelateDeployment(context.Background(), x.dep.ID, x.corr); err != nil {
				t.Fatal(err)
			}
			if len(ft.tasks) != 1 {
				t.Fatalf("tasks = %d", len(ft.tasks))
			}
			if tc.name != "inactive" && ft.tasks[0].AssignedUserID != "" {
				t.Fatalf("a Task was assigned to an owner without access: %+v", ft.tasks[0])
			}
			if len(fn.intents) != 0 {
				t.Fatalf("notified %d times", len(fn.intents))
			}
			if n := x.count(`SELECT count(*) FROM endpoints.deployment_followups WHERE deployment_id = $1::uuid AND unassigned`, x.dep.ID); n != 1 {
				t.Fatalf("unassigned follow-ups = %d", n)
			}
			after, err := x.repo().RolloutOverview(context.Background(), 5)
			if err != nil {
				t.Fatal(err)
			}
			if after.UnassignedFollowups != before.UnassignedFollowups+1 {
				t.Fatalf("unassigned count %d -> %d", before.UnassignedFollowups, after.UnassignedFollowups)
			}
		})
	}
}

func TestExportIsAuditedAndLimitedToTwoConcurrentRuns(t *testing.T) {
	x, _, _ := clusterEnv(t, "Export Limit App")
	p := x.reader
	started, release := make(chan struct{}, 4), make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			first := true
			_, err := x.svc.ExportReportRows(context.Background(), p, exportCaller(p), x.dep.ID, func(application.Deployment) {}, func(application.ReportRow) error {
				if first {
					first = false
					started <- struct{}{}
					<-release
				}
				return nil
			})
			errs <- err
		}()
	}
	<-started
	<-started
	_, err := x.svc.ExportReportRows(context.Background(), p, exportCaller(p), x.dep.ID, func(application.Deployment) { t.Error("begin called for a refused export") }, func(application.ReportRow) error { return nil })
	if !errors.Is(err, application.ErrExportBusy) {
		t.Fatalf("third export: %v", err)
	}
	close(release)
	wg.Wait()
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := x.svc.ExportReportRows(context.Background(), p, exportCaller(p), x.dep.ID, func(application.Deployment) {}, func(application.ReportRow) error { return nil }); err != nil {
		t.Fatalf("export after the others finished: %v", err)
	}
	if n := x.depAudits("endpoints.deployment.report_exported"); n != 3 {
		t.Fatalf("export audits = %d, want 3 (the refused one is not audited)", n)
	}
	var meta string
	if err := x.pool.QueryRow(context.Background(), `SELECT metadata::text FROM platform.audit_events WHERE action = 'endpoints.deployment.report_exported' AND target_id = $1 LIMIT 1`, x.dep.ID).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(meta, "PC-") {
		t.Fatalf("export audit carries device names: %s", meta)
	}
}

func TestSecurityContextIsCachedPerDeploymentAndDetailLevel(t *testing.T) {
	x, _, _ := clusterEnv(t, "Cache App")
	sec := &fakeSecurity{res: application.SecurityContext{AdvisoryCount: 1, OpenFindings: 1}}
	x.svc.WithSecurity(sec)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := x.svc.DeploymentSecurityContext(ctx, x.reader, x.dep.ID); err != nil {
			t.Fatal(err)
		}
	}
	if sec.calls != 1 {
		t.Fatalf("port calls = %d, want 1 within the TTL", sec.calls)
	}
	withSec := x.reader
	withSec.SecurityView = true
	if _, err := x.svc.DeploymentSecurityContext(ctx, withSec, x.dep.ID); err != nil {
		t.Fatal(err)
	}
	if sec.calls != 2 {
		t.Fatalf("detailed read shared the redacted cache entry: calls = %d", sec.calls)
	}
	// Authorization comes before the cache.
	if _, err := x.svc.DeploymentSecurityContext(ctx, application.Principal{UserID: x.newID()}, x.dep.ID); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("cached context served without permission: %v", err)
	}
}

func TestCorrelationVisitsTheLeastRecentlyCorrelatedFirst(t *testing.T) {
	x, _, _ := clusterEnv(t, "Order App")
	ctx := context.Background()
	since := time.Now().Add(-time.Hour)
	repo := x.repo()
	pos := func() (int, []string) {
		ids, err := repo.CorrelationDeployments(ctx, since, 10000)
		if err != nil {
			t.Fatal(err)
		}
		for i, id := range ids {
			if id == x.dep.ID {
				return i, ids
			}
		}
		t.Fatal("deployment is not a correlation candidate")
		return 0, nil
	}
	if err := repo.MarkCorrelated(ctx, x.dep.ID); err != nil {
		t.Fatal(err)
	}
	i, ids := pos()
	for _, id := range ids[:i] {
		var ok bool
		if err := x.pool.QueryRow(ctx, `SELECT last_correlated_at IS NULL OR last_correlated_at <= (SELECT last_correlated_at FROM endpoints.deployments WHERE id = $2::uuid)
			FROM endpoints.deployments WHERE id = $1::uuid`, id, x.dep.ID).Scan(&ok); err != nil || !ok {
			t.Fatalf("a more recently correlated deployment %s comes first (%v)", id, err)
		}
	}
	// A run stamps every Deployment it visited, whatever the outcome.
	x.forceSQL(`UPDATE endpoints.deployments SET last_correlated_at = NULL WHERE id = $1::uuid`, x.dep.ID)
	x.grantOwner()
	x.cleanupCorrelation()
	if err := x.svc.RunCorrelation(ctx, x.corr); err != nil {
		t.Logf("run: %v", err)
	}
	var stamped bool
	if err := x.pool.QueryRow(ctx, `SELECT last_correlated_at IS NOT NULL FROM endpoints.deployments WHERE id = $1::uuid`, x.dep.ID).Scan(&stamped); err != nil || !stamped {
		t.Fatalf("not stamped: %v %v", stamped, err)
	}
}
