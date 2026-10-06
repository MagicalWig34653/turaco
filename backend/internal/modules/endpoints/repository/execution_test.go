package repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

// Tests for Deployment execution (F9 G3): the engine runs against the Fake writer and the Fake provider through
// the real management ingestion.

type exEnv struct {
	*depEnv
	prov      *intune.Fake
	wr        *intune.FakeWriter
	starterID string
	starter   application.Principal
	exec      application.Principal
	prod      application.SoftwareProduct
	ver       application.SoftwareVersion
	pk        application.SoftwarePackage
	artifact  string
	dep       application.Deployment
}

type ringSpec struct {
	name      string
	devs      []string
	threshold int
	soak      int
	window    bool
	approval  bool
	maxTarget *int
}

func newExEnv(t *testing.T, name string, ext ...string) *exEnv {
	t.Helper()
	d := newDepEnv(t)
	ctx := context.Background()
	x := &exEnv{depEnv: d, prov: intune.NewFake()}
	x.wr = intune.NewFakeWriter(x.prov)
	d.svc.WithDeployWrite(true, x.wr)
	x.starterID = d.newID()
	x.starter = application.Principal{UserID: x.starterID, DeploymentsExecute: true, DeploymentsHighImpact: true, View: true}
	x.exec = x.starter
	t.Cleanup(func() {
		conn, err := d.pool.Acquire(ctx)
		if err != nil {
			return
		}
		defer conn.Release()
		_, _ = conn.Exec(ctx, `SET session_replication_role = replica`)
		for _, tbl := range []string{"deployment_attempts", "deployment_target_transitions", "deployment_targets", "deployment_ring_transitions", "deployment_ring_runs"} {
			_, _ = conn.Exec(ctx, `DELETE FROM endpoints.`+tbl+` WHERE deployment_id IN (SELECT id FROM endpoints.deployments WHERE created_by = $1)`, d.user)
		}
		_, _ = conn.Exec(ctx, `DELETE FROM endpoints.findings WHERE kind = 'deployment_evidence_conflict' AND device_id IN (SELECT id FROM endpoints.devices WHERE provider = $1)`, d.provider)
		_, _ = conn.Exec(ctx, `RESET session_replication_role`)
	})
	if len(ext) == 0 {
		ext = []string{"d1", "d2", "d3", "d4"}
	}
	var devs []application.SnapshotDevice
	for _, e := range ext {
		devs = append(devs, device(e, "PC-"+e, "windows", "10.0.22631", "corporate", "compliant", "Dell Inc.", "Latitude", "sn-"+d.suffix()+"-"+e))
	}
	d.ingest(devs...)
	x.prod, x.ver = d.approvedProductVersion(name, hashA)
	pk, err := d.svc.PackageVersion(ctx, d.caller(), d.pkgr, x.ver.ID, &x.ver.Version)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := d.svc.PublishPackage(ctx, d.caller(), d.pkgr, pk.ID, &pk.Version)
	if err != nil {
		t.Fatal(err)
	}
	x.artifact = *pub.ManagementArtifactExternalID
	x.prov.SetManagement(intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{{ExternalID: x.artifact, Kind: "application", Name: name, Platform: "windows", AssignmentsKnown: true}}})
	x.bridge()
	if res, err := d.svc.SyncPackages(ctx, d.caller(), d.pkgr); err != nil || res.Linked != 1 {
		t.Fatalf("link package: %v %+v", err, res)
	}
	x.pk = d.pkg(pk.ID)
	return x
}

// bridge ingests the Fake provider's current management data through the normal management ingestion.
func (x *exEnv) bridge() {
	x.t.Helper()
	m, err := x.prov.Management(context.Background())
	if err != nil {
		x.t.Fatal(err)
	}
	x.mingest(true, m)
}

// observe sets the observation of the artifact on a Device in the Fake provider (at zero: the time of the next ingestion).
func (x *exEnv) observe(devExt, state string, at time.Time) {
	x.t.Helper()
	m, _ := x.prov.Management(context.Background())
	var kept []intune.ObservationRecord
	for _, o := range m.Observations {
		if o.ExternalDeviceID != devExt || o.ArtifactExternalID != x.artifact {
			kept = append(kept, o)
		}
	}
	m.Observations = append(kept, intune.ObservationRecord{ExternalDeviceID: devExt, ArtifactExternalID: x.artifact, State: state, RawStatus: state, ObservedAt: at})
	x.prov.SetManagement(m)
}

func (x *exEnv) devID(ext string) string { return x.device(ext).ID }

func (x *exEnv) starterCaller() application.Caller {
	return application.Caller{Actor: audit.UserActor(x.starterID), CorrelationID: x.corr}
}

// schedule creates and schedules a plan of the given rings (every ring after the first uses a Change window).
func (x *exEnv) schedule(rings ...ringSpec) application.Deployment {
	x.t.Helper()
	ctx := context.Background()
	dep, err := x.svc.CreateDeployment(ctx, x.caller(), x.planner, application.DeploymentInput{Name: "Rollout " + x.suffix(), SoftwareVersionID: x.ver.ID, Intent: application.IntentInstall})
	if err != nil {
		x.t.Fatal(err)
	}
	future := time.Now().Add(48 * time.Hour)
	past := time.Now().Add(-time.Hour)
	for i, r := range rings {
		var ids []string
		for _, e := range r.devs {
			ids = append(ids, x.devID(e))
		}
		set := x.set(r.name, application.TargetDefinition{IncludeDeviceIDs: ids})
		in := application.RingInput{Name: r.name, TargetSetID: set.ID, SuccessThresholdPercent: r.threshold, SoakMinutes: r.soak, ApprovalRequired: r.approval, MaxTargets: r.maxTarget}
		if r.window || i > 0 {
			id := x.newID()
			x.changes[id] = application.ChangeWindow{ID: id, Reference: "CHG-" + r.name, Status: "approved", RequesterID: x.user, WindowStart: &past, WindowEnd: &future}
			in.ChangeID = &id
		} else {
			in.NoWindowRequired = true
		}
		if dep, _, err = x.svc.AddRing(ctx, x.caller(), x.planner, dep.ID, &dep.Version, in); err != nil {
			x.t.Fatalf("add ring: %v", err)
		}
	}
	dep, err = x.svc.ScheduleDeployment(ctx, x.caller(), x.planner, dep.ID, &dep.Version)
	if err != nil {
		x.t.Fatalf("schedule: %v", err)
	}
	x.dep = dep
	return dep
}

func (x *exEnv) cur() application.Deployment {
	x.t.Helper()
	d, err := x.repo().GetDeployment(context.Background(), x.dep.ID)
	if err != nil {
		x.t.Fatal(err)
	}
	return d
}

func (x *exEnv) start() application.Deployment {
	x.t.Helper()
	v := x.cur().Version
	d, err := x.svc.StartDeployment(context.Background(), x.starterCaller(), x.starter, x.dep.ID, &v)
	if err != nil {
		x.t.Fatalf("start: %v", err)
	}
	return d
}

func (x *exEnv) tick() {
	x.t.Helper()
	if err := x.svc.TickDeployment(context.Background(), x.dep.ID, x.corr); err != nil {
		x.t.Fatalf("tick: %v", err)
	}
}

func (x *exEnv) ringRun(pos int) application.DeploymentRingRun {
	x.t.Helper()
	runs, err := x.repo().RingRuns(context.Background(), x.dep.ID)
	if err != nil || pos > len(runs) {
		x.t.Fatalf("ring runs: %v (%d)", err, len(runs))
	}
	return runs[pos-1]
}

func (x *exEnv) ringID(pos int) string {
	x.t.Helper()
	rings, err := x.repo().Rings(context.Background(), x.dep.ID)
	if err != nil {
		x.t.Fatal(err)
	}
	return rings[pos-1].ID
}

func (x *exEnv) target(devExt string) string {
	x.t.Helper()
	var st string
	if err := x.pool.QueryRow(context.Background(), `SELECT t.state FROM endpoints.deployment_targets t JOIN endpoints.devices d ON d.id = t.device_id
		WHERE t.deployment_id = $1 AND d.provider = $2 AND d.external_id = $3`, x.dep.ID, x.provider, devExt).Scan(&st); err != nil {
		x.t.Fatalf("target %s: %v", devExt, err)
	}
	return st
}

func (x *exEnv) targetTime(devExt, col string) time.Time {
	x.t.Helper()
	var at *time.Time
	if err := x.pool.QueryRow(context.Background(), `SELECT t.`+col+` FROM endpoints.deployment_targets t JOIN endpoints.devices d ON d.id = t.device_id
		WHERE t.deployment_id = $1 AND d.provider = $2 AND d.external_id = $3`, x.dep.ID, x.provider, devExt).Scan(&at); err != nil || at == nil {
		x.t.Fatalf("target time %s.%s: %v", devExt, col, err)
	}
	return *at
}

func (x *exEnv) wantTargets(want map[string]string) {
	x.t.Helper()
	for ext, st := range want {
		if got := x.target(ext); got != st {
			x.t.Errorf("target %s = %s, want %s", ext, got, st)
		}
	}
}

func (x *exEnv) wantStatus(status string) {
	x.t.Helper()
	if got := x.cur(); got.Status != status {
		x.t.Fatalf("deployment status = %s (%s), want %s", got.Status, reasonOf(got.StatusReason), status)
	}
}

func (x *exEnv) wantRing(pos int, status string) {
	x.t.Helper()
	if got := x.ringRun(pos); got.Status != status {
		x.t.Fatalf("ring %d status = %s (%v), want %s", pos, got.Status, got.StatusReason, status)
	}
}

func (x *exEnv) evCount(typ string) int {
	return x.count(`SELECT count(*) FROM platform.outbox_events WHERE event_type = $1 AND payload->>'deploymentId' = $2`, typ, x.dep.ID)
}

func (x *exEnv) depAudits(action string) int {
	return x.count(`SELECT count(*) FROM platform.audit_events WHERE action = $1 AND target_id = $2`, action, x.dep.ID)
}

func (x *exEnv) promote(pos int) error {
	v := x.cur().Version
	_, err := x.svc.PromoteRing(context.Background(), x.starterCaller(), x.starter, x.dep.ID, x.ringID(pos), &v)
	return err
}

// readBack drives a ring up to the point where the assignment is written and read back.
func (x *exEnv) readBack() {
	x.t.Helper()
	x.tick() // write and request
	x.bridge()
	x.tick() // read-back
}

func reasonOf(r *string) string {
	if r == nil {
		return "<nil>"
	}
	return *r
}

func hasGate(err error, code string) bool { return gateCode(err) == code }

func TestDeploymentExecutionHappyPathTwoRings(t *testing.T) {
	x := newExEnv(t, "Happy App")
	ctx := context.Background()
	// d2 already has the version before the rollout.
	snap := application.SnapshotDevice{Record: intune.DeviceRecord{ExternalID: "d2", Name: "PC-d2", SerialNumber: "sn-" + x.suffix() + "-d2", OSPlatform: "windows", OSVersion: "10.0.22631",
		Manufacturer: "Dell Inc.", Model: "Latitude", Ownership: "corporate", ComplianceState: "compliant"},
		Software: []intune.SoftwareRecord{{Name: x.prod.Name, Version: "1.2.3"}}, SoftwareKnown: true}
	x.patch(snap)
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1", "d2"}, threshold: 100}, ringSpec{name: "wave", devs: []string{"d4"}, threshold: 100})

	x.wantStatus("scheduled")
	x.start()
	x.wantStatus("resolving_targets")
	x.tick()
	x.wantStatus("running")
	x.wantRing(1, "active")
	x.wantRing(2, "pending")
	x.wantTargets(map[string]string{"d1": "pending", "d2": "already_satisfied", "d4": "pending"})

	// The assignment is written once; targets are only requested, not assigned, until it is read back.
	x.tick()
	if x.wr.Applied() != 1 {
		t.Fatalf("writes = %d", x.wr.Applied())
	}
	x.wantTargets(map[string]string{"d1": "assignment_requested", "d4": "pending"})
	x.tick() // double tick: no second write
	if x.wr.Applied() != 1 || len(x.wr.Calls()) != 1 {
		t.Fatalf("double tick wrote again: %d/%d", x.wr.Applied(), len(x.wr.Calls()))
	}
	x.wantTargets(map[string]string{"d1": "assignment_requested"})
	op := x.wr.Calls()[0].Op
	if op.OperationID != x.dep.ID+":"+x.ringID(1)+":1" || op.TargetGroupExternalID != intune.RingGroupID(x.ringID(1)) || op.Intent != "required" || !slices.Equal(op.DeviceExternalIDs, []string{"d1"}) {
		t.Fatalf("write op %+v", op)
	}
	// Read-back by the normal ingestion moves the target to awaiting_observation.
	x.bridge()
	x.tick()
	x.wantTargets(map[string]string{"d1": "awaiting_observation"})
	rb := x.targetTime("d1", "read_back_at")

	// Evidence: applied and newer than the read-back.
	x.observe("d1", "applied", time.Time{})
	x.bridge()
	x.patch(device("d1", "PC-d1", "windows", "10.0.22631", "corporate", "compliant", "Dell Inc.", "Latitude", "sn-"+x.suffix()+"-d1"))
	x.tick()
	x.wantTargets(map[string]string{"d1": "successful"})
	if !x.targetTime("d1", "evidence_observed_at").After(rb) {
		t.Fatal("evidence not newer than read-back")
	}
	x.wantRing(1, "awaiting_promotion")
	x.wantStatus("running")

	// Promotion: the next ring's window must be open.
	chg := x.changes
	var wave application.ChangeWindow
	for id, c := range chg {
		if c.Reference == "CHG-wave" {
			past := x.clock.Add(-time.Hour)
			c.WindowEnd = &past
			chg[id] = c
			wave = c
		}
	}
	if err := x.promote(1); !hasGate(err, application.CodeWindowClosed) {
		t.Fatalf("promote with closed window: %v", err)
	}
	future := x.clock.Add(24 * time.Hour)
	wave.WindowEnd = &future
	chg[wave.ID] = wave
	if err := x.promote(1); err != nil {
		t.Fatalf("promote: %v", err)
	}
	x.wantRing(1, "promoted")
	x.wantRing(2, "active")

	x.readBack()
	x.wantTargets(map[string]string{"d4": "awaiting_observation"})
	x.observe("d4", "applied", time.Time{})
	x.bridge()
	x.tick()
	x.wantTargets(map[string]string{"d4": "successful"})
	x.wantStatus("completed")
	x.wantRing(2, "promoted")

	if x.wr.Applied() != 2 || x.count(`SELECT count(*) FROM endpoints.deployment_attempts WHERE deployment_id = $1 AND outcome_code = 'accepted'`, x.dep.ID) != 2 {
		t.Fatalf("writes %d", x.wr.Applied())
	}
	for typ, want := range map[string]int{"DeploymentStarted": 1, "RingActivated": 2, "RingPromoted": 2, "DeploymentCompleted": 1} {
		if got := x.evCount(typ); got != want {
			t.Errorf("event %s = %d, want %d", typ, got, want)
		}
	}
	if x.depAudits("endpoints.deployment.started") != 1 || x.depAudits("endpoints.deployment.completed") != 1 {
		t.Error("deployment audits missing")
	}
	// Finished Deployments leave the binding statuses, so their target sets can change again.
	if x.count(`SELECT count(*) FROM endpoints.deployment_target_transitions WHERE deployment_id = $1`, x.dep.ID) < 6 {
		t.Error("target history missing")
	}
	// The progress read carries counts, rate and gate.
	pr, err := x.svc.DeploymentProgress(ctx, x.exec, x.dep.ID)
	if err != nil || len(pr.Rings) != 2 || pr.Rings[0].Counts.ByState["already_satisfied"] != 1 || pr.Rings[0].Counts.ByState["successful"] != 1 || pr.Rings[0].NextGate != "promotion" && pr.Rings[0].Run.Status != "promoted" {
		t.Fatalf("progress: %v %+v", err, pr)
	}
	if pr.Rings[0].Rate == nil || *pr.Rings[0].Rate != 100 {
		t.Fatalf("rate: %+v", pr.Rings[0].Rate)
	}
}

func (x *exEnv) installed(ext string, installed bool) {
	x.t.Helper()
	d := intune.DeviceRecord{ExternalID: ext, Name: "PC-" + ext, SerialNumber: "sn-" + x.suffix() + "-" + ext, OSPlatform: "windows", OSVersion: "10.0.22631",
		Manufacturer: "Dell Inc.", Model: "Latitude", Ownership: "corporate", ComplianceState: "compliant"}
	sd := application.SnapshotDevice{Record: d, SoftwareKnown: true}
	if installed {
		sd.Software = []intune.SoftwareRecord{{Name: x.prod.Name, Version: "1.2.3"}}
	}
	x.patch(sd)
}

// patch ingests devices as an incomplete snapshot (nothing else is tombstoned).
func (x *exEnv) patch(devs ...application.SnapshotDevice) {
	x.t.Helper()
	if _, err := x.svc.Ingest(context.Background(), x.caller(), x.manage, application.Snapshot{Provider: x.provider, Source: application.SourceSync, Complete: false, Devices: devs}); err != nil {
		x.t.Fatalf("ingest: %v", err)
	}
}

func (x *exEnv) openConflict(devExt string) (string, bool) {
	var reason string
	err := x.pool.QueryRow(context.Background(), `SELECT f.detail->>'reason' FROM endpoints.findings f JOIN endpoints.devices d ON d.id = f.device_id
		WHERE f.kind = 'deployment_evidence_conflict' AND f.status = 'open' AND d.provider = $1 AND d.external_id = $2`, x.provider, devExt).Scan(&reason)
	return reason, err == nil
}

func TestDeploymentEvidenceRulesStaleNeverCountsAndContradictionsRaiseFindings(t *testing.T) {
	x := newExEnv(t, "Evidence App")
	x.schedule(ringSpec{name: "all", devs: []string{"d1", "d2", "d3"}, threshold: 30})
	x.start()
	x.tick()
	x.readBack()
	x.wantTargets(map[string]string{"d1": "awaiting_observation", "d2": "awaiting_observation", "d3": "awaiting_observation"})
	rb := x.targetTime("d1", "read_back_at")

	// Stale applied (older than the read-back), pending and unknown never decide.
	x.observe("d1", "applied", rb.Add(-time.Minute))
	x.observe("d2", "pending", time.Time{})
	x.observe("d3", "unknown", time.Time{})
	x.bridge()
	x.tick()
	x.wantTargets(map[string]string{"d1": "awaiting_observation", "d2": "awaiting_observation", "d3": "awaiting_observation"})

	// d3 is applied without the software in a fresh inventory: success by observation, finding raised.
	x.observe("d3", "applied", time.Time{})
	x.bridge()
	x.installed("d3", false)
	x.tick()
	x.wantTargets(map[string]string{"d3": "successful"})
	if r, ok := x.openConflict("d3"); !ok || r != "applied_without_installation" {
		t.Fatalf("conflict d3: %q %v", r, ok)
	}
	// d1 failed although the version is installed: failure by observation, finding raised.
	x.observe("d1", "failed", time.Time{})
	x.bridge()
	x.installed("d1", true)
	x.tick()
	x.wantTargets(map[string]string{"d1": "failed", "d2": "awaiting_observation"})
	if r, ok := x.openConflict("d1"); !ok || r != "failed_with_installation" {
		t.Fatalf("conflict d1: %q %v", r, ok)
	}
	// An inventory older than the observation is no evidence of a contradiction.
	x.wantRing(1, "active")

	// d2 never reports: it expires. Old evidence is not fresh, so the ring waits (stale never counts for promotion).
	x.clock = x.clock.Add(73 * time.Hour)
	x.tick()
	x.wantTargets(map[string]string{"d2": "expired"})
	x.wantRing(1, "awaiting_promotion")
	x.wantStatus("running")
	x.tick()
	x.wantStatus("running")
	// Fresh synchronization refreshes the evidence; the last ring then completes with errors.
	x.bridge()
	x.tick()
	x.wantStatus("completed_with_errors")
}

func TestDeploymentFailureThresholdHaltsRingAndPauses(t *testing.T) {
	x := newExEnv(t, "Threshold App")
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
	x.wantStatus("paused")
	if got := x.cur(); got.StatusReason == nil || *got.StatusReason != application.ReasonRingHalted {
		t.Fatalf("reason %v", got.StatusReason)
	}
	if r := x.ringRun(1); r.StatusReason == nil || *r.StatusReason != application.ReasonFailureThreshold {
		t.Fatalf("ring reason %v", r.StatusReason)
	}
	if x.evCount("RingHalted") != 1 {
		t.Fatal("RingHalted missing")
	}
	writes := x.wr.Applied()
	x.tick()
	x.tick()
	if x.wr.Applied() != writes {
		t.Fatal("engine wrote while paused")
	}
	// A halted ring is resumed explicitly; resuming the Deployment alone is refused.
	v := x.cur().Version
	ctx := context.Background()
	if _, err := x.svc.ResumeDeployment(ctx, x.starterCaller(), x.starter, x.dep.ID, &v); !hasGate(err, application.CodeRingHalted) {
		t.Fatalf("resume deployment with halted ring: %v", err)
	}
	if _, err := x.svc.ResumeRing(ctx, x.starterCaller(), x.starter, x.dep.ID, x.ringID(1), &v); err != nil {
		t.Fatalf("resume ring: %v", err)
	}
	x.wantRing(1, "active")
	x.wantStatus("running")
}

func TestDeploymentWriterFailuresAreRecordedAndRetried(t *testing.T) {
	x := newExEnv(t, "Writer App")
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100})
	x.start()
	x.tick()
	x.wr.FailNext(intune.ErrTransient)
	x.tick()
	x.wantTargets(map[string]string{"d1": "pending"})
	x.tick() // second attempt succeeds with a new operation id
	x.wantTargets(map[string]string{"d1": "assignment_requested"})
	rows, err := x.pool.Query(context.Background(), `SELECT kind, attempt, outcome_code, operation_id FROM endpoints.deployment_attempts WHERE deployment_id = $1 ORDER BY attempt`, x.dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var k, o, op string
		var n int
		if err := rows.Scan(&k, &n, &o, &op); err != nil {
			t.Fatal(err)
		}
		got = append(got, k+":"+o+":"+op[len(op)-1:])
	}
	if !slices.Equal(got, []string{"set_assignment:transient_error:1", "set_assignment:accepted:2"}) {
		t.Fatalf("attempts %v", got)
	}
	// A permanent error halts the ring and pauses the Deployment.
	y := newExEnv(t, "Writer App 2")
	y.wr.FailNext(intune.ErrPermanent)
	y.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100})
	y.start()
	y.tick()
	y.tick()
	y.wantRing(1, "halted")
	y.wantStatus("paused")
	if r := y.ringRun(1); r.StatusReason == nil || *r.StatusReason != application.ReasonAssignmentFailed {
		t.Fatalf("ring reason %v", r.StatusReason)
	}
	// The production placeholder writer fails permanently the same way.
	z := newExEnv(t, "Writer App 3")
	z.svc.WithDeployWrite(true, intune.NotConfiguredWriter{})
	z.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100})
	z.start()
	z.tick()
	z.tick()
	z.wantRing(1, "halted")
}

func TestDeploymentStartGates(t *testing.T) {
	x := newExEnv(t, "Gate App")
	ctx := context.Background()
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100})
	v := x.cur().Version
	// Capability off: every write operation is refused.
	x.svc.WithDeployWrite(false, nil)
	if _, err := x.svc.StartDeployment(ctx, x.starterCaller(), x.starter, x.dep.ID, &v); !hasGate(err, application.CodeDeployWriteDisabled) {
		t.Fatalf("flag off: %v", err)
	}
	x.svc.WithDeployWrite(true, x.wr)
	// Missing permission, missing expectedVersion, stale version.
	if _, err := x.svc.StartDeployment(ctx, x.starterCaller(), application.Principal{UserID: x.starterID}, x.dep.ID, &v); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("no permission: %v", err)
	}
	if _, err := x.svc.StartDeployment(ctx, x.starterCaller(), x.starter, x.dep.ID, nil); err == nil {
		t.Fatal("missing expectedVersion accepted")
	}
	stale := v + 5
	if _, err := x.svc.StartDeployment(ctx, x.starterCaller(), x.starter, x.dep.ID, &stale); !errors.Is(err, application.ErrVersionConflict) {
		t.Fatalf("stale: %v", err)
	}
	// Plan hash re-verified at start.
	conn, err := x.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Exec(ctx, `SET session_replication_role = replica`)
	_, err = conn.Exec(ctx, `UPDATE endpoints.deployments SET plan_sha256 = repeat('a', 64) WHERE id = $1`, x.dep.ID)
	_, _ = conn.Exec(ctx, `RESET session_replication_role`)
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.svc.StartDeployment(ctx, x.starterCaller(), x.starter, x.dep.ID, &v); !hasGate(err, application.CodePlanChanged) {
		t.Fatalf("plan hash: %v", err)
	}

	// Hash mismatch reported by the provider closes the package gate.
	y := newExEnv(t, "Gate App 2")
	y.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100})
	y.fake.ReportHash(*y.pk.ProviderPackageID, hashB)
	y.sync()
	v = y.cur().Version
	if _, err := y.svc.StartDeployment(ctx, y.starterCaller(), y.starter, y.dep.ID, &v); !hasGate(err, application.ReasonPackageGateClosed) {
		t.Fatalf("hash mismatch: %v", err)
	}

	// A revoked version.
	w := newExEnv(t, "Gate App 3")
	w.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100})
	cur, err := w.svc.GetSoftwareVersion(ctx, w.appr, w.ver.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.RevokeVersion(ctx, w.approverCaller(), w.appr, w.ver.ID, "defect", &cur.Version.Version); err != nil {
		t.Fatal(err)
	}
	v = w.cur().Version
	if _, err := w.svc.StartDeployment(ctx, w.starterCaller(), w.starter, w.dep.ID, &v); !hasGate(err, application.ReasonVersionRevoked) {
		t.Fatalf("revoked: %v", err)
	}

	// A closed Change window of the first ring (a pilot with a window).
	c := newExEnv(t, "Gate App 4")
	c.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100, window: true})
	for id, cw := range c.changes {
		past := c.clock.Add(-time.Minute)
		cw.WindowEnd = &past
		c.changes[id] = cw
	}
	v = c.cur().Version
	if _, err := c.svc.StartDeployment(ctx, c.starterCaller(), c.starter, c.dep.ID, &v); !hasGate(err, application.CodeWindowClosed) {
		t.Fatalf("window: %v", err)
	}
	if c.cur().Status != "scheduled" {
		t.Fatal("refused start changed the status")
	}

	// A package that is not published: schedule a version whose package was only packaged.
	_, nv := c.approvedProductVersion("Unpublished App", hashB)
	if _, err := c.svc.PackageVersion(ctx, c.caller(), c.pkgr, nv.ID, &nv.Version); err != nil {
		t.Fatal(err)
	}
	dep, err := c.svc.CreateDeployment(ctx, c.caller(), c.planner, application.DeploymentInput{Name: "Unpublished", SoftwareVersionID: nv.ID, Intent: application.IntentInstall})
	if err != nil {
		t.Fatal(err)
	}
	set := c.set("unpub", application.TargetDefinition{IncludeDeviceIDs: []string{c.devID("d1")}})
	if dep, _, err = c.svc.AddRing(ctx, c.caller(), c.planner, dep.ID, &dep.Version, application.RingInput{Name: "p", TargetSetID: set.ID, SuccessThresholdPercent: 100, NoWindowRequired: true}); err != nil {
		t.Fatal(err)
	}
	if dep, err = c.svc.ScheduleDeployment(ctx, c.caller(), c.planner, dep.ID, &dep.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := c.svc.StartDeployment(ctx, c.starterCaller(), c.starter, dep.ID, &dep.Version); !hasGate(err, application.ReasonPackageNotPublish) {
		t.Fatalf("unpublished: %v", err)
	}
}

func (x *exEnv) markHighImpact() {
	x.t.Helper()
	ctx := context.Background()
	conn, err := x.pool.Acquire(ctx)
	if err != nil {
		x.t.Fatal(err)
	}
	defer conn.Release()
	_, _ = conn.Exec(ctx, `SET session_replication_role = replica`)
	_, err = conn.Exec(ctx, `UPDATE endpoints.deployments SET high_impact = true, approved_at = now() WHERE id = $1`, x.dep.ID)
	_, _ = conn.Exec(ctx, `RESET session_replication_role`)
	if err != nil {
		x.t.Fatal(err)
	}
}

func TestDeploymentHighImpactSeparationAndPromotionPermission(t *testing.T) {
	x := newExEnv(t, "Sod App")
	ctx := context.Background()
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100}, ringSpec{name: "wave", devs: []string{"d2"}, threshold: 100})
	x.markHighImpact()
	v := x.cur().Version
	planner := application.Principal{UserID: x.user, DeploymentsExecute: true, DeploymentsHighImpact: true, View: true}
	if _, err := x.svc.StartDeployment(ctx, x.caller(), planner, x.dep.ID, &v); !errors.Is(err, application.ErrSeparationOfPlanning) {
		t.Fatalf("planner starts high-impact plan: %v", err)
	}
	x.start()
	x.tick()
	x.readBack()
	x.observe("d1", "applied", time.Time{})
	x.bridge()
	x.tick()
	x.wantRing(1, "awaiting_promotion")
	// Promotion of a high-impact plan needs deployments.high_impact.
	v = x.cur().Version
	noHI := application.Principal{UserID: x.starterID, DeploymentsExecute: true}
	if _, err := x.svc.PromoteRing(ctx, x.starterCaller(), noHI, x.dep.ID, x.ringID(1), &v); !errors.Is(err, application.ErrHighImpactForbidden) {
		t.Fatalf("promotion without high impact: %v", err)
	}
	if err := x.promote(1); err != nil {
		t.Fatalf("promotion: %v", err)
	}
}

func TestDeploymentKillSwitchHaltsOnRevokeAndHashMismatch(t *testing.T) {
	x := newExEnv(t, "Kill App")
	ctx := context.Background()
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100})
	x.start()
	x.tick()
	x.readBack()
	writes := len(x.wr.Calls())
	cur, err := x.svc.GetSoftwareVersion(ctx, x.appr, x.ver.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.svc.RevokeVersion(ctx, x.approverCaller(), x.appr, x.ver.ID, "defect", &cur.Version.Version); err != nil {
		t.Fatal(err)
	}
	x.tick()
	x.wantStatus("paused")
	x.wantRing(1, "halted")
	if got := x.cur(); got.StatusReason == nil || *got.StatusReason != application.ReasonVersionRevoked {
		t.Fatalf("pause reason %v", got.StatusReason)
	}
	if x.evCount("RingHalted") != 1 || len(x.wr.Calls()) != writes {
		t.Fatal("kill switch events or writes wrong")
	}
	// Evidence arriving after the kill switch does not change targets (the engine does nothing while paused).
	x.observe("d1", "applied", time.Time{})
	x.bridge()
	x.tick()
	x.wantTargets(map[string]string{"d1": "awaiting_observation"})
	v := x.cur().Version
	if _, err := x.svc.ResumeRing(ctx, x.starterCaller(), x.starter, x.dep.ID, x.ringID(1), &v); !hasGate(err, application.ReasonVersionRevoked) {
		t.Fatalf("resume after revoke: %v", err)
	}

	// A hash mismatch reported while running halts too.
	y := newExEnv(t, "Kill App 2")
	y.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100})
	y.start()
	y.tick()
	y.tick()
	y.fake.ReportHash(*y.pk.ProviderPackageID, hashB)
	y.sync()
	y.tick()
	y.wantStatus("paused")
	if got := y.cur(); got.StatusReason == nil || *got.StatusReason != application.ReasonPackageGateClosed {
		t.Fatalf("pause reason %v", got.StatusReason)
	}
}

func TestDeploymentManualOperationsAndCancelClearsAssignments(t *testing.T) {
	x := newExEnv(t, "Manual App")
	ctx := context.Background()
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1", "d2"}, threshold: 100})
	x.start()
	x.tick()
	x.readBack()
	v := x.cur().Version
	if _, err := x.svc.PauseDeployment(ctx, x.starterCaller(), x.starter, x.dep.ID, nil); err == nil {
		t.Fatal("pause without expectedVersion")
	}
	stale := v - 1
	if _, err := x.svc.PauseDeployment(ctx, x.starterCaller(), x.starter, x.dep.ID, &stale); !errors.Is(err, application.ErrVersionConflict) {
		t.Fatalf("stale pause: %v", err)
	}
	if _, err := x.svc.PauseDeployment(ctx, x.starterCaller(), x.starter, x.dep.ID, &v); err != nil {
		t.Fatal(err)
	}
	x.wantStatus("paused")
	v = x.cur().Version
	if _, err := x.svc.ResumeDeployment(ctx, x.starterCaller(), x.starter, x.dep.ID, &v); err != nil {
		t.Fatal(err)
	}
	v = x.cur().Version
	if _, err := x.svc.HaltDeployment(ctx, x.starterCaller(), x.starter, x.dep.ID, &v, "free text"); err == nil {
		t.Fatal("free-text halt reason accepted")
	}
	if _, err := x.svc.HaltDeployment(ctx, x.starterCaller(), x.starter, x.dep.ID, &v, "security_risk"); err != nil {
		t.Fatal(err)
	}
	x.wantRing(1, "halted")
	v = x.cur().Version
	if _, err := x.svc.ResumeRing(ctx, x.starterCaller(), x.starter, x.dep.ID, x.ringID(1), &v); err != nil {
		t.Fatal(err)
	}
	x.wantRing(1, "active")
	v = x.cur().Version
	if _, err := x.svc.HaltRing(ctx, x.starterCaller(), x.starter, x.dep.ID, x.ringID(1), &v, "quality_issue"); err != nil {
		t.Fatal(err)
	}
	x.wantStatus("paused")

	// Cancel: targets are cancelled, assignments cleared by the engine, idempotently.
	v = x.cur().Version
	if _, err := x.svc.CancelDeployment(ctx, x.starterCaller(), x.starter, x.dep.ID, &v, "plan_error"); err != nil {
		t.Fatal(err)
	}
	x.wantStatus("cancelled")
	x.wantTargets(map[string]string{"d1": "cancelled", "d2": "cancelled"})
	m, _ := x.prov.Management(ctx)
	if len(m.Artifacts[0].Assignments) != 1 {
		t.Fatalf("assignment before clear: %+v", m.Artifacts[0].Assignments)
	}
	x.tick()
	x.tick()
	m, _ = x.prov.Management(ctx)
	if len(m.Artifacts[0].Assignments) != 0 || len(m.Memberships) != 0 {
		t.Fatalf("not cleared: %+v", m)
	}
	clears := 0
	for _, c := range x.wr.Calls() {
		if c.Clear {
			clears++
		}
	}
	if clears != 1 || x.count(`SELECT count(*) FROM endpoints.deployment_attempts WHERE deployment_id = $1 AND kind = 'clear_assignment'`, x.dep.ID) != 1 {
		t.Fatalf("clear calls %d", clears)
	}
	// A cancelled Deployment cannot be cancelled again or resumed.
	v = x.cur().Version
	var tr *application.InvalidTransitionError
	if _, err := x.svc.CancelDeployment(ctx, x.starterCaller(), x.starter, x.dep.ID, &v, "plan_error"); !errors.As(err, &tr) {
		t.Fatalf("second cancel: %v", err)
	}
	if _, err := x.svc.ResumeDeployment(ctx, x.starterCaller(), x.starter, x.dep.ID, &v); !errors.As(err, &tr) {
		t.Fatalf("resume cancelled: %v", err)
	}
	if x.depAudits("endpoints.deployment.cancelled") != 1 {
		t.Fatal("cancel audit missing")
	}
}

func TestDeploymentReadsPermissionsAndRedaction(t *testing.T) {
	x := newExEnv(t, "Read App")
	ctx := context.Background()
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1", "d2"}, threshold: 100})
	x.start()
	x.tick()
	x.tick()
	stranger := application.Principal{UserID: x.newID()}
	if _, err := x.svc.DeploymentProgress(ctx, stranger, x.dep.ID); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("stranger progress: %v", err)
	}
	if _, err := x.svc.ListRingTargets(ctx, stranger, x.dep.ID, x.ringID(1), "", application.Page{}); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("stranger targets: %v", err)
	}
	if _, err := x.svc.ListDeploymentAttempts(ctx, stranger, x.dep.ID, application.Page{}); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("stranger attempts: %v", err)
	}
	// Another Deployment's ring id is not found under this Deployment.
	if _, err := x.svc.ListRingTargets(ctx, x.exec, x.dep.ID, x.newID(), "", application.Page{}); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("foreign ring: %v", err)
	}
	if _, err := x.svc.ListRingTargets(ctx, x.exec, x.dep.ID, x.ringID(1), "bogus", application.Page{}); err == nil {
		t.Fatal("bogus state accepted")
	}
	viewer := application.Principal{UserID: x.newID(), DeploymentsView: true}
	res, err := x.svc.ListRingTargets(ctx, viewer, x.dep.ID, x.ringID(1), "", application.Page{})
	if err != nil || len(res.Items) != 2 || !res.NamesRedacted {
		t.Fatalf("redacted: %v %+v", err, res)
	}
	for _, it := range res.Items {
		if it.DeviceName != "" {
			t.Fatal("name leaked")
		}
	}
	res, err = x.svc.ListRingTargets(ctx, x.exec, x.dep.ID, x.ringID(1), "assignment_requested", application.Page{Limit: 1})
	if err != nil || len(res.Items) != 1 || res.NamesRedacted || res.Items[0].DeviceName == "" || res.NextCursor == "" {
		t.Fatalf("names with endpoints.view: %v %+v", err, res)
	}
	att, err := x.svc.ListDeploymentAttempts(ctx, viewer, x.dep.ID, application.Page{})
	if err != nil || len(att.Items) != 1 || att.Items[0].OutcomeCode != "accepted" {
		t.Fatalf("attempts: %v %+v", err, att)
	}
	// Execution without the permission is refused for every operation.
	v := x.cur().Version
	none := application.Principal{UserID: x.starterID}
	for name, fn := range map[string]func() error{
		"pause": func() error { _, err := x.svc.PauseDeployment(ctx, x.starterCaller(), none, x.dep.ID, &v); return err },
		"halt": func() error {
			_, err := x.svc.HaltDeployment(ctx, x.starterCaller(), none, x.dep.ID, &v, "manual_halt")
			return err
		},
		"promote": func() error {
			_, err := x.svc.PromoteRing(ctx, x.starterCaller(), none, x.dep.ID, x.ringID(1), &v)
			return err
		},
		"cancel": func() error {
			_, err := x.svc.CancelDeployment(ctx, x.starterCaller(), none, x.dep.ID, &v, "plan_error")
			return err
		},
	} {
		if err := fn(); !errors.Is(err, application.ErrForbidden) {
			t.Errorf("%s without permission: %v", name, err)
		}
	}
	// A planner (deployments.manage) cannot cancel a running Deployment.
	if _, err := x.svc.CancelDeployment(ctx, x.caller(), x.planner, x.dep.ID, &v, "plan_error"); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("manage cancels running: %v", err)
	}
}

func TestDeploymentConcurrentTicksAndManualHalt(t *testing.T) {
	x := newExEnv(t, "Race App")
	ctx := context.Background()
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1", "d2"}, threshold: 100})
	x.start()
	x.tick()
	x.wr.SetLatency(300 * time.Millisecond)
	errs := make(chan error, 3)
	for i := 0; i < 2; i++ {
		go func() { errs <- x.svc.TickDeployment(ctx, x.dep.ID, x.corr) }()
	}
	time.Sleep(100 * time.Millisecond)
	v := x.cur().Version
	go func() {
		_, err := x.svc.HaltDeployment(ctx, x.starterCaller(), x.starter, x.dep.ID, &v, "manual_halt")
		errs <- err
	}()
	for i := 0; i < 3; i++ {
		if err := <-errs; err != nil && !errors.Is(err, application.ErrVersionConflict) {
			t.Fatalf("concurrent op: %v", err)
		}
	}
	if x.wr.Applied() != 1 {
		t.Fatalf("concurrent ticks wrote %d times", x.wr.Applied())
	}
	// The halt is never lost: either it ran after the tick or the stale version was refused.
	x.wr.SetLatency(0)
	if got := x.cur(); got.Status == "running" {
		v := got.Version
		if _, err := x.svc.HaltDeployment(ctx, x.starterCaller(), x.starter, x.dep.ID, &v, "manual_halt"); err != nil {
			t.Fatal(err)
		}
	}
	x.wantStatus("paused")
	x.wantRing(1, "halted")
}

func TestDeploymentResolutionFailsWhenRingCapIsExceeded(t *testing.T) {
	x := newExEnv(t, "Cap App")
	ctx := context.Background()
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1", "d2"}, threshold: 100})
	conn, err := x.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Exec(ctx, `SET session_replication_role = replica`)
	_, err = conn.Exec(ctx, `UPDATE endpoints.deployment_rings SET max_targets = 1 WHERE deployment_id = $1`, x.dep.ID)
	_, _ = conn.Exec(ctx, `RESET session_replication_role`)
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	// The plan hash covers the cap, so the start refuses first; resolve directly to prove the engine re-checks.
	v := x.cur().Version
	if _, err := x.svc.StartDeployment(ctx, x.starterCaller(), x.starter, x.dep.ID, &v); !hasGate(err, application.CodePlanChanged) {
		t.Fatalf("start with changed cap: %v", err)
	}
	conn, _ = x.pool.Acquire(ctx)
	_, _ = conn.Exec(ctx, `SET session_replication_role = replica`)
	_, err = conn.Exec(ctx, `UPDATE endpoints.deployments SET status = 'resolving_targets', started_by = $2::uuid, started_at = now() WHERE id = $1`, x.dep.ID, x.starterID)
	_, _ = conn.Exec(ctx, `RESET session_replication_role`)
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	x.tick()
	x.wantStatus("failed")
	if got := x.cur(); got.StatusReason == nil || *got.StatusReason != application.ReasonTooManyTargets || got.FinishedAt == nil {
		t.Fatalf("failed: %+v", got)
	}
	if x.evCount("DeploymentFailed") != 1 {
		t.Fatal("DeploymentFailed missing")
	}
}

func (x *exEnv) ringApproval(subjectType, subjectID, approvalID, decision, by string) error {
	status := "approved"
	if decision == "reject" {
		status = "rejected"
	}
	x.approvals.setDecided(approvalID, status, by)
	payload, _ := json.Marshal(map[string]string{"approvalId": approvalID, "subjectType": subjectType, "subjectId": subjectID, "decision": decision})
	return x.repo().InTx(context.Background(), func(tx pgx.Tx) error {
		return x.svc.OnApprovalDecided(context.Background(), tx, events.OutboxEvent{EventType: "ApprovalDecided", CorrelationID: x.corr, Payload: payload})
	})
}

func TestDeploymentRingPromotionApproval(t *testing.T) {
	x := newExEnv(t, "Approval App")
	ctx := context.Background()
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100, approval: true}, ringSpec{name: "wave", devs: []string{"d2"}, threshold: 100})
	x.start()
	x.tick()
	x.readBack()
	x.observe("d1", "applied", time.Time{})
	x.bridge()
	x.tick()
	x.wantRing(1, "awaiting_promotion")
	if err := x.promote(1); !hasGate(err, application.CodePromotionApproval) {
		t.Fatalf("promote without approval: %v", err)
	}
	run := x.ringRun(1)
	v := x.cur().Version
	approver := application.Approver{UserID: &x.approver}
	// The starter and the planner cannot be the approver.
	if _, err := x.svc.RequestRingApproval(ctx, x.starterCaller(), x.starter, x.dep.ID, x.ringID(1), &v, application.Approver{UserID: &x.starterID}); !errors.Is(err, application.ErrNoEligibleApprover) {
		t.Fatalf("starter as approver: %v", err)
	}
	if _, err := x.svc.RequestRingApproval(ctx, x.starterCaller(), x.starter, x.dep.ID, x.ringID(1), &v, approver); err != nil {
		t.Fatalf("request approval: %v", err)
	}
	run = x.ringRun(1)
	if run.PromotionApprovalID == nil || *run.PromotionApprovalStatus != "pending" {
		t.Fatalf("approval binding %+v", run)
	}
	// A decision by an excluded person (the starter) is refused and does not approve.
	if err := x.ringApproval("deployment_ring", run.ID, *run.PromotionApprovalID, "approve", x.starterID); err != nil {
		t.Fatal(err)
	}
	if got := x.ringRun(1); *got.PromotionApprovalStatus != "rejected" {
		t.Fatalf("excluded decider approved: %+v", got)
	}
	if err := x.promote(1); !hasGate(err, application.CodePromotionApproval) {
		t.Fatalf("promote after refused approval: %v", err)
	}
	// A second request, approved by the named approver, opens the gate.
	v = x.cur().Version
	if _, err := x.svc.RequestRingApproval(ctx, x.starterCaller(), x.starter, x.dep.ID, x.ringID(1), &v, approver); err != nil {
		t.Fatalf("second request: %v", err)
	}
	run = x.ringRun(1)
	if err := x.ringApproval("deployment_ring", run.ID, *run.PromotionApprovalID, "approve", x.approver); err != nil {
		t.Fatal(err)
	}
	// Replaying the event changes nothing.
	if err := x.ringApproval("deployment_ring", run.ID, *run.PromotionApprovalID, "approve", x.approver); err != nil {
		t.Fatal(err)
	}
	if got := x.ringRun(1); *got.PromotionApprovalStatus != "approved" {
		t.Fatalf("approval not applied: %+v", got)
	}
	if err := x.promote(1); err != nil {
		t.Fatalf("promote with approval: %v", err)
	}
	x.wantRing(2, "active")
}

func TestDeploymentExecutionTablesEnforceInvariants(t *testing.T) {
	x := newExEnv(t, "Invariant Exec App")
	ctx := context.Background()
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100})
	x.start()
	x.tick()
	x.tick()
	run := x.ringRun(1)
	refused := []struct{ name, sql string }{
		{"scheduled-like jump", `UPDATE endpoints.deployments SET status = 'scheduled', status_reason = NULL WHERE id = $1`},
		{"running without start", `UPDATE endpoints.deployments SET started_at = NULL, started_by = NULL WHERE id = $1`},
		{"paused without reason", `UPDATE endpoints.deployments SET status = 'paused' WHERE id = $1`},
		{"completed from running without finish", `UPDATE endpoints.deployments SET status = 'completed' WHERE id = $1`},
		{"started rewritten", `UPDATE endpoints.deployments SET started_at = now() - interval '1 day' WHERE id = $1`},
		{"ring pending to promoted", `UPDATE endpoints.deployment_ring_runs SET status = 'promoted', promoted_at = now() WHERE id = '` + run.ID + `' AND $1::text IS NOT NULL`},
		{"ring rebound", `UPDATE endpoints.deployment_ring_runs SET position = 2 WHERE id = '` + run.ID + `' AND $1::text IS NOT NULL`},
		{"ring run delete", `DELETE FROM endpoints.deployment_ring_runs WHERE deployment_id = $1::uuid`},
		{"target jump", `UPDATE endpoints.deployment_targets SET state = 'successful', read_back_at = now(), decided_at = now(), evidence_observed_at = now() WHERE deployment_id = $1`},
		{"target without request time", `UPDATE endpoints.deployment_targets SET state = 'awaiting_observation' WHERE deployment_id = $1`},
		{"target delete", `DELETE FROM endpoints.deployment_targets WHERE deployment_id = $1`},
		{"target history update", `UPDATE endpoints.deployment_target_transitions SET reason = 'other' WHERE deployment_id = $1`},
		{"ring history delete", `DELETE FROM endpoints.deployment_ring_transitions WHERE deployment_id = $1`},
		{"attempt update", `UPDATE endpoints.deployment_attempts SET outcome_code = 'permanent_error' WHERE deployment_id = $1`},
		{"attempt delete", `DELETE FROM endpoints.deployment_attempts WHERE deployment_id = $1`},
		{"attempt duplicate operation", `INSERT INTO endpoints.deployment_attempts (deployment_id, ring_run_id, kind, attempt, operation_id, requested_at, outcome_code, correlation_id)
			SELECT deployment_id, ring_run_id, kind, attempt + 10, operation_id, requested_at, outcome_code, correlation_id FROM endpoints.deployment_attempts WHERE deployment_id = $1`},
		{"attempt bad outcome", `INSERT INTO endpoints.deployment_attempts (deployment_id, ring_run_id, kind, attempt, operation_id, requested_at, outcome_code, correlation_id)
			SELECT deployment_id, ring_run_id, kind, 99, 'x', requested_at, 'maybe', 'c' FROM endpoints.deployment_attempts WHERE deployment_id = $1`},
	}
	for _, c := range refused {
		if _, err := x.pool.Exec(ctx, c.sql, x.dep.ID); err == nil {
			t.Errorf("%s accepted", c.name)
		}
	}
	for _, tbl := range []string{"deployment_attempts", "deployment_ring_transitions", "deployment_target_transitions", "deployment_targets", "deployment_ring_runs"} {
		if _, err := x.pool.Exec(ctx, `TRUNCATE endpoints.`+tbl); err == nil {
			t.Errorf("truncate %s accepted", tbl)
		}
	}
	// Target sets of a running Deployment cannot change or be archived.
	rings, _ := x.repo().Rings(ctx, x.dep.ID)
	set, err := x.svc.GetTargetSet(ctx, x.planner, rings[0].TargetSetID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.svc.ArchiveTargetSet(ctx, x.caller(), x.planner, set.ID, &set.Version); !errors.Is(err, application.ErrTargetSetInUse) {
		t.Fatalf("archive in-use set: %v", err)
	}
}

func TestDeployWriteCapabilityIsAuditedOnce(t *testing.T) {
	x := newExEnv(t, "Cap Audit App")
	ctx := context.Background()
	before := x.count(`SELECT count(*) FROM platform.audit_events WHERE action = 'endpoints.deploy_write.enabled'`)
	for i := 0; i < 3; i++ {
		if err := x.svc.AuditDeployWrite(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := x.count(`SELECT count(*) FROM platform.audit_events WHERE action = 'endpoints.deploy_write.enabled'`); got != before+1 {
		t.Fatalf("audit records %d -> %d", before, got)
	}
	var actor *string
	var meta string
	if err := x.pool.QueryRow(ctx, `SELECT actor_id::text, metadata::text FROM platform.audit_events WHERE action = 'endpoints.deploy_write.enabled' ORDER BY id DESC LIMIT 1`).Scan(&actor, &meta); err != nil || actor != nil {
		t.Fatalf("actor %v %v", actor, err)
	}
}
