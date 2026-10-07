package repository_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// Tests for failure correlation, the work follow-up, the report and the security context (F9 G4).

type fakeFollowTasks struct {
	x     *exEnv
	tasks []application.FollowUpTask
	fail  bool
	// inactive makes the Tasks contract refuse the assignee (the Task is then created unassigned).
	inactive bool
}

func (f *fakeFollowTasks) CreateInTx(_ context.Context, _ pgx.Tx, in application.FollowUpTask) (string, bool, error) {
	if f.fail {
		return "", false, errors.New("tasks unavailable")
	}
	f.tasks = append(f.tasks, in)
	return f.x.newID(), in.AssignedUserID != "" && !f.inactive, nil
}

// grantOwner gives the Deployment owner a deployments read permission (what a follow-up Task needs to be assigned).
func (x *exEnv) grantOwner() {
	owner := x.cur().OwnerUserID
	x.approvers.perms[owner] = append(x.approvers.perms[owner], application.PermDeploymentsView)
}

type fakeFollowNotes struct{ intents []notifications.Intent }

func (f *fakeFollowNotes) Create(_ context.Context, _ pgx.Tx, in notifications.Intent) (bool, error) {
	f.intents = append(f.intents, in)
	return true, nil
}

type fakeSecurity struct {
	calls     int
	productID string
	devices   []string
	details   bool
	res       application.SecurityContext
}

func (f *fakeSecurity) DeploymentContext(_ context.Context, productID string, devices []string, details bool) (application.SecurityContext, error) {
	f.calls++
	f.productID, f.devices, f.details = productID, devices, details
	return f.res, nil
}

// clusterEnv schedules one ring of eight Devices (four Latitude, four Surface; all the same maker and OS), starts it and
// lets d1-d4 fail with the raw status "0x87D1041C" and d5-d6 succeed.
func clusterEnv(t *testing.T, name string) (*exEnv, *fakeFollowTasks, *fakeFollowNotes) {
	t.Helper()
	exts := []string{"d1", "d2", "d3", "d4", "d5", "d6", "d7", "d8"}
	x := newExEnv(t, name, exts...)
	var devs []application.SnapshotDevice
	for i, e := range exts {
		model := "Latitude"
		if i >= 4 {
			model = "Surface"
		}
		devs = append(devs, device(e, "PC-"+e, "windows", "10.0.22631", "corporate", "compliant", "Dell Inc.", model, "sn-"+x.suffix()+"-"+e))
	}
	x.patch(devs...)
	ft, fn := &fakeFollowTasks{x: x}, &fakeFollowNotes{}
	x.svc.WithFollowUps(ft, fn)
	x.schedule(ringSpec{name: "pilot", devs: exts, threshold: 1})
	x.start()
	x.tick()
	x.readBack()
	m, _ := x.prov.Management(context.Background())
	for _, e := range exts[:6] {
		var kept []intune.ObservationRecord
		for _, o := range m.Observations {
			if o.ExternalDeviceID != e || o.ArtifactExternalID != x.artifact {
				kept = append(kept, o)
			}
		}
		rec := intune.ObservationRecord{ExternalDeviceID: e, ArtifactExternalID: x.artifact, State: "applied", RawStatus: "installed"}
		if e <= "d4" {
			rec.State, rec.RawStatus = "failed", "0x87D1041C"
		}
		m.Observations = append(kept, rec)
	}
	x.prov.SetManagement(m)
	x.bridge()
	x.tick()
	return x, ft, fn
}

// cleanupCorrelation removes the findings and follow-ups of the Deployment after the test (they have no delete path).
func (x *exEnv) cleanupCorrelation() {
	x.t.Cleanup(func() {
		ctx := context.Background()
		conn, err := x.pool.Acquire(ctx)
		if err != nil {
			return
		}
		defer conn.Release()
		_, _ = conn.Exec(ctx, `SET session_replication_role = replica`)
		_, _ = conn.Exec(ctx, `DELETE FROM endpoints.deployment_followups WHERE deployment_id = $1::uuid`, x.dep.ID)
		_, _ = conn.Exec(ctx, `DELETE FROM endpoints.findings WHERE deployment_id = $1::uuid`, x.dep.ID)
		_, _ = conn.Exec(ctx, `RESET session_replication_role`)
	})
}

func (x *exEnv) correlate() {
	x.t.Helper()
	x.grantOwner()
	x.cleanupCorrelation()
	if err := x.svc.CorrelateDeployment(context.Background(), x.dep.ID, x.corr); err != nil {
		x.t.Fatalf("correlate: %v", err)
	}
}

func (x *exEnv) openClusters() map[string]string {
	x.t.Helper()
	rows, err := x.pool.Query(context.Background(), `SELECT cluster_key, detail::text FROM endpoints.findings
		WHERE kind = 'deployment_failure_cluster' AND deployment_id = $1::uuid AND status = 'open'`, x.dep.ID)
	if err != nil {
		x.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, d string
		if err := rows.Scan(&k, &d); err != nil {
			x.t.Fatal(err)
		}
		out[k] = d
	}
	return out
}

func TestFailureClustersAreRaisedIdempotentlyAndResolvedWhenTheDeploymentEnds(t *testing.T) {
	x, ft, fn := clusterEnv(t, "Cluster App")
	x.correlate()
	got := x.openClusters()
	if len(got) != 2 || got["error_code:0x87D1041C"] == "" || got["model:Latitude"] == "" {
		t.Fatalf("clusters = %v (want error_code and model; manufacturer, os_version and ring span every target)", got)
	}
	for k, d := range got {
		if !strings.Contains(d, x.dep.ID) || !strings.Contains(d, `"failed": 4`) {
			t.Fatalf("detail of %s = %s", k, d)
		}
	}
	raised := x.evCount("DeploymentFailureClusterDetected")
	if raised != 2 {
		t.Fatalf("cluster events = %d", raised)
	}
	// A second run changes nothing.
	x.correlate()
	if len(x.openClusters()) != 2 || x.evCount("DeploymentFailureClusterDetected") != raised {
		t.Fatal("second run raised again")
	}
	// ONE follow-up Task for the cluster, for the owner, due in two days, reference-based title, no free text.
	if len(ft.tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(ft.tasks))
	}
	task := ft.tasks[0]
	dep := x.cur()
	if task.DeploymentID != dep.ID || task.AssignedUserID != dep.OwnerUserID || task.Title != "Deployment "+dep.Reference+": failures cluster, review the cause" {
		t.Fatalf("task = %+v", task)
	}
	if d := time.Until(task.DueAt); d < 47*time.Hour || d > 49*time.Hour {
		t.Fatalf("due in %s", d)
	}
	if len(fn.intents) != 1 {
		t.Fatalf("notifications = %d", len(fn.intents))
	}
	in := fn.intents[0]
	if in.Category != application.DeploymentAttentionCategory || in.RecipientUserID != dep.OwnerUserID || in.LinkType != "deployment" || in.LinkID != dep.ID ||
		len(in.Params) != 1 || in.Params["title"] != dep.Reference || in.DedupeKey == "" {
		t.Fatalf("intent = %+v", in)
	}
	// The cluster is a Deployment finding: it does not appear in the Device finding list or on a Device.
	devFindings, err := x.repo().ListFindings(context.Background(), application.FindingFilter{Status: application.FindingOpen, Page: application.Page{Limit: 200}})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range devFindings.Items {
		if f.Kind == application.FindingDeploymentFailureCluster {
			t.Fatal("cluster finding in the device finding list")
		}
	}
	// Cancelling ends the Deployment: the clusters are resolved by the next run.
	v := x.cur().Version
	if _, err := x.svc.CancelDeployment(context.Background(), x.starterCaller(), x.starter, x.dep.ID, &v, "other"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	x.correlate()
	if n := len(x.openClusters()); n != 0 {
		t.Fatalf("open clusters after cancel = %d", n)
	}
	if x.depAudits("endpoints.deployment.failure_cluster_resolved") != 2 {
		t.Fatal("resolution not audited")
	}
	if len(ft.tasks) != 1 {
		t.Fatalf("tasks after cancel = %d", len(ft.tasks))
	}
}

func exportCaller(p application.Principal) application.Caller {
	return application.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: "req-export"}
}

func TestFollowUpTaskFailureKeepsTheFindingsAndTheFlagSwitchesTasksOff(t *testing.T) {
	x, ft, fn := clusterEnv(t, "Flag App")
	ft.fail = true
	x.grantOwner()
	if err := x.svc.CorrelateDeployment(context.Background(), x.dep.ID, x.corr); err != nil {
		t.Fatalf("a failing Task creation must not fail the step: %v", err)
	}
	if len(x.openClusters()) != 2 {
		t.Fatal("findings were rolled back by the failing follow-up")
	}
	var n int
	if err := x.pool.QueryRow(context.Background(), `SELECT count(*) FROM endpoints.deployment_followups WHERE deployment_id = $1::uuid`, x.dep.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("followups = %d (%v)", n, err)
	}
	ft.fail = false
	if _, err := x.pool.Exec(context.Background(), `UPDATE endpoints.deployments SET create_tasks = false WHERE id = $1::uuid`, x.dep.ID); err != nil {
		t.Fatal(err)
	}
	x.correlate()
	if len(x.openClusters()) != 2 {
		t.Fatal("findings are raised whatever the flag says")
	}
	if len(ft.tasks) != 0 || len(fn.intents) != 0 {
		t.Fatalf("create_tasks=false still created %d tasks, %d notifications", len(ft.tasks), len(fn.intents))
	}
}

func TestHaltedRingFollowUpIsPerRingAndReasonAndSkipsPeopleHalts(t *testing.T) {
	x := newExEnv(t, "Halt Follow App")
	ft, fn := &fakeFollowTasks{x: x}, &fakeFollowNotes{}
	x.svc.WithFollowUps(ft, fn)
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
	x.correlate()
	x.correlate()
	var titles []string
	for _, tk := range ft.tasks {
		titles = append(titles, tk.Title)
	}
	ref := x.cur().Reference
	want := []string{"Deployment " + ref + ": failures cluster, review the cause", "Deployment " + ref + ": ring halted, too many failures"}
	if len(titles) != 2 || !(titles[0] == want[0] && titles[1] == want[1]) {
		t.Fatalf("titles = %v", titles)
	}
	var withRing int
	if err := x.pool.QueryRow(context.Background(), `SELECT count(*) FROM endpoints.deployment_followups WHERE deployment_id = $1::uuid AND ring_id = $2::uuid`, x.dep.ID, x.ringID(1)).Scan(&withRing); err != nil || withRing != 1 {
		t.Fatalf("ring follow-ups = %d (%v)", withRing, err)
	}
	// The paused Deployment (reason ring_halted) adds nothing of its own; a second run is a no-op.
	if len(fn.intents) != 2 {
		t.Fatalf("notifications = %d", len(fn.intents))
	}
}

func TestPersonalHaltAndPauseCreateNoFollowUp(t *testing.T) {
	x := newExEnv(t, "Manual Follow App")
	ft := &fakeFollowTasks{x: x}
	x.svc.WithFollowUps(ft, &fakeFollowNotes{})
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1", "d2"}, threshold: 50})
	x.start()
	x.tick()
	v := x.cur().Version
	ctx := context.Background()
	if _, err := x.svc.HaltRing(ctx, x.starterCaller(), x.starter, x.dep.ID, x.ringID(1), &v, "quality_issue"); err != nil {
		t.Fatalf("halt ring: %v", err)
	}
	x.correlate()
	if len(ft.tasks) != 0 {
		t.Fatalf("a person's own halt created %d tasks", len(ft.tasks))
	}
}

func TestReportDerivesCountsRatesMedianAndReasons(t *testing.T) {
	x, _, _ := clusterEnv(t, "Report App")
	x.correlate()
	// Fix the times of the two successes: 60 s and 180 s from request to decision, so the median is 120 s.
	if _, err := x.pool.Exec(context.Background(), `UPDATE endpoints.deployment_targets SET assignment_requested_at = decided_at - interval '60 seconds'
		WHERE deployment_id = $1::uuid AND device_id = $2::uuid`, x.dep.ID, x.devID("d5")); err != nil {
		t.Fatal(err)
	}
	if _, err := x.pool.Exec(context.Background(), `UPDATE endpoints.deployment_targets SET assignment_requested_at = decided_at - interval '180 seconds'
		WHERE deployment_id = $1::uuid AND device_id = $2::uuid`, x.dep.ID, x.devID("d6")); err != nil {
		t.Fatal(err)
	}
	rep, err := x.svc.DeploymentReport(context.Background(), x.reader, x.dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Totals[application.TargetFailed] != 4 || rep.Totals[application.TargetSuccessful] != 2 {
		t.Fatalf("totals = %v", rep.Totals)
	}
	if rep.SuccessRatePercent == nil || *rep.SuccessRatePercent != 33.3 {
		t.Fatalf("rate = %v", rep.SuccessRatePercent)
	}
	if rep.MedianSecondsToSuccess == nil || *rep.MedianSecondsToSuccess != 120 {
		t.Fatalf("median = %v", rep.MedianSecondsToSuccess)
	}
	if len(rep.Rings) != 1 || rep.Rings[0].MedianSecondsToSuccess == nil || *rep.Rings[0].MedianSecondsToSuccess != 120 || rep.Rings[0].Run.ActivatedAt == nil {
		t.Fatalf("rings = %+v", rep.Rings)
	}
	if len(rep.TopFailureReasons) != 1 || rep.TopFailureReasons[0].Code != "0x87D1041C" || rep.TopFailureReasons[0].Count != 4 {
		t.Fatalf("reasons = %+v", rep.TopFailureReasons)
	}
	if len(rep.Clusters) != 2 || len(rep.FollowUps) != 1 || len(rep.Transitions) < 3 {
		t.Fatalf("clusters %d followups %d transitions %d", len(rep.Clusters), len(rep.FollowUps), len(rep.Transitions))
	}
	if rep.Deployment.StartedBy == nil || *rep.Deployment.StartedBy != x.starterID {
		t.Fatalf("started by %v", rep.Deployment.StartedBy)
	}
}

func TestReportAndRolloutPermissionsAndExport(t *testing.T) {
	x, _, _ := clusterEnv(t, "Export App")
	ctx := context.Background()
	// IDOR: the owner without a deployments read permission and strangers do not see the report; a bad id is not found.
	for _, p := range []application.Principal{
		{UserID: x.cur().OwnerUserID},
		{UserID: x.newID(), View: true},
		{UserID: x.newID(), SoftwareView: true, ManagementView: true},
	} {
		if _, err := x.svc.DeploymentReport(ctx, p, x.dep.ID); !errors.Is(err, application.ErrNotFound) {
			t.Fatalf("report without permission: %v", err)
		}
		if _, err := x.svc.DeploymentSecurityContext(ctx, p, x.dep.ID); !errors.Is(err, application.ErrNotFound) {
			t.Fatalf("security context without permission: %v", err)
		}
		if _, _, err := x.svc.ListRollouts(ctx, p, nil, application.Page{}); !errors.Is(err, application.ErrForbidden) {
			t.Fatalf("rollouts without permission: %v", err)
		}
		if _, err := x.svc.ExportReportRows(ctx, p, exportCaller(p), x.dep.ID, func(application.Deployment) {}, func(application.ReportRow) error { return nil }); !errors.Is(err, application.ErrNotFound) {
			t.Fatalf("export without permission: %v", err)
		}
	}
	if _, err := x.svc.DeploymentReport(ctx, x.reader, "not-a-uuid"); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("bad id: %v", err)
	}
	// Names only with endpoints.view.
	export := func(p application.Principal) (rows int, names int) {
		_, err := x.svc.ExportReportRows(ctx, p, exportCaller(p), x.dep.ID, func(application.Deployment) {}, func(r application.ReportRow) error {
			rows++
			if r.DeviceName != "" {
				names++
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return rows, names
	}
	if rows, names := export(x.reader); rows != 8 || names != 0 {
		t.Fatalf("export without endpoints.view: %d rows, %d names", rows, names)
	}
	withNames := x.reader
	withNames.View = true
	if rows, names := export(withNames); rows != 8 || names != 8 {
		t.Fatalf("export with endpoints.view: %d rows, %d names", rows, names)
	}
	// The rollout list shows the running Deployment with its progress; an unknown status is refused.
	rows, _, err := x.svc.ListRollouts(ctx, x.reader, []string{application.DeploymentRunning}, application.Page{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var found *application.RolloutRow
	for i := range rows {
		if rows[i].Deployment.ID == x.dep.ID {
			found = &rows[i]
		}
	}
	if found == nil || found.RingCount != 1 || found.Counts[application.TargetFailed] != 4 || found.CurrentPosition == nil || *found.CurrentPosition != 1 {
		t.Fatalf("rollout row = %+v", found)
	}
	if _, _, err := x.svc.ListRollouts(ctx, x.reader, []string{"bogus"}, application.Page{}); err == nil {
		t.Fatal("unknown status accepted")
	}
}

func TestSecurityContextScopeAndRedaction(t *testing.T) {
	x, _, _ := clusterEnv(t, "Security App")
	ctx := context.Background()
	sec := &fakeSecurity{res: application.SecurityContext{AdvisoryCount: 1, OpenFindings: 3,
		Advisories: []application.SecurityContextAdvisory{{ID: "a1", Reference: "ADV-1", Title: "private title", OpenFindings: 3}}}}
	x.svc.WithSecurity(sec)
	// Without security.view: counts only, and the port was asked for no details.
	res, err := x.svc.DeploymentSecurityContext(ctx, x.reader, x.dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Detailed || sec.details || len(res.Context.Advisories) != 0 || res.Context.AdvisoryCount != 1 || res.Context.OpenFindings != 3 {
		t.Fatalf("redacted result = %+v (details asked: %v)", res, sec.details)
	}
	// The scope is the product and the Devices of the targets, nothing else.
	if sec.productID != x.cur().ProductID || len(sec.devices) != 8 {
		t.Fatalf("scope = %s %d", sec.productID, len(sec.devices))
	}
	withSec := x.reader
	withSec.SecurityView = true
	res, err = x.svc.DeploymentSecurityContext(ctx, withSec, x.dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Detailed || !sec.details || len(res.Context.Advisories) != 1 {
		t.Fatalf("detailed result = %+v", res)
	}
	// A port that ignores the details flag is still redacted.
	sec.details = false
	res, err = x.svc.DeploymentSecurityContext(ctx, x.reader, x.dep.ID)
	if err != nil || len(res.Context.Advisories) != 0 {
		t.Fatalf("misbehaving port leaked: %+v %v", res, err)
	}
}

func TestDeploymentFindingSubjectAndKeyConstraints(t *testing.T) {
	x := newExEnv(t, "Check App")
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1", "d2"}, threshold: 50})
	ctx := context.Background()
	exec := func(q string, args ...any) error {
		_, err := x.pool.Exec(ctx, q, args...)
		return err
	}
	dev := x.devID("d1")
	bad := []struct{ name, sql string }{
		{"cluster with a device subject", `INSERT INTO endpoints.findings (kind, deployment_id, device_id, cluster_key) VALUES ('deployment_failure_cluster', $1::uuid, $2::uuid, 'model:x')`},
		{"cluster without a key", `INSERT INTO endpoints.findings (kind, deployment_id) VALUES ('deployment_failure_cluster', $1::uuid)`},
		{"cluster without a deployment", `INSERT INTO endpoints.findings (kind, device_id, cluster_key) VALUES ('deployment_failure_cluster', $2::uuid, 'model:x')`},
		{"device finding with a deployment", `INSERT INTO endpoints.findings (kind, device_id, deployment_id) VALUES ('no_asset_match', $2::uuid, $1::uuid)`},
		{"key on a device finding", `INSERT INTO endpoints.findings (kind, device_id, cluster_key) VALUES ('no_asset_match', $2::uuid, 'model:x')`},
	}
	for _, c := range bad {
		if err := exec(c.sql, x.dep.ID, dev); err == nil {
			t.Errorf("%s was accepted", c.name)
		}
	}
	if err := exec(`INSERT INTO endpoints.findings (kind, deployment_id, cluster_key) VALUES ('deployment_failure_cluster', $1::uuid, 'model:x')`, x.dep.ID); err != nil {
		t.Fatalf("valid cluster finding: %v", err)
	}
	if err := exec(`INSERT INTO endpoints.findings (kind, deployment_id, cluster_key) VALUES ('deployment_failure_cluster', $1::uuid, 'model:x')`, x.dep.ID); err == nil {
		t.Error("two open findings for one deployment and key")
	}
	if err := exec(`UPDATE endpoints.findings SET status = 'resolved', resolved_at = now() WHERE deployment_id = $1::uuid`, x.dep.ID); err != nil {
		t.Fatal(err)
	}
	if err := exec(`INSERT INTO endpoints.findings (kind, deployment_id, cluster_key) VALUES ('deployment_failure_cluster', $1::uuid, 'model:x')`, x.dep.ID); err != nil {
		t.Errorf("a resolved key can be raised again: %v", err)
	}
	// Follow-ups are unique per deployment, reason and ring; they cannot be deleted.
	task := x.newID()
	if err := exec(`INSERT INTO endpoints.deployment_followups (deployment_id, reason, task_id) VALUES ($1::uuid, 'failure_cluster', $2::uuid)`, x.dep.ID, task); err != nil {
		t.Fatal(err)
	}
	if err := exec(`INSERT INTO endpoints.deployment_followups (deployment_id, reason, task_id) VALUES ($1::uuid, 'failure_cluster', $2::uuid)`, x.dep.ID, x.newID()); err == nil {
		t.Error("duplicate follow-up accepted")
	}
	if err := exec(`DELETE FROM endpoints.deployment_followups WHERE deployment_id = $1::uuid`, x.dep.ID); err == nil {
		t.Error("follow-up deleted")
	}
	// Test cleanup of the rows above.
	conn, err := x.pool.Acquire(ctx)
	if err == nil {
		defer conn.Release()
		_, _ = conn.Exec(ctx, `SET session_replication_role = replica`)
		_, _ = conn.Exec(ctx, `DELETE FROM endpoints.deployment_followups WHERE deployment_id = $1::uuid`, x.dep.ID)
		_, _ = conn.Exec(ctx, `DELETE FROM endpoints.findings WHERE deployment_id = $1::uuid`, x.dep.ID)
		_, _ = conn.Exec(ctx, `RESET session_replication_role`)
	}
}

func TestCreateTasksFlagDefaultsToTrueAndCanBeSet(t *testing.T) {
	d := newDepEnv(t)
	prod, ver := d.approvedProductVersion("Flag Plan App", hashA)
	_ = prod
	ctx := context.Background()
	on, err := d.svc.CreateDeployment(ctx, d.caller(), d.planner, application.DeploymentInput{Name: "Flag on " + d.suffix(), SoftwareVersionID: ver.ID, Intent: application.IntentInstall})
	if err != nil || !on.CreateTasks {
		t.Fatalf("default flag: %v %+v", err, on.CreateTasks)
	}
	off := false
	upd, err := d.svc.UpdateDeployment(ctx, d.caller(), d.planner, on.ID, &on.Version, application.DeploymentInput{Name: on.Name, SoftwareVersionID: ver.ID, Intent: application.IntentInstall, CreateTasks: &off})
	if err != nil || upd.CreateTasks {
		t.Fatalf("update flag: %v %+v", err, upd.CreateTasks)
	}
	keep, err := d.svc.UpdateDeployment(ctx, d.caller(), d.planner, upd.ID, &upd.Version, application.DeploymentInput{Name: on.Name, SoftwareVersionID: ver.ID, Intent: application.IntentInstall})
	if err != nil || keep.CreateTasks {
		t.Fatalf("an unset flag stays: %v %+v", err, keep.CreateTasks)
	}
}
