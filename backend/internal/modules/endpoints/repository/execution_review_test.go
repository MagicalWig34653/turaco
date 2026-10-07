package repository_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
)

// Tests for the G3 review findings: write protocol, kill switch, evidence rules, guards (docs/product/f9-software-lifecycle-design.md,
// "G3 review outcomes").

// hookWriter wraps the FakeWriter: onSet runs inside the provider call, clearErr fails clears of a ring.
type hookWriter struct {
	inner    *intune.FakeWriter
	onSet    func()
	clearErr func(ringKey string) error
}

func (h *hookWriter) SetRingAssignment(ctx context.Context, op intune.RingAssignmentOp) error {
	if h.onSet != nil {
		h.onSet()
	}
	return h.inner.SetRingAssignment(ctx, op)
}

func (h *hookWriter) ClearRingAssignment(ctx context.Context, opID, artifact, ringKey string) error {
	if h.clearErr != nil {
		if err := h.clearErr(ringKey); err != nil {
			return err
		}
	}
	return h.inner.ClearRingAssignment(ctx, opID, artifact, ringKey)
}

// force runs a statement with the triggers disabled (test setup of states the engine cannot reach).
func (x *exEnv) force(sql string, args ...any) {
	x.t.Helper()
	ctx := context.Background()
	conn, err := x.pool.Acquire(ctx)
	if err != nil {
		x.t.Fatal(err)
	}
	defer conn.Release()
	_, _ = conn.Exec(ctx, `SET session_replication_role = replica`)
	_, err = conn.Exec(ctx, sql, args...)
	_, _ = conn.Exec(ctx, `RESET session_replication_role`)
	if err != nil {
		x.t.Fatal(err)
	}
}

func (x *exEnv) sql(sql string, args ...any) {
	x.t.Helper()
	if _, err := x.pool.Exec(context.Background(), sql, args...); err != nil {
		x.t.Fatal(err)
	}
}

func (x *exEnv) revoke() {
	x.t.Helper()
	ctx := context.Background()
	cur, err := x.svc.GetSoftwareVersion(ctx, x.appr, x.ver.ID)
	if err != nil {
		x.t.Fatal(err)
	}
	if _, err := x.svc.RevokeVersion(ctx, x.approverCaller(), x.appr, x.ver.ID, "defect", &cur.Version.Version); err != nil {
		x.t.Fatal(err)
	}
}

func (x *exEnv) cancel() {
	x.t.Helper()
	v := x.cur().Version
	if _, err := x.svc.CancelDeployment(context.Background(), x.starterCaller(), x.starter, x.dep.ID, &v, "plan_error"); err != nil {
		x.t.Fatal(err)
	}
}

func (x *exEnv) attempts(kind string) []string {
	x.t.Helper()
	rows, err := x.pool.Query(context.Background(), `SELECT outcome_code FROM endpoints.deployment_attempts WHERE deployment_id = $1 AND kind = $2 ORDER BY attempt`, x.dep.ID, kind)
	if err != nil {
		x.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var o string
		if err := rows.Scan(&o); err != nil {
			x.t.Fatal(err)
		}
		out = append(out, o)
	}
	return out
}

func (x *exEnv) assignments() int {
	m, _ := x.prov.Management(context.Background())
	n := 0
	for _, a := range m.Artifacts {
		n += len(a.Assignments)
	}
	return n
}

func (x *exEnv) stateReason(devExt string) string {
	x.t.Helper()
	var r *string
	if err := x.pool.QueryRow(context.Background(), `SELECT t.state_reason FROM endpoints.deployment_targets t JOIN endpoints.devices d ON d.id = t.device_id
		WHERE t.deployment_id = $1 AND d.provider = $2 AND d.external_id = $3`, x.dep.ID, x.provider, devExt).Scan(&r); err != nil {
		x.t.Fatal(err)
	}
	return reasonOf(r)
}

func TestReviewCancelDuringInFlightWriteStillClears(t *testing.T) {
	x := newExEnv(t, "Inflight App")
	hw := &hookWriter{inner: x.wr}
	x.svc.WithDeployWrite(true, hw)
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100})
	x.start()
	x.tick()
	hw.onSet = func() { x.cancel() } // the person cancels while the provider call is running
	x.tick()
	hw.onSet = nil
	x.wantStatus("cancelled")
	if got := x.attempts("set_assignment"); !slices.Equal(got, []string{"accepted"}) {
		t.Fatalf("set attempts %v", got)
	}
	if r := x.ringRun(1); r.ClearRequestedAt == nil || r.AssignmentClearedAt != nil {
		t.Fatalf("clearing not queued: %+v", r)
	}
	if x.assignments() != 1 {
		t.Fatal("provider assignment missing before the clear")
	}
	x.tick()
	if x.assignments() != 0 || x.ringRun(1).AssignmentClearedAt == nil {
		t.Fatalf("assignment survived the cancel: %d", x.assignments())
	}
}

func TestReviewCrashedInFlightAttemptIsInterruptedAndCleared(t *testing.T) {
	x := newExEnv(t, "Crash App")
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100})
	x.start()
	x.tick()
	run := x.ringRun(1)
	// A worker died between the decision to write and the end of the call: pin and in_flight row are committed, nothing else.
	x.sql(`UPDATE endpoints.deployment_ring_runs SET management_artifact_id = (SELECT management_artifact_id FROM endpoints.software_packages WHERE id = $2),
		management_artifact_external_id = $3 WHERE id = $1`, run.ID, x.pk.ID, x.artifact)
	x.sql(`INSERT INTO endpoints.deployment_attempts (deployment_id, ring_run_id, kind, attempt, operation_id, requested_at, outcome_code, correlation_id)
		VALUES ($1, $2, 'set_assignment', 1, $3, now(), 'in_flight', 'crash')`, x.dep.ID, run.ID, "crash-"+run.ID)
	x.cancel()
	if r := x.ringRun(1); r.ClearRequestedAt == nil {
		t.Fatal("an in_flight set attempt did not queue the clearing")
	}
	x.tick()
	if got := x.attempts("set_assignment"); !slices.Equal(got, []string{"interrupted"}) {
		t.Fatalf("set attempts %v", got)
	}
	if got := x.attempts("clear_assignment"); !slices.Equal(got, []string{"accepted"}) {
		t.Fatalf("clear attempts %v", got)
	}
	if x.depAudits("endpoints.deployment.attempt_finished") != 2 {
		t.Fatalf("attempt audits %d", x.depAudits("endpoints.deployment.attempt_finished"))
	}
}

func TestReviewResolutionRefusesAPlanThatDrifted(t *testing.T) {
	x := newExEnv(t, "Drift App")
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1", "d2"}, threshold: 100})
	if x.cur().HighImpact || x.cur().ScheduledTargets == nil || *x.cur().ScheduledTargets != 2 {
		t.Fatalf("scheduled plan %+v", x.cur())
	}
	x.start()
	x.svc.WithHighImpactThresholds(2, 0) // the fleet rules changed after scheduling
	x.tick()
	x.wantStatus("failed")
	if got := x.cur(); got.StatusReason == nil || *got.StatusReason != application.ReasonBecameHighImpact {
		t.Fatalf("reason %v", got.StatusReason)
	}

	y := newExEnv(t, "Growth App")
	y.schedule(ringSpec{name: "pilot", devs: []string{"d1", "d2"}, threshold: 100})
	y.force(`UPDATE endpoints.deployments SET scheduled_targets = 1 WHERE id = $1`, y.dep.ID) // 2 targets now, 1 at scheduling: +100%
	y.start()
	y.tick()
	y.wantStatus("failed")
	if got := y.cur(); got.StatusReason == nil || *got.StatusReason != application.ReasonTargetsGrew {
		t.Fatalf("reason %v", got.StatusReason)
	}
	// Within 25% the plan runs: 2 targets against 2 evaluated at scheduling.
	z := newExEnv(t, "Steady App")
	z.schedule(ringSpec{name: "pilot", devs: []string{"d1", "d2"}, threshold: 100})
	z.start()
	z.tick()
	z.wantStatus("running")
}

func TestReviewEngineWorkIsRoundRobinAndTheKillSwitchSweepIsNotCapped(t *testing.T) {
	ctx := context.Background()
	x := newExEnv(t, "Round Robin A")
	y := newExEnv(t, "Round Robin B")
	for _, e := range []*exEnv{x, y} {
		e.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100})
		e.start()
	}
	y.tick()
	x.tick() // x was worked on last
	ids, err := x.repo().EngineWork(ctx, 100000)
	if err != nil {
		t.Fatal(err)
	}
	if ix, iy := slices.Index(ids, x.dep.ID), slices.Index(ids, y.dep.ID); ix < 0 || iy < 0 || iy > ix {
		t.Fatalf("work order: x at %d, y at %d (the Deployment ticked longest ago comes first)", ix, iy)
	}
	// Both versions are revoked; one sweep pauses both without any capped tick.
	x.revoke()
	y.revoke()
	if err := x.svc.GateSweep(ctx, x.corr); err != nil {
		t.Fatal(err)
	}
	for _, e := range []*exEnv{x, y} {
		e.wantStatus("paused")
		if got := e.cur(); got.StatusReason == nil || *got.StatusReason != application.ReasonVersionRevoked {
			t.Fatalf("pause reason %v", got.StatusReason)
		}
	}
}

func TestReviewKillSwitchWithoutWriteCapabilityKeepsClearPendingVisible(t *testing.T) {
	x := newExEnv(t, "Clear Pending App")
	ctx := context.Background()
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100})
	x.start()
	x.tick()
	x.readBack()
	x.svc.WithDeployWrite(false, nil)
	x.revoke()
	if err := x.svc.GateSweep(ctx, x.corr); err != nil { // pausing, halting and queueing need no capability
		t.Fatal(err)
	}
	x.wantStatus("paused")
	x.wantRing(1, "halted")
	calls := len(x.wr.Calls())
	x.tick()
	pr, err := x.svc.DeploymentProgress(ctx, x.reader, x.dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(x.wr.Calls()) != calls || !pr.ClearPending || !pr.Rings[0].ClearPending || x.assignments() != 1 {
		t.Fatalf("without the capability nothing is written: calls %d->%d progress %+v", calls, len(x.wr.Calls()), pr.Rings[0])
	}
	x.svc.WithDeployWrite(true, nil)
	x.tick()
	pr, err = x.svc.DeploymentProgress(ctx, x.reader, x.dep.ID)
	if err != nil || pr.ClearPending || x.assignments() != 0 {
		t.Fatalf("not cleared with the capability: %v %+v", err, pr)
	}
}

func TestReviewRelinkedArtifactClosesTheGate(t *testing.T) {
	x := newExEnv(t, "Pin App")
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100})
	x.start()
	x.tick()
	x.readBack()
	run := x.ringRun(1)
	if run.ManagementArtifactID == nil || run.ManagementArtifactExternalID == nil || *run.ManagementArtifactExternalID != x.artifact {
		t.Fatalf("artifact not pinned at the first write: %+v", run)
	}
	x.force(`UPDATE endpoints.deployment_ring_runs SET management_artifact_external_id = 'relinked' WHERE id = $1`, run.ID)
	if err := x.svc.GateSweep(context.Background(), x.corr); err != nil {
		t.Fatal(err)
	}
	x.wantStatus("paused")
	if got := x.cur(); got.StatusReason == nil || *got.StatusReason != application.ReasonArtifactChanged {
		t.Fatalf("reason %v", got.StatusReason)
	}
}

func TestReviewClearingCoversEveryRingAndGivesUpWithAFinding(t *testing.T) {
	x := newExEnv(t, "Clear All App")
	ctx := context.Background()
	hw := &hookWriter{inner: x.wr}
	x.svc.WithDeployWrite(true, hw)
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100}, ringSpec{name: "wave", devs: []string{"d2"}, threshold: 100})
	x.start()
	x.tick()
	x.tick() // ring 1 written
	// Ring 2 was written too (setup of a state the engine reaches after a promotion).
	r2 := x.ringRun(2)
	x.sql(`UPDATE endpoints.deployment_ring_runs SET management_artifact_id = (SELECT management_artifact_id FROM endpoints.software_packages WHERE id = $2),
		management_artifact_external_id = $3 WHERE id = $1`, r2.ID, x.pk.ID, x.artifact)
	x.sql(`INSERT INTO endpoints.deployment_attempts (deployment_id, ring_run_id, kind, attempt, operation_id, requested_at, outcome_code, finished_at, correlation_id)
		VALUES ($1, $2, 'set_assignment', 1, $3, now(), 'accepted', now(), 'setup')`, x.dep.ID, r2.ID, "setup-"+r2.ID)
	ring1 := x.ringID(1)
	hw.clearErr = func(ringKey string) error {
		if ringKey == ring1 {
			return intune.ErrPermanent
		}
		return nil
	}
	x.cancel()
	x.tick() // ring 1 fails permanently, ring 2 is still cleared in the same tick
	if x.ringRun(2).AssignmentClearedAt == nil || x.ringRun(1).AssignmentClearedAt != nil {
		t.Fatalf("one failing ring blocked the other: %+v %+v", x.ringRun(1), x.ringRun(2))
	}
	for i := 0; i < 12; i++ {
		x.tick()
	}
	if got := x.count(`SELECT count(*) FROM endpoints.deployment_attempts WHERE ring_run_id = $1 AND kind = 'clear_assignment'`, x.ringRun(1).ID); got != application.MaxClearAttempts {
		t.Fatalf("clear attempts = %d, want %d", got, application.MaxClearAttempts)
	}
	if x.count(`SELECT count(*) FROM endpoints.findings WHERE kind = 'deployment_clear_failed' AND status = 'open' AND detail->>'deploymentId' = $1`, x.dep.ID) != 1 ||
		x.depAudits("endpoints.deployment.clear_failed") != 1 {
		t.Fatal("deployment_clear_failed finding or its audit missing")
	}
	// Every failed attempt is audited (codes only).
	if got := x.count(`SELECT count(*) FROM platform.audit_events WHERE action = 'endpoints.deployment.attempt_finished' AND target_id = $1 AND metadata->>'outcome' = 'permanent_error'`, x.dep.ID); got != application.MaxClearAttempts {
		t.Fatalf("failed attempt audits = %d", got)
	}
	pr, err := x.svc.DeploymentProgress(ctx, x.reader, x.dep.ID)
	if err != nil || !pr.Rings[0].ClearFailed || !pr.ClearPending {
		t.Fatalf("progress %v %+v", err, pr.Rings[0])
	}
}

func TestReviewNoEvidenceNeverSettlesAndHaltsAfterTheEvidenceWindow(t *testing.T) {
	x := newExEnv(t, "No Evidence App")
	for _, e := range []string{"d1", "d2"} {
		x.patch(application.SnapshotDevice{Record: intune.DeviceRecord{ExternalID: e, Name: "PC-" + e, SerialNumber: "sn-" + x.suffix() + "-" + e, OSPlatform: "windows", OSVersion: "10.0.22631",
			Manufacturer: "Dell Inc.", Model: "Latitude", Ownership: "corporate", ComplianceState: "compliant"},
			Software: []intune.SoftwareRecord{{Name: x.prod.Name, Version: "1.2.3"}}, SoftwareKnown: true})
	}
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1", "d2"}, threshold: 100})
	x.start()
	x.tick()
	x.wantTargets(map[string]string{"d1": "already_satisfied", "d2": "already_satisfied"})
	x.tick()
	x.wantRing(1, "active")
	if x.ringRun(1).SettledAt != nil {
		t.Fatal("a ring without a denominator settled")
	}
	x.clock = x.clock.Add(100 * time.Hour) // the service clock of the tests is fake: the evidence window (72h) ran out
	x.tick()
	x.wantRing(1, "halted")
	if r := x.ringRun(1); r.StatusReason == nil || *r.StatusReason != application.ReasonNoEvidence {
		t.Fatalf("reason %v", r.StatusReason)
	}
	x.wantStatus("paused")
}

func TestReviewProviderNotApplicableRatioHaltsTheRing(t *testing.T) {
	x := newExEnv(t, "NA Ratio App")
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1", "d2", "d3"}, threshold: 100})
	x.start()
	x.tick()
	x.readBack()
	x.observe("d1", "not_applicable", time.Time{})
	x.observe("d2", "not_applicable", time.Time{})
	x.bridge()
	x.tick()
	x.wantTargets(map[string]string{"d1": "not_applicable", "d2": "not_applicable"})
	x.wantRing(1, "halted")
	if r := x.ringRun(1); r.StatusReason == nil || *r.StatusReason != application.ReasonNARatio {
		t.Fatalf("reason %v", r.StatusReason)
	}
}

func TestReviewAssignmentRequestedExpiresWhenTheReadBackNeverComes(t *testing.T) {
	x := newExEnv(t, "Readback Missing App")
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1", "d2", "d3"}, threshold: 100})
	x.start()
	x.tick()
	x.tick()
	x.wantTargets(map[string]string{"d1": "assignment_requested"})
	x.clock = x.clock.Add(100 * time.Hour)
	x.tick() // no synchronization read the assignment back
	x.wantTargets(map[string]string{"d1": "expired", "d2": "expired", "d3": "expired"})
	if got := x.stateReason("d1"); got != application.ReasonReadBackMissing {
		t.Fatalf("reason %s", got)
	}
	x.wantRing(1, "halted") // expired targets count as failures
}

func TestReviewAlreadySatisfiedNeedsFreshInventory(t *testing.T) {
	x := newExEnv(t, "Fresh Inventory App")
	x.patch(application.SnapshotDevice{Record: intune.DeviceRecord{ExternalID: "d1", Name: "PC-d1", SerialNumber: "sn-" + x.suffix() + "-d1", OSPlatform: "windows", OSVersion: "10.0.22631",
		Manufacturer: "Dell Inc.", Model: "Latitude", Ownership: "corporate", ComplianceState: "compliant"},
		Software: []intune.SoftwareRecord{{Name: x.prod.Name, Version: "1.2.3"}}, SoftwareKnown: true})
	x.sql(`UPDATE endpoints.devices SET last_synced_at = now() - interval '3 days' WHERE provider = $1 AND external_id = 'd1'`, x.provider)
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1", "d2"}, threshold: 100})
	x.start()
	x.tick()
	x.wantTargets(map[string]string{"d1": "pending", "d2": "pending"}) // stale inventory proves nothing
	x.sql(`UPDATE endpoints.devices SET last_synced_at = now() WHERE provider = $1 AND external_id = 'd1'`, x.provider)
	x.tick() // re-checked before the write
	x.wantTargets(map[string]string{"d1": "already_satisfied", "d2": "assignment_requested"})
	if op := x.wr.Calls()[0].Op; !slices.Equal(op.DeviceExternalIDs, []string{"d2"}) {
		t.Fatalf("write devices %v", op.DeviceExternalIDs)
	}
}

func TestReviewChangeWindowIsEnforcedWhenWriting(t *testing.T) {
	x := newExEnv(t, "Window App")
	ctx := context.Background()
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100, window: true})
	x.start()
	rings, _ := x.repo().Rings(ctx, x.dep.ID)
	id := *rings[0].ChangeID
	open := x.changes[id]
	ended := x.clock.Add(-time.Hour)
	x.changes[id] = application.ChangeWindow{ID: id, Reference: open.Reference, Status: "approved", RequesterID: open.RequesterID, WindowStart: &ended, WindowEnd: &ended}
	x.tick() // resolution
	x.tick() // the window closed before the first write
	x.wantStatus("paused")
	if got := x.cur(); got.StatusReason == nil || *got.StatusReason != application.ReasonWindowClosed || len(x.wr.Calls()) != 0 {
		t.Fatalf("reason %v calls %d", got.StatusReason, len(x.wr.Calls()))
	}
	v := x.cur().Version
	if _, err := x.svc.ResumeDeployment(ctx, x.starterCaller(), x.starter, x.dep.ID, &v); !hasGate(err, application.CodeWindowClosed) {
		t.Fatalf("resume with a closed window: %v", err)
	}
	x.changes[id] = open
	if _, err := x.svc.ResumeDeployment(ctx, x.starterCaller(), x.starter, x.dep.ID, &v); err != nil {
		t.Fatalf("resume with an open window: %v", err)
	}
	x.tick()
	x.wantTargets(map[string]string{"d1": "assignment_requested"})
}

func TestReviewHaltWithdrawsThePromotionApproval(t *testing.T) {
	x := newExEnv(t, "Withdraw App")
	ctx := context.Background()
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100, approval: true}, ringSpec{name: "wave", devs: []string{"d2"}, threshold: 100})
	x.start()
	x.tick()
	x.readBack()
	x.observe("d1", "applied", time.Time{})
	x.bridge()
	x.tick()
	x.wantRing(1, "awaiting_promotion")
	v := x.cur().Version
	if _, err := x.svc.RequestRingApproval(ctx, x.starterCaller(), x.starter, x.dep.ID, x.ringID(1), &v, application.Approver{UserID: &x.approver}); err != nil {
		t.Fatal(err)
	}
	run := x.ringRun(1)
	aid := *run.PromotionApprovalID
	v = x.cur().Version
	if _, err := x.svc.HaltRing(ctx, x.starterCaller(), x.starter, x.dep.ID, x.ringID(1), &v, "quality_issue"); err != nil {
		t.Fatal(err)
	}
	got := x.ringRun(1)
	if got.PromotionApprovalID != nil || got.PromotionApprovalStatus != nil || got.PromotionApprovalRequestedBy != nil {
		t.Fatalf("approval binding survived the halt: %+v", got)
	}
	list, _ := x.approvals.RingForSubject(ctx, run.ID)
	if len(list) != 1 || list[0].ID != aid || list[0].Status != "cancelled" {
		t.Fatalf("approval not cancelled: %+v", list)
	}
}

func TestReviewSeparationOfPlanningAlsoCoversPromoteAndResumeRing(t *testing.T) {
	x := newExEnv(t, "Sod2 App")
	ctx := context.Background()
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100}, ringSpec{name: "wave", devs: []string{"d2"}, threshold: 100})
	x.markHighImpact()
	x.start()
	x.tick()
	x.readBack()
	x.observe("d1", "applied", time.Time{})
	x.bridge()
	x.tick()
	x.wantRing(1, "awaiting_promotion")
	planner := application.Principal{UserID: x.user, DeploymentsExecute: true, DeploymentsHighImpact: true, View: true}
	v := x.cur().Version
	if _, err := x.svc.PromoteRing(ctx, x.caller(), planner, x.dep.ID, x.ringID(1), &v); !errors.Is(err, application.ErrSeparationOfPlanning) {
		t.Fatalf("planner promotes: %v", err)
	}
	if _, err := x.svc.HaltRing(ctx, x.starterCaller(), x.starter, x.dep.ID, x.ringID(1), &v, "quality_issue"); err != nil {
		t.Fatal(err)
	}
	v = x.cur().Version
	if _, err := x.svc.ResumeRing(ctx, x.caller(), planner, x.dep.ID, x.ringID(1), &v, ""); !errors.Is(err, application.ErrSeparationOfPlanning) {
		t.Fatalf("planner resumes: %v", err)
	}
}

func TestReviewEvidenceNeedsAProviderTimestampOrAStateChangeAfterTheReadBack(t *testing.T) {
	x := newExEnv(t, "Timestamp App")
	// The artifact already reports applied for d1 before the rollout (an older installation of the same artifact).
	x.observe("d1", "applied", time.Time{})
	x.bridge()
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100})
	x.start()
	x.tick()
	x.readBack()
	x.wantTargets(map[string]string{"d1": "awaiting_observation"})
	// A later synchronization without a provider timestamp restamps the unchanged state with the sync time; that is not evidence.
	x.bridge()
	x.tick()
	x.wantTargets(map[string]string{"d1": "awaiting_observation"})
	if x.count(`SELECT count(*) FROM endpoints.management_observations WHERE observed_at_provider AND device_id = $1::uuid`, x.devID("d1")) != 0 {
		t.Fatal("a missing provider timestamp was recorded as one")
	}
	// The provider's own timestamp, newer than the read-back, is.
	x.observe("d1", "applied", x.clock.Add(500*time.Millisecond)) // after the read-back, before the next synchronization
	x.bridge()
	if x.count(`SELECT count(*) FROM endpoints.management_observations WHERE observed_at_provider AND device_id = $1::uuid`, x.devID("d1")) != 1 {
		t.Fatal("provider timestamp not recorded")
	}
	x.tick()
	x.wantTargets(map[string]string{"d1": "successful"})

	// A state change after the read-back is evidence without a provider timestamp.
	y := newExEnv(t, "Timestamp App 2")
	y.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100})
	y.start()
	y.tick()
	y.readBack()
	y.observe("d1", "applied", time.Time{})
	y.bridge()
	y.tick()
	y.wantTargets(map[string]string{"d1": "successful"})
}

func TestReviewRetryResetsTheAttemptCounterAtMostThreeTimes(t *testing.T) {
	x := newExEnv(t, "Retry App")
	ctx := context.Background()
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100})
	x.start()
	x.tick()
	resume := func(reason string) error {
		v := x.cur().Version
		_, err := x.svc.ResumeRing(ctx, x.starterCaller(), x.starter, x.dep.ID, x.ringID(1), &v, reason)
		return err
	}
	for i := 1; i <= application.MaxRingRetries; i++ {
		x.wr.FailNext(intune.ErrPermanent)
		x.tick()
		x.wantRing(1, "halted")
		if err := resume(""); !hasGate(err, application.CodeRetryRequired) {
			t.Fatalf("resume without retry: %v", err)
		}
		if err := resume("bogus"); err == nil {
			t.Fatal("unknown resume reason accepted")
		}
		if err := resume(application.ReasonRetry); err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
		if r := x.ringRun(1); r.RetryCount != i {
			t.Fatalf("retry count %d", r.RetryCount)
		}
	}
	x.wr.FailNext(intune.ErrPermanent)
	x.tick()
	x.wantRing(1, "halted")
	if err := resume(application.ReasonRetry); !hasGate(err, application.CodeRetryLimit) {
		t.Fatalf("fourth retry: %v", err)
	}
	if got := x.count(`SELECT count(*) FROM platform.audit_events WHERE action = 'endpoints.deployment_ring.retried' AND metadata->>'deploymentId' = $1`, x.dep.ID); got != application.MaxRingRetries {
		t.Fatalf("retries audited %d times", got)
	}
	// Every attempt outcome is audited.
	if got, want := x.depAudits("endpoints.deployment.attempt_finished"), len(x.attempts("set_assignment")); got != want || want != application.MaxRingRetries+1 {
		t.Fatalf("attempt audits %d, attempts %d", got, want)
	}
	// The attempt counter restarts: a transient error after a retry does not halt at once.
	y := newExEnv(t, "Retry App 2")
	y.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100})
	y.start()
	y.tick()
	y.wr.FailNext(intune.ErrPermanent)
	y.tick()
	v := y.cur().Version
	if _, err := y.svc.ResumeRing(ctx, y.starterCaller(), y.starter, y.dep.ID, y.ringID(1), &v, application.ReasonRetry); err != nil {
		t.Fatal(err)
	}
	y.wr.FailNext(intune.ErrTransient, intune.ErrTransient, intune.ErrTransient)
	y.tick()
	y.tick()
	y.tick()
	y.wantRing(1, "active")
	y.tick()
	y.wantTargets(map[string]string{"d1": "assignment_requested"})
}

func TestReviewRingDevicesAreFilteredToTheProvider(t *testing.T) {
	x := newExEnv(t, "Provider Filter App")
	ctx := context.Background()
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1", "d2"}, threshold: 100})
	x.start()
	x.tick()
	run := x.ringRun(1)
	tx, err := x.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	own, err := x.repo().RingDeviceExternalIDsTx(ctx, tx, run.ID, x.provider)
	if err != nil {
		t.Fatal(err)
	}
	other, err := x.repo().RingDeviceExternalIDsTx(ctx, tx, run.ID, "other-provider")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(own, []string{"d1", "d2"}) || len(other) != 0 {
		t.Fatalf("own %v other %v", own, other)
	}
}

func TestReviewExecutionTablesGuardsAndCompositeKeys(t *testing.T) {
	x := newExEnv(t, "Guard App")
	y := newExEnv(t, "Guard App 2")
	y.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100})
	x.schedule(ringSpec{name: "pilot", devs: []string{"d1"}, threshold: 100})
	x.start()
	x.tick()
	run := x.ringRun(1)
	var targetID string
	if err := x.pool.QueryRow(context.Background(), `SELECT id::text FROM endpoints.deployment_targets WHERE deployment_id = $1 LIMIT 1`, x.dep.ID).Scan(&targetID); err != nil {
		t.Fatal(err)
	}
	refused := []struct{ name, sql string }{
		{"ring transition of another deployment", `INSERT INTO endpoints.deployment_ring_transitions (deployment_id, ring_run_id, to_status, operation, actor_system, correlation_id)
			VALUES ('` + y.dep.ID + `', '` + run.ID + `', 'active', 'x', 'sys', 'c')`},
		{"attempt of another deployment", `INSERT INTO endpoints.deployment_attempts (deployment_id, ring_run_id, kind, attempt, operation_id, requested_at, outcome_code, correlation_id)
			VALUES ('` + y.dep.ID + `', '` + run.ID + `', 'set_assignment', 50, 'cross-op', now(), 'in_flight', 'c')`},
		{"target transition of another deployment", `INSERT INTO endpoints.deployment_target_transitions (deployment_id, target_id, to_state, correlation_id)
			VALUES ('` + y.dep.ID + `', '` + targetID + `', 'pending', 'c')`},
		{"target transition with an unknown state", `INSERT INTO endpoints.deployment_target_transitions (deployment_id, target_id, to_state, correlation_id)
			VALUES ('` + x.dep.ID + `', '` + targetID + `', 'bogus', 'c')`},
		{"approval appears approved", `UPDATE endpoints.deployment_ring_runs SET promotion_approval_id = gen_random_uuid(), promotion_approval_status = 'approved' WHERE id = '` + run.ID + `'`},
	}
	for _, c := range refused {
		if _, err := x.pool.Exec(context.Background(), c.sql); err == nil {
			t.Errorf("%s accepted", c.name)
		}
	}
	// Approval status: none -> pending is allowed, pending -> none only by a halt, approved is final until a halt.
	x.sql(`UPDATE endpoints.deployment_ring_runs SET promotion_approval_id = gen_random_uuid(), promotion_approval_status = 'pending' WHERE id = $1`, run.ID)
	if _, err := x.pool.Exec(context.Background(), `UPDATE endpoints.deployment_ring_runs SET promotion_approval_id = NULL, promotion_approval_status = NULL WHERE id = $1`, run.ID); err == nil {
		t.Error("approval withdrawn without a halt")
	}
	// An attempt is finished once; the pin never changes.
	x.sql(`INSERT INTO endpoints.deployment_attempts (deployment_id, ring_run_id, kind, attempt, operation_id, requested_at, outcome_code, correlation_id)
		VALUES ($1, $2, 'clear_assignment', 1, 'guard-op', now(), 'in_flight', 'c')`, x.dep.ID, run.ID)
	if _, err := x.pool.Exec(context.Background(), `UPDATE endpoints.deployment_attempts SET outcome_code = 'in_flight' WHERE operation_id = 'guard-op'`); err == nil {
		t.Error("in_flight rewritten")
	}
	x.sql(`UPDATE endpoints.deployment_attempts SET outcome_code = 'accepted', finished_at = now() WHERE operation_id = 'guard-op'`)
	if _, err := x.pool.Exec(context.Background(), `UPDATE endpoints.deployment_attempts SET outcome_code = 'permanent_error' WHERE operation_id = 'guard-op'`); err == nil {
		t.Error("finished attempt rewritten")
	}
	// A cancelled Deployment is final apart from the clearing of its assignments.
	x.cancel()
	for _, sql := range []string{
		`UPDATE endpoints.deployment_ring_runs SET settled_at = now() WHERE id = '` + run.ID + `'`,
		`UPDATE endpoints.deployment_ring_runs SET retry_count = 1 WHERE id = '` + run.ID + `'`,
		`UPDATE endpoints.deployment_targets SET state_reason = 'other' WHERE id = '` + targetID + `'`,
	} {
		if _, err := x.pool.Exec(context.Background(), sql); err == nil {
			t.Errorf("cancelled deployment changed: %s", sql)
		}
	}
	x.sql(`UPDATE endpoints.deployment_ring_runs SET assignment_cleared_at = now() WHERE id = $1`, run.ID)
}
