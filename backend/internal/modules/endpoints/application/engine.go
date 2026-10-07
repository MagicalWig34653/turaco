package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

// The deployment engine (F9 G3): the worker job endpoints.deployment_tick advances every running Deployment one
// bounded step per phase and is idempotent. One Deployment is worked on by one worker at a time (session advisory
// lock); every phase runs in its own transaction under the Deployment row lock, and the single external call (the
// assignment write) runs between two transactions with an operation id derived from the stored attempts, so a
// crash, a retry or a second worker never writes twice.
//
// Kill switch. Every tick first sweeps all running and paused Deployments for a closed security gate (uncapped, set
// based pre-filter, then the authoritative gate check per candidate); the capped, round-robin work (oldest tick first)
// follows. A closed gate pauses the Deployment, halts its rings and queues the clearing of every ring assignment (the same
// path as cancel); clearing needs SOFTWARE_DEPLOY_WRITE, without it the assignments stay queued (clear_pending).
//
// Write protocol. The decision to write is committed as an in_flight attempt BEFORE the provider call and finished
// after it, so a crash or a cancel in between always leaves a record that the provider may hold the assignment; the next
// tick turns a left-over in_flight into interrupted.
//
// Evidence rule. A target becomes awaiting_observation only after the management synchronization read the ring
// assignment and the group membership back (Assigned). It is decided only from the Management Observation of the
// artifact on that Device that is newer than the read-back: applied is success, failed is failure; pending, conflict,
// unknown and anything not newer never decide. The Device's software inventory corroborates; a contradiction raises
// the Endpoint Finding deployment_evidence_conflict but does not change the observation-derived state.

// HandleDeploymentTick is the job handler of DeploymentTickJobType. With the capability off it only runs the
// kill-switch sweep (pausing and halting reduce risk and need no capability); nothing is written to the provider.
func (s *Service) HandleDeploymentTick(ctx context.Context, job jobs.Job) error {
	corr := "job:" + job.ID
	if !s.deployWrite {
		return s.GateSweep(ctx, corr)
	}
	if err := s.AuditDeployWrite(ctx); err != nil {
		return err
	}
	return s.TickAll(ctx, corr)
}

// GateSweep closes the security gate of every running or paused Deployment whose gate is closed (uncapped).
func (s *Service) GateSweep(ctx context.Context, corr string) error {
	ids, err := s.store.GateSweep(ctx)
	if err != nil {
		return err
	}
	c := engineCaller(corr)
	var errs []error
	for _, id := range ids {
		err := s.store.InTx(ctx, func(tx pgx.Tx) error {
			st, err := s.lockExec(ctx, tx, id, nil, "kill_switch", DeploymentRunning, DeploymentPaused)
			var inv *InvalidTransitionError
			if errors.As(err, &inv) {
				return nil
			}
			if err != nil {
				return err
			}
			g, err := s.executionGate(ctx, tx, st.d, st.runs)
			if err != nil || g.code == "" {
				return err
			}
			return s.closeGate(ctx, tx, c, st, g.code)
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("gate sweep %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// TickAll runs the kill-switch sweep and then works on at most MaxTickDeployments Deployments (oldest tick first).
func (s *Service) TickAll(ctx context.Context, corr string) error {
	var errs []error
	if err := s.GateSweep(ctx, corr); err != nil {
		errs = append(errs, err)
	}
	ids, err := s.store.EngineWork(ctx, MaxTickDeployments)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, id := range ids {
		if err := s.TickDeployment(ctx, id, corr); err != nil {
			errs = append(errs, fmt.Errorf("deployment %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// TickDeployment advances one Deployment. It does nothing while another worker holds the Deployment.
func (s *Service) TickDeployment(ctx context.Context, id, corr string) error {
	unlock, ok, err := s.store.TryLockProvider(ctx, "deployment:"+id)
	if err != nil || !ok {
		return err
	}
	defer unlock()
	if err := s.store.TouchTicked(ctx, id); err != nil {
		return err
	}
	d, err := s.store.GetDeployment(ctx, id)
	if err != nil {
		return err
	}
	if err := s.interruptAttempts(ctx, id, corr); err != nil {
		return err
	}
	switch d.Status {
	case DeploymentResolvingTargets:
		return s.resolveStep(ctx, d, corr)
	case DeploymentRunning:
		plan, err := s.planStep(ctx, id, corr)
		if err != nil {
			return err
		}
		if plan != nil {
			if err := s.writeStep(ctx, id, corr, *plan); err != nil {
				return err
			}
		}
		return s.observeStep(ctx, id, corr)
	case DeploymentCancelled, DeploymentPaused:
		return s.clearStep(ctx, id, corr)
	}
	return nil
}

// interruptAttempts turns the in_flight attempts of a Deployment (left by a crashed worker; the advisory lock
// guarantees no write of this Deployment is running) into interrupted and audits each.
func (s *Service) interruptAttempts(ctx context.Context, id, corr string) error {
	c := engineCaller(corr)
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		list, err := s.store.InterruptAttemptsTx(ctx, tx, id, s.now())
		if err != nil {
			return err
		}
		for _, a := range list {
			if err := s.auditAttempt(ctx, tx, c, id, a.RingRunID, a.Kind, a.Attempt, OutcomeInterrupted); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) auditAttempt(ctx context.Context, tx pgx.Tx, c Caller, deploymentID, ringRunID, kind string, attempt int, outcome string) error {
	return audit.Record(ctx, tx, audit.Change{Action: "endpoints.deployment.attempt_finished", TargetType: "deployment", TargetID: deploymentID, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Metadata: map[string]any{"ringRunId": ringRunID, "kind": kind, "attempt": attempt, "outcome": outcome}})
}

func assignmentIntent(intent string) string {
	if intent == IntentUninstall {
		return "uninstall"
	}
	return "required"
}

// ---- resolution ----

func (s *Service) failResolution(ctx context.Context, id, corr, reason string, meta map[string]any) error {
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockDeploymentTx(ctx, tx, id)
		if err != nil || cur.Status != DeploymentResolvingTargets {
			return err
		}
		if meta == nil {
			meta = map[string]any{}
		}
		_, err = s.fail(ctx, tx, engineCaller(corr), cur, reason, meta)
		return err
	})
}

// resolveStep evaluates the Target Set of every ring (a Device belongs to the first ring that selects it), refuses
// a ring above its cap, an incomplete evaluation and rings without Devices, snapshots the targets and starts the
// first ring (resolving_targets -> ready -> running).
func (s *Service) resolveStep(ctx context.Context, d Deployment, corr string) error {
	rings, err := s.store.Rings(ctx, d.ID)
	if err != nil {
		return err
	}
	budget := s.newBudget()
	evals := make([]TargetEvaluation, len(rings))
	sets := make([]TargetSet, len(rings))
	for i, r := range rings {
		set, err := s.store.GetTargetSet(ctx, r.TargetSetID)
		if err != nil {
			return err
		}
		sets[i] = set
		var ev TargetEvaluation
		if err := s.store.ReadSnapshot(ctx, func(ctx context.Context) error {
			var err error
			ev, err = s.evaluateDefinition(ctx, set.Definition, budget)
			return err
		}); err != nil {
			return err
		}
		switch {
		case ev.Incomplete:
			return s.failResolution(ctx, d.ID, corr, ReasonEvalIncomplete, map[string]any{"ringId": r.ID})
		case ev.Truncated || ev.Matched > r.MaxTargets:
			return s.failResolution(ctx, d.ID, corr, ReasonTooManyTargets, map[string]any{"ringId": r.ID, "matched": ev.Matched, "maxTargets": r.MaxTargets})
		}
		evals[i] = ev
	}
	seen := map[string]bool{}
	perRing := make([][]string, len(rings))
	for i := range rings {
		for _, id := range evals[i].deviceIDs {
			if !seen[id] {
				seen[id] = true
				perRing[i] = append(perRing[i], id)
			}
		}
		if len(perRing[i]) == 0 {
			return s.failResolution(ctx, d.ID, corr, ReasonNoTargets, map[string]any{"ringId": rings[i].ID})
		}
	}
	code, meta, err := s.resolutionDrift(ctx, d, rings, sets, perRing)
	if err != nil {
		return err
	}
	if code != "" {
		return s.failResolution(ctx, d.ID, corr, code, meta)
	}
	c := engineCaller(corr)
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		st, err := s.lockExec(ctx, tx, d.ID, nil, "resolve", DeploymentResolvingTargets)
		if err != nil {
			return err
		}
		if len(st.runs) != 0 || len(st.rings) != len(rings) {
			return nil
		}
		g, err := s.executionGate(ctx, tx, st.d, nil)
		if err != nil {
			return err
		}
		if g.code != "" {
			_, err := s.fail(ctx, tx, c, st.d, g.code, nil)
			return err
		}
		var all []string
		for _, ids := range perRing {
			all = append(all, ids...)
		}
		facts, err := s.store.DeviceFactsTx(ctx, tx, all, g.version.ProductID, g.version.ProductVersion, s.now().Add(-s.evidenceFresh))
		if err != nil {
			return err
		}
		runs := make([]DeploymentRingRun, len(st.rings))
		for i, r := range st.rings {
			runs[i] = DeploymentRingRun{DeploymentID: st.d.ID, RingID: r.ID, Position: r.Position, GroupExternalID: intune.RingGroupID(r.ID)}
		}
		if runs, err = s.store.InsertRingRunsTx(ctx, tx, runs); err != nil {
			return err
		}
		var targets []TargetInsert
		for i, ids := range perRing {
			run, _, _ := st.runForRing(runs, st.rings[i].ID)
			for _, id := range ids {
				f, ok := facts[id]
				ti := TargetInsert{RingRunID: run.ID, DeviceID: id, State: TargetPending}
				switch {
				case !ok || !f.Live:
					ti.State, ti.Reason = TargetNotApplicable, ReasonDeviceGone
				case f.Provider != s.viewProvider:
					ti.State, ti.Reason = TargetNotApplicable, ReasonProviderMismatch
				// already_satisfied only from fresh inventory; stale inventory keeps the target pending (re-checked before each write).
				case g.pkg.ArtifactPlatform != "other" && f.Platform != g.pkg.ArtifactPlatform:
					ti.State, ti.Reason = TargetNotApplicable, ReasonPlatformMismatch
				case d.Intent == IntentUninstall && !f.HasVersion && f.Fresh:
					ti.State, ti.Reason = TargetAlreadySatisfied, ReasonNotInstalled
				case d.Intent != IntentUninstall && f.HasVersion && f.Fresh:
					ti.State, ti.Reason = TargetAlreadySatisfied, ReasonAlreadyPresent
				}
				targets = append(targets, ti)
			}
		}
		if err := s.store.InsertTargetsTx(ctx, tx, st.d.ID, targets, s.now(), corr); err != nil {
			return err
		}
		ready := st.d
		ready.Status = DeploymentReady
		cur, err := s.commitDeployment(ctx, tx, c, st.d, ready, "targets_resolved", "", map[string]any{"targets": len(targets), "rings": len(runs)})
		if err != nil {
			return err
		}
		running := cur
		running.Status = DeploymentRunning
		if _, err := s.commitDeployment(ctx, tx, c, cur, running, "running", "", nil); err != nil {
			return err
		}
		return s.activateRun(ctx, tx, c, runs[0], "activated")
	})
}

// resolutionDrift refuses a plan that is not what was scheduled: it became high impact (the resolved counts or the Target
// Sets' reasons now meet the high-impact rules although the plan was not high impact at scheduling) or the resolved target
// total exceeds the total evaluated at scheduling by more than MaxTargetGrowthPercent. Either needs a new plan.
func (s *Service) resolutionDrift(ctx context.Context, d Deployment, rings []DeploymentRing, sets []TargetSet, perRing [][]string) (string, map[string]any, error) {
	total := 0
	for _, ids := range perRing {
		total += len(ids)
	}
	if !d.HighImpact {
		var live int
		if err := s.store.ReadSnapshot(ctx, func(ctx context.Context) error {
			var err error
			live, err = s.store.CountLiveDevicesAfter(ctx, s.viewProvider, "")
			return err
		}); err != nil {
			return "", nil, err
		}
		hi := countIsHighImpact(total, live, s.hiTargets, s.hiPercent)
		for i, r := range rings {
			if r.NoWindowRequired && countIsHighImpact(len(perRing[i]), live, s.hiTargets, s.hiPercent) {
				hi = true
			}
		}
		for _, set := range sets {
			reason, err := s.setHighImpact(ctx, set.Definition)
			if err != nil {
				return "", nil, err
			}
			hi = hi || reason != ""
		}
		if hi {
			return ReasonBecameHighImpact, map[string]any{"targets": total}, nil
		}
	}
	if d.ScheduledTargets != nil && total*100 > *d.ScheduledTargets*(100+MaxTargetGrowthPercent) {
		return ReasonTargetsGrew, map[string]any{"targets": total, "scheduledTargets": *d.ScheduledTargets}, nil
	}
	return "", nil, nil
}

func (e execState) runForRing(runs []DeploymentRingRun, ringID string) (DeploymentRingRun, int, bool) {
	for i, r := range runs {
		if r.RingID == ringID {
			return r, i, true
		}
	}
	return DeploymentRingRun{}, 0, false
}

// ---- assignment write ----

type writePlan struct {
	runID   string
	ringID  string
	attempt int
	op      intune.RingAssignmentOp
	// requested is when the write was decided.
	requested time.Time
}

func activeRun(runs []DeploymentRingRun) (DeploymentRingRun, bool) {
	for _, r := range runs {
		if r.Status == RingActive {
			return r, true
		}
	}
	return DeploymentRingRun{}, false
}

func (s *Service) ringHaltAndPause(ctx context.Context, tx pgx.Tx, c Caller, st execState, run DeploymentRingRun, op, reason string) error {
	if _, err := s.haltRun(ctx, tx, c, run, op, reason); err != nil {
		return err
	}
	cur, err := s.store.DeploymentTx(ctx, tx, st.d.ID)
	if err != nil || cur.Status != DeploymentRunning {
		return err
	}
	next := cur
	why := ReasonRingHalted
	next.Status, next.StatusReason = DeploymentPaused, &why
	_, err = s.commitDeployment(ctx, tx, c, cur, next, "paused", why, map[string]any{"ringId": run.RingID, "cause": reason})
	return err
}

// planStep applies the kill switch and decides whether the active ring needs its assignment written. The decision is
// committed together with the in_flight attempt (and the artifact pin) before the provider is called.
func (s *Service) planStep(ctx context.Context, id, corr string) (*writePlan, error) {
	rings, err := s.store.Rings(ctx, id)
	if err != nil {
		return nil, err
	}
	windows, err := s.lookupWindows(ctx, rings)
	if err != nil {
		return nil, err
	}
	var plan *writePlan
	c := engineCaller(corr)
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		st, err := s.lockExec(ctx, tx, id, nil, "tick")
		if err != nil || st.d.Status != DeploymentRunning {
			return err
		}
		g, err := s.executionGate(ctx, tx, st.d, st.runs)
		if err != nil {
			return err
		}
		if g.code != "" {
			return s.closeGate(ctx, tx, c, st, g.code)
		}
		run, ok := activeRun(st.runs)
		if !ok {
			return nil
		}
		now := s.now()
		if _, err := s.store.RecheckSatisfiedTx(ctx, tx, run.ID, g.version.ProductID, g.version.ProductVersion, st.d.Intent == IntentUninstall, now.Add(-s.evidenceFresh), now, corr); err != nil {
			return err
		}
		counts, err := s.store.RingCountsTx(ctx, tx, run.ID, "", time.Time{})
		if err != nil {
			return err
		}
		if counts.ByState[TargetPending] == 0 {
			return nil
		}
		n, accepted, err := s.store.AttemptStateTx(ctx, tx, run.ID, AttemptSet)
		if err != nil {
			return err
		}
		if accepted {
			return s.requestAssignments(ctx, tx, c, run, n)
		}
		if n-run.AttemptBase >= MaxAssignmentAttempts {
			return s.ringHaltAndPause(ctx, tx, c, st, run, "write_failed", ReasonAssignmentFailed)
		}
		ring, _ := st.ring(run.RingID)
		if !windowOpen(ring, windows, now) {
			_, err := s.pauseAndHalt(ctx, tx, c, st, "paused", ReasonWindowClosed, false)
			return err
		}
		devs, err := s.store.RingDeviceExternalIDsTx(ctx, tx, run.ID, s.viewProvider)
		if err != nil {
			return err
		}
		if len(devs) == 0 {
			return nil
		}
		if run.ManagementArtifactID == nil {
			next := run
			next.ManagementArtifactID, next.ManagementArtifactExternalID = g.pkg.ArtifactID, &g.pkg.ArtifactExternalID
			if run, err = s.commitRun(ctx, tx, c, run, next, "artifact_pinned", "", nil); err != nil {
				return err
			}
		}
		op := intune.RingAssignmentOp{OperationID: fmt.Sprintf("%s:%s:%d", st.d.ID, run.RingID, n+1),
			ManagementArtifactExternalID: *run.ManagementArtifactExternalID, RingKey: run.RingID, TargetGroupExternalID: run.GroupExternalID,
			Intent: assignmentIntent(st.d.Intent), DeviceExternalIDs: devs}
		if err := s.store.BeginAttemptTx(ctx, tx, DeploymentAttempt{DeploymentID: id, RingRunID: run.ID, Kind: AttemptSet, Attempt: n + 1,
			OperationID: op.OperationID, RequestedAt: now}, corr); err != nil {
			return err
		}
		plan = &writePlan{runID: run.ID, ringID: run.RingID, attempt: n + 1, op: op, requested: now}
		return nil
	})
	return plan, err
}

func (s *Service) requestAssignments(ctx context.Context, tx pgx.Tx, c Caller, run DeploymentRingRun, attempt int) error {
	n, err := s.store.RequestAssignmentsTx(ctx, tx, run.ID, s.now(), c.CorrelationID)
	if err != nil {
		return err
	}
	next := run
	if next.AssignmentRequestedAt == nil {
		now := s.now()
		next.AssignmentRequestedAt = &now
	}
	_, err = s.commitRun(ctx, tx, c, run, next, "assignment_requested", "", map[string]any{"attempt": attempt, "targets": n})
	return err
}

func outcomeOf(err error) string {
	switch {
	case err == nil:
		return OutcomeAccepted
	case intune.IsTransient(err):
		return OutcomeTransient
	}
	return OutcomePermanent
}

// writeStep performs the assignment write outside any transaction and finishes the attempt.
func (s *Service) writeStep(ctx context.Context, id, corr string, plan writePlan) error {
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	werr := s.writer.SetRingAssignment(wctx, plan.op)
	cancel()
	outcome := outcomeOf(werr)
	c := engineCaller(corr)
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		st, err := s.lockExec(ctx, tx, id, nil, "write")
		if err != nil {
			return err
		}
		if err := s.store.FinishAttemptTx(ctx, tx, plan.runID, AttemptSet, plan.attempt, outcome, s.now()); err != nil {
			return err
		}
		if err := s.auditAttempt(ctx, tx, c, id, plan.runID, AttemptSet, plan.attempt, outcome); err != nil {
			return err
		}
		run, _, ok := st.run(plan.ringID)
		if !ok || st.d.Status != DeploymentRunning || run.Status != RingActive {
			return nil
		}
		switch {
		case outcome == OutcomeAccepted:
			return s.requestAssignments(ctx, tx, c, run, plan.attempt)
		case outcome == OutcomePermanent || plan.attempt-run.AttemptBase >= MaxAssignmentAttempts:
			return s.ringHaltAndPause(ctx, tx, c, st, run, "write_failed", ReasonAssignmentFailed)
		}
		return nil
	})
}

// ---- evidence ----

func (s *Service) conflictReason(intent, state string, d TargetDecision) string {
	if !d.DeviceFresh {
		return ""
	}
	switch {
	case state == TargetSuccessful && intent != IntentUninstall && !d.HasVersion:
		return "applied_without_installation"
	case state == TargetSuccessful && intent == IntentUninstall && d.HasVersion:
		return "applied_but_installed"
	case state == TargetFailed && intent != IntentUninstall && d.HasVersion:
		return "failed_with_installation"
	}
	return ""
}

func (s *Service) raiseConflict(ctx context.Context, tx pgx.Tx, c Caller, st execState, run DeploymentRingRun, d TargetDecision, reason string) error {
	detail, _ := json.Marshal(map[string]any{"deploymentId": st.d.ID, "ringId": run.RingID, "targetId": d.TargetID, "reason": reason})
	id, raised, err := s.store.OpenFindingTx(ctx, tx, "deployment_evidence_conflict", d.DeviceID, detail)
	if err != nil {
		return err
	}
	if !raised {
		return nil
	}
	return audit.Record(ctx, tx, audit.Change{Action: "endpoints.deployment.evidence_conflict", TargetType: "deployment", TargetID: st.d.ID, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Metadata: map[string]any{"findingId": id, "deviceId": d.DeviceID, "ringId": run.RingID, "reason": reason}})
}

// observeStep reads back assignments, decides targets from observations, expires waiting targets and moves rings.
func (s *Service) observeStep(ctx context.Context, id, corr string) error {
	c := engineCaller(corr)
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		st, err := s.lockExec(ctx, tx, id, nil, "observe", DeploymentRunning)
		var inv *InvalidTransitionError
		if errors.As(err, &inv) {
			return nil
		}
		if err != nil {
			return err
		}
		g, err := s.executionGate(ctx, tx, st.d, st.runs)
		if err != nil {
			return err
		}
		if g.code != "" {
			return s.closeGate(ctx, tx, c, st, g.code)
		}
		now := s.now()
		for _, run := range st.runs {
			if run.Status != RingActive && run.Status != RingAwaitingPromotion {
				continue
			}
			ring, ok := st.ring(run.RingID)
			if !ok {
				continue
			}
			// The pinned artifact is the one written; a ring that was not written yet has none to read back.
			art := *g.pkg.ArtifactID
			if run.ManagementArtifactID != nil {
				art = *run.ManagementArtifactID
			}
			if _, err := s.store.DetectReadBackTx(ctx, tx, run.ID, art, run.GroupExternalID, assignmentIntent(st.d.Intent), now, s.obsExpiry, corr); err != nil {
				return err
			}
			decisions, err := s.store.DecideTargetsTx(ctx, tx, run.ID, art, g.version.ProductID, g.version.ProductVersion, now, corr)
			if err != nil {
				return err
			}
			for _, d := range decisions {
				if reason := s.conflictReason(st.d.Intent, d.State, d); reason != "" {
					if err := s.raiseConflict(ctx, tx, c, st, run, d, reason); err != nil {
						return err
					}
				}
			}
			if _, err := s.store.ExpireTargetsTx(ctx, tx, run.ID, now, s.obsExpiry, corr); err != nil {
				return err
			}
			halted, err := s.evaluateRing(ctx, tx, c, st, run, ring, art)
			if err != nil || halted {
				return err
			}
		}
		return nil
	})
}

// evaluateRing moves a ring after new evidence: failure threshold -> halt, settled -> soak -> awaiting promotion, and
// for the last ring completion of the Deployment. It reports whether the ring was halted.
func (s *Service) evaluateRing(ctx context.Context, tx pgx.Tx, c Caller, st execState, run DeploymentRingRun, ring DeploymentRing, art string) (bool, error) {
	now := s.now()
	counts, err := s.store.RingCountsTx(ctx, tx, run.ID, art, now.Add(-s.evidenceFresh))
	if err != nil {
		return false, err
	}
	den := counts.Denominator()
	thr := ring.SuccessThresholdPercent
	if run.Status == RingActive {
		if targets := counts.Total() - counts.ByState[TargetCancelled]; counts.ProviderNA > 0 && counts.ProviderNA*100 > MaxProviderNotApplicablePercent*targets {
			return true, s.ringHaltAndPause(ctx, tx, c, st, run, "auto_halted", ReasonNARatio)
		}
		failures := counts.ByState[TargetFailed] + counts.ByState[TargetExpired]
		decided := counts.ByState[TargetSuccessful] + failures
		if den > 0 && decided >= min(MinAutoHaltSample, den) && failures*100 > (100-thr)*den {
			return true, s.ringHaltAndPause(ctx, tx, c, st, run, "auto_halted", ReasonFailureThreshold)
		}
		if counts.Open() > 0 {
			return false, nil
		}
		if den < counts.MinEvidence() {
			// No (or too little) evidence is never success: the ring stays active until the evidence window of the ring ran out.
			if run.ActivatedAt != nil && !now.Before(run.ActivatedAt.Add(s.obsExpiry)) {
				return true, s.ringHaltAndPause(ctx, tx, c, st, run, "auto_halted", ReasonNoEvidence)
			}
			return false, nil
		}
		if run.SettledAt == nil {
			next := run
			next.SettledAt = &now
			if run, err = s.commitRun(ctx, tx, c, run, next, "settled", "", map[string]any{"targets": counts.Total()}); err != nil {
				return false, err
			}
		}
		if counts.ByState[TargetSuccessful]*100 < thr*den {
			return true, s.ringHaltAndPause(ctx, tx, c, st, run, "auto_halted", ReasonThresholdNotMet)
		}
		if now.Before(run.SettledAt.Add(time.Duration(ring.SoakMinutes) * time.Minute)) {
			return false, nil
		}
		next := run
		next.Status, next.AwaitingSince = RingAwaitingPromotion, &now
		if run, err = s.commitRun(ctx, tx, c, run, next, "awaiting_promotion", "", nil); err != nil {
			return false, err
		}
	}
	last := run.Position == st.runs[len(st.runs)-1].Position
	if !last || evidenceCode(run, ring, counts, now) != "" {
		return false, nil
	}
	return false, s.completeDeployment(ctx, tx, c, st, run)
}

func (s *Service) completeDeployment(ctx context.Context, tx pgx.Tx, c Caller, st execState, last DeploymentRingRun) error {
	now := s.now()
	promoted := last
	promoted.Status, promoted.PromotedAt = RingPromoted, &now
	if _, err := s.commitRun(ctx, tx, c, last, promoted, "promoted", "", nil); err != nil {
		return err
	}
	if err := publish(ctx, tx, c, EventRingPromoted, map[string]any{"deploymentId": st.d.ID, "ringId": last.RingID, "ringRunId": last.ID, "position": last.Position}); err != nil {
		return err
	}
	succeeded, failed := 0, 0
	for _, r := range st.runs {
		cnt, err := s.store.RingCountsTx(ctx, tx, r.ID, "", time.Time{})
		if err != nil {
			return err
		}
		succeeded += cnt.ByState[TargetSuccessful]
		failed += cnt.ByState[TargetFailed] + cnt.ByState[TargetExpired]
	}
	cur, err := s.store.DeploymentTx(ctx, tx, st.d.ID)
	if err != nil {
		return err
	}
	next := cur
	next.Status, next.FinishedAt = DeploymentCompleted, &now
	if failed > 0 {
		next.Status = DeploymentCompletedWithError
	}
	if _, err := s.commitDeployment(ctx, tx, c, cur, next, "completed", "", map[string]any{"successful": succeeded, "failed": failed}); err != nil {
		return err
	}
	return publish(ctx, tx, c, EventDeploymentCompleted, map[string]any{"deploymentId": st.d.ID, "status": next.Status, "successful": succeeded, "failed": failed})
}

// ---- clearing ----

// needsClear reports a ring run whose assignment is queued for clearing and not cleared.
func needsClear(r DeploymentRingRun) bool {
	return r.ClearRequestedAt != nil && r.AssignmentClearedAt == nil
}

// queueClear queues the clearing of every ring assignment of the Deployment that the provider may hold (a set attempt
// of any outcome exists, in_flight included). The ring runs are read again: halts changed them.
func (s *Service) queueClear(ctx context.Context, tx pgx.Tx, c Caller, deploymentID, reason string) error {
	runs, err := s.store.RingRunsTx(ctx, tx, deploymentID)
	if err != nil {
		return err
	}
	for _, r := range runs {
		if r.ClearRequestedAt != nil || r.AssignmentClearedAt != nil {
			continue
		}
		n, _, err := s.store.AttemptStateTx(ctx, tx, r.ID, AttemptSet)
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		next := r
		now := s.now()
		next.ClearRequestedAt = &now
		if _, err := s.commitRun(ctx, tx, c, r, next, "clear_queued", reason, map[string]any{"attempts": n}); err != nil {
			return err
		}
	}
	return nil
}

type clearPlan struct {
	run      DeploymentRingRun
	attempt  int
	artifact string
	opID     string
}

// clearStep clears the queued ring assignments of a cancelled or kill-switched Deployment, every ring in its own call
// (a ring that keeps failing does not block the others). Each call is an in_flight attempt first, idempotent per operation
// id; after MaxClearAttempts failures the Endpoint Finding deployment_clear_failed is raised and the engine stops.
func (s *Service) clearStep(ctx context.Context, id, corr string) error {
	if !s.deployWrite {
		return nil
	}
	var plans []clearPlan
	c := engineCaller(corr)
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		st, err := s.lockExec(ctx, tx, id, nil, "clear", DeploymentCancelled, DeploymentPaused)
		var inv *InvalidTransitionError
		if errors.As(err, &inv) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, r := range st.runs {
			if !needsClear(r) || r.ManagementArtifactExternalID == nil {
				continue
			}
			n, _, err := s.store.AttemptStateTx(ctx, tx, r.ID, AttemptClear)
			if err != nil {
				return err
			}
			if n >= MaxClearAttempts {
				continue
			}
			p := clearPlan{run: r, attempt: n + 1, artifact: *r.ManagementArtifactExternalID, opID: fmt.Sprintf("%s:%s:clear:%d", id, r.RingID, n+1)}
			if err := s.store.BeginAttemptTx(ctx, tx, DeploymentAttempt{DeploymentID: id, RingRunID: r.ID, Kind: AttemptClear, Attempt: p.attempt,
				OperationID: p.opID, RequestedAt: s.now()}, corr); err != nil {
				return err
			}
			plans = append(plans, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range plans {
		wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		werr := s.writer.ClearRingAssignment(wctx, p.opID, p.artifact, p.run.RingID)
		cancel()
		outcome := outcomeOf(werr)
		if err := s.store.InTx(ctx, func(tx pgx.Tx) error {
			st, err := s.lockExec(ctx, tx, id, nil, "clear")
			if err != nil {
				return err
			}
			if err := s.store.FinishAttemptTx(ctx, tx, p.run.ID, AttemptClear, p.attempt, outcome, s.now()); err != nil {
				return err
			}
			if err := s.auditAttempt(ctx, tx, c, id, p.run.ID, AttemptClear, p.attempt, outcome); err != nil {
				return err
			}
			run, _, ok := st.run(p.run.RingID)
			if !ok || run.AssignmentClearedAt != nil {
				return nil
			}
			if outcome == OutcomeAccepted {
				next := run
				now := s.now()
				next.AssignmentClearedAt = &now
				_, err := s.commitRun(ctx, tx, c, run, next, "assignment_cleared", "", map[string]any{"attempt": p.attempt})
				return err
			}
			if p.attempt >= MaxClearAttempts {
				return s.raiseClearFailed(ctx, tx, c, st, run, p.attempt)
			}
			return nil
		}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// raiseClearFailed raises the Endpoint Finding deployment_clear_failed for a ring whose assignment could not be cleared.
// It is a Device finding on the first target Device of the ring; the detail names the Deployment and the ring.
func (s *Service) raiseClearFailed(ctx context.Context, tx pgx.Tx, c Caller, st execState, run DeploymentRingRun, attempts int) error {
	dev, err := s.store.RingFirstDeviceTx(ctx, tx, run.ID)
	if err != nil {
		return err
	}
	detail, _ := json.Marshal(map[string]any{"deploymentId": st.d.ID, "ringId": run.RingID, "attempts": attempts})
	id, raised, err := s.store.OpenFindingTx(ctx, tx, "deployment_clear_failed", dev, detail)
	if err != nil {
		return err
	}
	if !raised {
		return nil
	}
	return audit.Record(ctx, tx, audit.Change{Action: "endpoints.deployment.clear_failed", TargetType: "deployment", TargetID: st.d.ID, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Metadata: map[string]any{"findingId": id, "ringId": run.RingID, "attempts": attempts}})
}
