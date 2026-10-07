package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

// Deployment execution operations (F9 G3). Every operation needs deployments.execute (a person), expectedVersion
// (the Deployment's version), is audited (endpoints.deployment.<op> and endpoints.deployment_ring.<op>, ids, codes
// and counts only) and appends to the append-only histories. Operations that issue new provider writes (start,
// resume, resume ring, promote) need the capability SOFTWARE_DEPLOY_WRITE; pause, halt and cancel reduce risk and
// work without it (clearing assignments of a cancelled Deployment waits for the capability).

// HaltReasons are the reason codes a person may give to halt a ring or a Deployment.
var HaltReasons = []string{"manual_halt", "quality_issue", "security_risk", "other"}

// engineCaller is the caller of an engine step.
func engineCaller(corr string) Caller {
	return Caller{Actor: audit.SystemActor(DeploymentEngineActor), CorrelationID: corr}
}

// WithDeployWrite switches the write capability (SOFTWARE_DEPLOY_WRITE) on and sets the writer. With the capability
// off every operation that would write returns the gate deploy_write_disabled; a nil writer keeps the placeholder.
func (s *Service) WithDeployWrite(enabled bool, w intune.AssignmentWriter) *Service {
	s.deployWrite = enabled
	if w != nil {
		s.writer = w
	}
	return s
}

// WithExecutionTimings sets how long a target waits for evidence after read-back (default DefaultObservationExpiry)
// and how recent the evidence of a promotion gate must be (default DefaultEvidenceFreshness); zero keeps a default.
func (s *Service) WithExecutionTimings(expiry, freshness time.Duration) *Service {
	if expiry > 0 {
		s.obsExpiry = expiry
	}
	if freshness > 0 {
		s.evidenceFresh = freshness
	}
	return s
}

// AuditDeployWrite records once per process that the write capability is enabled (audit endpoints.deploy_write.enabled,
// system actor deploy-write). It runs at worker start and on the first start or tick; with the capability off it does
// nothing.
func (s *Service) AuditDeployWrite(ctx context.Context) error {
	if !s.deployWrite || s.deployWriteAudited.Load() {
		return nil
	}
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Change{Action: "endpoints.deploy_write.enabled", TargetType: "capability", TargetID: "software_deploy_write",
			Actor: audit.SystemActor(DeployWriteActor), CorrelationID: "capability:" + s.now().UTC().Format(time.RFC3339Nano),
			Metadata: map[string]any{"capability": "SOFTWARE_DEPLOY_WRITE"}})
	})
	if err == nil {
		s.deployWriteAudited.Store(true)
	}
	return err
}

func (s *Service) writeGate() error {
	if !s.deployWrite {
		return &GateError{Code: CodeDeployWriteDisabled}
	}
	return nil
}

func (s *Service) execPreamble(c Caller, p Principal, expected *int, id string) error {
	if err := c.validate(); err != nil {
		return err
	}
	if !p.DeploymentsExecute {
		return ErrForbidden
	}
	if c.Actor.UserID == "" {
		return invalid("deployments are executed by a person")
	}
	if !validUUID(id) {
		return ErrNotFound
	}
	if expected == nil {
		return invalid("expectedVersion is required")
	}
	return nil
}

// execState is a locked Deployment with its rings and ring runs.
type execState struct {
	d     Deployment
	rings []DeploymentRing
	runs  []DeploymentRingRun
}

func (e execState) ring(ringID string) (DeploymentRing, bool) { return findRing(e.rings, ringID) }

func (e execState) run(ringID string) (DeploymentRingRun, int, bool) {
	for i, r := range e.runs {
		if r.RingID == ringID {
			return r, i, true
		}
	}
	return DeploymentRingRun{}, 0, false
}

func (e execState) ringOfRun(runID string) (DeploymentRing, bool) {
	for _, r := range e.runs {
		if r.ID == runID {
			return e.ring(r.RingID)
		}
	}
	return DeploymentRing{}, false
}

func (s *Service) lockExec(ctx context.Context, tx pgx.Tx, id string, expected *int, op string, from ...string) (execState, error) {
	cur, err := s.store.LockDeploymentTx(ctx, tx, strings.ToLower(id))
	if err != nil {
		return execState{}, err
	}
	if expected != nil && *expected != cur.Version {
		return execState{}, ErrVersionConflict
	}
	if len(from) > 0 && !slices.Contains(from, cur.Status) {
		return execState{}, &InvalidTransitionError{Operation: op, From: cur.Status}
	}
	rings, err := s.store.RingsTx(ctx, tx, cur.ID)
	if err != nil {
		return execState{}, err
	}
	runs, err := s.store.RingRunsTx(ctx, tx, cur.ID)
	if err != nil {
		return execState{}, err
	}
	return execState{d: cur, rings: rings, runs: runs}, nil
}

func ringRunState(r DeploymentRingRun) map[string]any {
	m := map[string]any{"status": r.Status, "position": r.Position}
	if r.StatusReason != nil {
		m["reason"] = *r.StatusReason
	}
	return m
}

// commitRun stores the next ring run state, appends a transition when the status changed and audits.
func (s *Service) commitRun(ctx context.Context, tx pgx.Tx, c Caller, cur, next DeploymentRingRun, op, reason string, meta map[string]any) (DeploymentRingRun, error) {
	updated, err := s.store.UpdateRingRunTx(ctx, tx, next)
	if err != nil {
		return DeploymentRingRun{}, err
	}
	if cur.Status != updated.Status {
		from := cur.Status
		t := RingTransition{DeploymentID: cur.DeploymentID, RingRunID: cur.ID, From: &from, To: updated.Status, Operation: op, Reason: strPtr(reason),
			CorrelationID: c.CorrelationID}
		t.ActorUserID, t.ActorSystem = softwareActor(c)
		if err := s.store.AppendRingTransitionTx(ctx, tx, t); err != nil {
			return DeploymentRingRun{}, err
		}
	}
	if meta == nil {
		meta = map[string]any{}
	}
	meta["deploymentId"] = cur.DeploymentID
	meta["ringId"] = cur.RingID
	if reason != "" {
		meta["reason"] = reason
	}
	return updated, audit.Record(ctx, tx, audit.Change{Action: "endpoints.deployment_ring." + op, TargetType: "deployment_ring", TargetID: cur.ID, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: ringRunState(cur), After: ringRunState(updated), Metadata: meta})
}

// gateResult is the outcome of the execution gates of a Deployment.
type gateResult struct {
	code    string
	version SoftwareVersion
	pkg     PublishedPackage
}

// executionGate re-checks, inside the caller's transaction, everything the rollout depends on: the version and its
// product are approved, the package is published, linked to its Management Artifact, has no open hash or
// publication finding, carries the approved installer hash and is still the artifact that the ring runs pinned at their
// first write (a relinked artifact closes the gate: artifact_changed). A non-empty code closes the gate.
func (s *Service) executionGate(ctx context.Context, tx pgx.Tx, d Deployment, runs []DeploymentRingRun) (gateResult, error) {
	v, err := s.store.ShareVersionTx(ctx, tx, d.SoftwareVersionID)
	if err != nil {
		return gateResult{}, err
	}
	out := gateResult{version: v}
	prod, err := s.store.ShareProductTx(ctx, tx, v.ProductID)
	if err != nil {
		return gateResult{}, err
	}
	switch {
	case v.ApprovalStatus == VersionRevoked:
		out.code = ReasonVersionRevoked
		return out, nil
	case v.ApprovalStatus != VersionApproved:
		out.code = IssueVersionNotApproved
		return out, nil
	case prod.ApprovalStatus == ProductBlocked:
		out.code = ReasonProductBlocked
		return out, nil
	case prod.ApprovalStatus != ProductApproved:
		out.code = IssueProductNotApproved
		return out, nil
	}
	closed, _, err := s.store.PackageGateTx(ctx, tx, v.ID)
	if err != nil {
		return gateResult{}, err
	}
	if closed {
		out.code = ReasonPackageGateClosed
		return out, nil
	}
	pkg, ok, err := s.store.PublishedPackageTx(ctx, tx, v.ID)
	if err != nil {
		return gateResult{}, err
	}
	switch {
	case !ok:
		out.code = ReasonPackageNotPublish
	case pkg.ArtifactID == nil || pkg.ArtifactExternalID == "":
		out.code = ReasonArtifactUnlinked
	case !pkg.HashMatches:
		out.code = ReasonHashMismatch
	}
	out.pkg = pkg
	if out.code == "" {
		for _, r := range runs {
			if r.ManagementArtifactID != nil && (*r.ManagementArtifactID != *pkg.ArtifactID ||
				r.ManagementArtifactExternalID == nil || *r.ManagementArtifactExternalID != pkg.ArtifactExternalID) {
				out.code = ReasonArtifactChanged
				break
			}
		}
	}
	return out, nil
}

// closeGate is the kill switch: the Deployment pauses with the gate code, its rings halt and the clearing of every ring
// assignment the provider may hold is queued (the same path as cancel). Idempotent.
func (s *Service) closeGate(ctx context.Context, tx pgx.Tx, c Caller, st execState, code string) error {
	if _, err := s.pauseAndHalt(ctx, tx, c, st, "kill_switch", code, true); err != nil {
		return err
	}
	return s.queueClear(ctx, tx, c, st.d.ID, code)
}

func (s *Service) fail(ctx context.Context, tx pgx.Tx, c Caller, cur Deployment, reason string, meta map[string]any) (Deployment, error) {
	next := cur
	now := s.now()
	next.Status, next.StatusReason, next.FinishedAt = DeploymentFailed, &reason, &now
	out, err := s.commitDeployment(ctx, tx, c, cur, next, "failed", reason, meta)
	if err != nil {
		return Deployment{}, err
	}
	return out, publish(ctx, tx, c, EventDeploymentFailed, map[string]any{"deploymentId": cur.ID, "reason": reason})
}

// windowOpen reports whether the ring's Change window is open (or the ring needs none).
func windowOpen(r DeploymentRing, windows map[string]ChangeWindow, now time.Time) bool {
	if r.NoWindowRequired {
		return true
	}
	if r.ChangeID == nil {
		return false
	}
	cw, ok := windows[*r.ChangeID]
	return changeWindowOpen(cw, ok, now)
}

func planners(d Deployment) []string {
	out := append([]string{d.OwnerUserID, d.CreatedBy}, d.Editors...)
	for _, p := range []*string{d.SubmittedBy, d.ScheduledBy} {
		if p != nil {
			out = append(out, *p)
		}
	}
	return out
}

// StartDeployment starts a scheduled Deployment: it re-verifies the plan hash, the approvals, the published package
// with its approved hash and the Change window of the first ring, and moves the Deployment to resolving_targets; the
// engine then resolves and snapshots the targets of every ring and activates the first one. For a high-impact plan
// the starter must not have planned it (owner, creator, editor, submitter, scheduler). Needs deployments.execute and
// the write capability.
func (s *Service) StartDeployment(ctx context.Context, c Caller, p Principal, id string, expected *int) (Deployment, error) {
	if err := s.execPreamble(c, p, expected, id); err != nil {
		return Deployment{}, err
	}
	if err := s.writeGate(); err != nil {
		return Deployment{}, err
	}
	if err := s.AuditDeployWrite(ctx); err != nil {
		return Deployment{}, err
	}
	pre, err := s.store.Rings(ctx, strings.ToLower(id))
	if err != nil {
		return Deployment{}, err
	}
	windows, err := s.lookupWindows(ctx, pre)
	if err != nil {
		return Deployment{}, err
	}
	var out Deployment
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		st, err := s.lockExec(ctx, tx, id, expected, "start", DeploymentScheduled)
		if err != nil {
			return err
		}
		cur := st.d
		if len(st.rings) == 0 {
			return &GateError{Code: IssueNoRings}
		}
		sets, err := s.store.ShareTargetSetsTx(ctx, tx, ringSetIDs(st.rings))
		if err != nil {
			return err
		}
		if cur.PlanSHA256 == nil || planSHA256(cur, st.rings, sets) != *cur.PlanSHA256 {
			return &GateError{Code: CodePlanChanged}
		}
		if cur.HighImpact && slices.Contains(planners(cur), c.Actor.UserID) {
			return ErrSeparationOfPlanning
		}
		g, err := s.executionGate(ctx, tx, cur, nil)
		if err != nil {
			return err
		}
		if g.code != "" {
			return &GateError{Code: g.code}
		}
		if !windowOpen(st.rings[0], windows, s.now()) {
			return &GateError{Code: CodeWindowClosed}
		}
		next := cur
		now := s.now()
		by := c.Actor.UserID
		next.Status, next.StartedBy, next.StartedAt = DeploymentResolvingTargets, &by, &now
		if out, err = s.commitDeployment(ctx, tx, c, cur, next, "started", "", map[string]any{"ringCount": len(st.rings), "highImpact": cur.HighImpact}); err != nil {
			return err
		}
		return publish(ctx, tx, c, EventDeploymentStarted, map[string]any{"deploymentId": cur.ID, "versionId": cur.SoftwareVersionID,
			"intent": cur.Intent, "ringCount": len(st.rings), "highImpact": cur.HighImpact})
	})
	return out, err
}

// pauseAndHalt pauses a running Deployment with the reason and halts its active or awaiting ring run (the kill
// switch). halted says whether to halt the rings (false: plain pause).
func (s *Service) pauseAndHalt(ctx context.Context, tx pgx.Tx, c Caller, st execState, op, reason string, haltRings bool) (Deployment, error) {
	next := st.d
	next.Status, next.StatusReason = DeploymentPaused, &reason
	out := st.d
	var err error
	if st.d.Status != DeploymentPaused || (st.d.StatusReason == nil || *st.d.StatusReason != reason) {
		if out, err = s.commitDeployment(ctx, tx, c, st.d, next, op, reason, map[string]any{"ringsHalted": haltRings}); err != nil {
			return Deployment{}, err
		}
	}
	if !haltRings {
		return out, nil
	}
	for _, r := range st.runs {
		if r.Status != RingActive && r.Status != RingAwaitingPromotion {
			continue
		}
		if _, err := s.haltRun(ctx, tx, c, r, op, reason); err != nil {
			return Deployment{}, err
		}
	}
	return out, nil
}

func (s *Service) haltRun(ctx context.Context, tx pgx.Tx, c Caller, r DeploymentRingRun, op, reason string) (DeploymentRingRun, error) {
	next := r
	now := s.now()
	next.Status, next.StatusReason, next.HaltedAt, next.AwaitingSince = RingHalted, &reason, &now, nil
	// A halt withdraws the promotion Approval: a later approval must not promote a ring that was halted in between.
	var meta map[string]any
	if r.PromotionApprovalID != nil {
		if r.PromotionApprovalStatus != nil && *r.PromotionApprovalStatus == approvalPending {
			if err := s.approvals.CancelRingBySubjectInTx(ctx, tx, c.Actor, c.CorrelationID, r.ID); err != nil {
				return DeploymentRingRun{}, err
			}
		}
		next.PromotionApprovalID, next.PromotionApprovalStatus, next.PromotionApprovalRequestedBy = nil, nil, nil
		meta = map[string]any{"approvalWithdrawn": true}
	}
	out, err := s.commitRun(ctx, tx, c, r, next, op, reason, meta)
	if err != nil {
		return DeploymentRingRun{}, err
	}
	return out, publish(ctx, tx, c, EventRingHalted, map[string]any{"deploymentId": r.DeploymentID, "ringId": r.RingID, "ringRunId": r.ID, "reason": reason})
}

// PauseDeployment pauses a running Deployment (no further assignment writes; ring state is kept).
func (s *Service) PauseDeployment(ctx context.Context, c Caller, p Principal, id string, expected *int) (Deployment, error) {
	if err := s.execPreamble(c, p, expected, id); err != nil {
		return Deployment{}, err
	}
	var out Deployment
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		st, err := s.lockExec(ctx, tx, id, expected, "pause", DeploymentRunning)
		if err != nil {
			return err
		}
		out, err = s.pauseAndHalt(ctx, tx, c, st, "paused", ReasonManualPause, false)
		return err
	})
	return out, err
}

// HaltDeployment is the kill switch of a person: the Deployment is paused and its active ring halted immediately.
// A halted ring is resumed with ResumeRing. reason is one of HaltReasons.
func (s *Service) HaltDeployment(ctx context.Context, c Caller, p Principal, id string, expected *int, reason string) (Deployment, error) {
	if err := s.execPreamble(c, p, expected, id); err != nil {
		return Deployment{}, err
	}
	if !slices.Contains(HaltReasons, reason) {
		return Deployment{}, invalid("reason must be one of %s", strings.Join(HaltReasons, ", "))
	}
	var out Deployment
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		st, err := s.lockExec(ctx, tx, id, expected, "halt", DeploymentRunning, DeploymentPaused)
		if err != nil {
			return err
		}
		out, err = s.pauseAndHalt(ctx, tx, c, st, "halted", reason, true)
		return err
	})
	return out, err
}

// ResumeDeployment resumes a paused Deployment whose rings are not halted (resume a halted ring with ResumeRing).
// The gates are re-checked; the write capability is required.
func (s *Service) ResumeDeployment(ctx context.Context, c Caller, p Principal, id string, expected *int) (Deployment, error) {
	if err := s.execPreamble(c, p, expected, id); err != nil {
		return Deployment{}, err
	}
	if err := s.writeGate(); err != nil {
		return Deployment{}, err
	}
	pre, err := s.store.Rings(ctx, strings.ToLower(id))
	if err != nil {
		return Deployment{}, err
	}
	windows, err := s.lookupWindows(ctx, pre)
	if err != nil {
		return Deployment{}, err
	}
	var out Deployment
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		st, err := s.lockExec(ctx, tx, id, expected, "resume", DeploymentPaused)
		if err != nil {
			return err
		}
		for _, r := range st.runs {
			if r.Status == RingHalted {
				return &GateError{Code: CodeRingHalted}
			}
			if code := clearGateCode(r); code != "" {
				return &GateError{Code: code}
			}
		}
		g, err := s.executionGate(ctx, tx, st.d, st.runs)
		if err != nil {
			return err
		}
		if g.code != "" {
			return &GateError{Code: g.code}
		}
		for _, r := range st.runs {
			if ring, ok := st.ring(r.RingID); ok && r.Status == RingActive && !windowOpen(ring, windows, s.now()) {
				return &GateError{Code: CodeWindowClosed}
			}
		}
		next := st.d
		next.Status, next.StatusReason = DeploymentRunning, nil
		out, err = s.commitDeployment(ctx, tx, c, st.d, next, "resumed", "", nil)
		return err
	})
	return out, err
}

// clearGateCode is the refusal of a resume while the assignment of a ring is queued for clearing (clear_pending) or was
// cleared (assignment_cleared: the rollout was pulled back and is cancelled, not resumed); "" when nothing was queued.
func clearGateCode(r DeploymentRingRun) string {
	switch {
	case r.AssignmentClearedAt != nil:
		return CodeAssignmentCleared
	case r.ClearRequestedAt != nil:
		return CodeClearPending
	}
	return ""
}

func (s *Service) ringOp(c Caller, p Principal, id, ringID string, expected *int) error {
	if err := s.execPreamble(c, p, expected, id); err != nil {
		return err
	}
	if !validUUID(ringID) {
		return ErrNotFound
	}
	return nil
}

// HaltRing halts an active or awaiting ring (reason one of HaltReasons); a running Deployment pauses with reason
// ring_halted.
func (s *Service) HaltRing(ctx context.Context, c Caller, p Principal, id, ringID string, expected *int, reason string) (Deployment, error) {
	if err := s.ringOp(c, p, id, ringID, expected); err != nil {
		return Deployment{}, err
	}
	if !slices.Contains(HaltReasons, reason) {
		return Deployment{}, invalid("reason must be one of %s", strings.Join(HaltReasons, ", "))
	}
	var out Deployment
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		st, err := s.lockExec(ctx, tx, id, expected, "halt_ring", DeploymentRunning, DeploymentPaused)
		if err != nil {
			return err
		}
		run, _, ok := st.run(strings.ToLower(ringID))
		if !ok {
			return ErrNotFound
		}
		if run.Status != RingActive && run.Status != RingAwaitingPromotion {
			return &InvalidTransitionError{Operation: "halt_ring", From: run.Status}
		}
		if _, err := s.haltRun(ctx, tx, c, run, "halted", reason); err != nil {
			return err
		}
		out = st.d
		if st.d.Status == DeploymentRunning {
			next := st.d
			why := ReasonRingHalted
			next.Status, next.StatusReason = DeploymentPaused, &why
			out, err = s.commitDeployment(ctx, tx, c, st.d, next, "paused", why, map[string]any{"ringId": run.RingID})
		}
		return err
	})
	return out, err
}

// ReasonRingHalted is the pause reason of a Deployment whose ring was halted.
const ReasonRingHalted = "ring_halted"

// ResumeRing resumes a halted ring: the gates are re-checked, the ring's Change window must be open (rings without
// window exemption) and the Deployment returns to running when it was paused. A ring halted with assignment_failed is
// resumed only with reason "retry", which resets the attempt counter (at most MaxRingRetries times, audited). A rollout
// whose assignments were queued for clearing (kill switch) cannot be resumed. The person who planned a high-impact plan
// cannot resume it. Needs the write capability.
func (s *Service) ResumeRing(ctx context.Context, c Caller, p Principal, id, ringID string, expected *int, reason string) (Deployment, error) {
	if err := s.ringOp(c, p, id, ringID, expected); err != nil {
		return Deployment{}, err
	}
	if reason != "" && reason != ReasonRetry {
		return Deployment{}, invalid("reason must be empty or %s", ReasonRetry)
	}
	if err := s.writeGate(); err != nil {
		return Deployment{}, err
	}
	pre, err := s.store.Rings(ctx, strings.ToLower(id))
	if err != nil {
		return Deployment{}, err
	}
	windows, err := s.lookupWindows(ctx, pre)
	if err != nil {
		return Deployment{}, err
	}
	var out Deployment
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		st, err := s.lockExec(ctx, tx, id, expected, "resume_ring", DeploymentRunning, DeploymentPaused)
		if err != nil {
			return err
		}
		run, _, ok := st.run(strings.ToLower(ringID))
		if !ok {
			return ErrNotFound
		}
		if run.Status != RingHalted {
			return &GateError{Code: CodeRingNotHalted}
		}
		if st.d.HighImpact && slices.Contains(planners(st.d), c.Actor.UserID) {
			return ErrSeparationOfPlanning
		}
		for _, r := range st.runs {
			if code := clearGateCode(r); code != "" {
				return &GateError{Code: code}
			}
		}
		ring, _ := st.ring(run.RingID)
		g, err := s.executionGate(ctx, tx, st.d, st.runs)
		if err != nil {
			return err
		}
		if g.code != "" {
			return &GateError{Code: g.code}
		}
		if !windowOpen(ring, windows, s.now()) {
			return &GateError{Code: CodeWindowClosed}
		}
		for _, other := range st.runs {
			if other.ID != run.ID && other.Status == RingHalted {
				return &GateError{Code: CodeRingHalted}
			}
		}
		next := run
		now := s.now()
		next.Status, next.StatusReason, next.HaltedAt, next.AwaitingSince = RingActive, nil, nil, nil
		if next.ActivatedAt == nil {
			next.ActivatedAt = &now
		}
		op, meta := "resumed", map[string]any(nil)
		if run.StatusReason != nil && *run.StatusReason == ReasonAssignmentFailed {
			switch {
			case reason != ReasonRetry:
				return &GateError{Code: CodeRetryRequired}
			case run.RetryCount >= MaxRingRetries:
				return &GateError{Code: CodeRetryLimit}
			}
			n, _, err := s.store.AttemptStateTx(ctx, tx, run.ID, AttemptSet)
			if err != nil {
				return err
			}
			next.AttemptBase, next.RetryCount = n, run.RetryCount+1
			op, meta = "retried", map[string]any{"retry": next.RetryCount, "attemptBase": n}
		} else if reason == ReasonRetry {
			return invalid("only a ring halted with %s is retried", ReasonAssignmentFailed)
		}
		if _, err := s.commitRun(ctx, tx, c, run, next, op, reason, meta); err != nil {
			return err
		}
		out = st.d
		if st.d.Status == DeploymentPaused {
			d := st.d
			d.Status, d.StatusReason = DeploymentRunning, nil
			out, err = s.commitDeployment(ctx, tx, c, st.d, d, "resumed", "", map[string]any{"ringId": run.RingID})
		}
		return err
	})
	return out, err
}

// PromoteRing promotes a ring that waits for promotion: the next ring becomes active. Gates: the ring is awaiting
// promotion (every target decided, soak elapsed, success threshold met on evidence fresher than the freshness
// window), the optional promotion Approval is approved, the gates of the package still hold and the next ring's
// Change window is open. Promoting a high-impact plan needs deployments.high_impact; the write capability is
// required. The last ring is not promoted by a person: the engine completes the Deployment.
func (s *Service) PromoteRing(ctx context.Context, c Caller, p Principal, id, ringID string, expected *int) (Deployment, error) {
	if err := s.ringOp(c, p, id, ringID, expected); err != nil {
		return Deployment{}, err
	}
	if err := s.writeGate(); err != nil {
		return Deployment{}, err
	}
	pre, err := s.store.Rings(ctx, strings.ToLower(id))
	if err != nil {
		return Deployment{}, err
	}
	windows, err := s.lookupWindows(ctx, pre)
	if err != nil {
		return Deployment{}, err
	}
	var out Deployment
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		st, err := s.lockExec(ctx, tx, id, expected, "promote_ring", DeploymentRunning)
		if err != nil {
			return err
		}
		if st.d.HighImpact && !p.DeploymentsHighImpact {
			return ErrHighImpactForbidden
		}
		if st.d.HighImpact && slices.Contains(planners(st.d), c.Actor.UserID) {
			return ErrSeparationOfPlanning
		}
		run, idx, ok := st.run(strings.ToLower(ringID))
		if !ok {
			return ErrNotFound
		}
		if run.Status != RingAwaitingPromotion {
			return &GateError{Code: CodeRingNotAwaiting}
		}
		if idx+1 >= len(st.runs) {
			return &GateError{Code: CodeNoPreviousRing}
		}
		ring, _ := st.ring(run.RingID)
		g, err := s.executionGate(ctx, tx, st.d, st.runs)
		if err != nil {
			return err
		}
		if g.code != "" {
			return &GateError{Code: g.code}
		}
		now := s.now()
		if code, err := s.promotionEvidence(ctx, tx, run, ring, g, now); err != nil || code != "" {
			if err != nil {
				return err
			}
			return &GateError{Code: code}
		}
		if ring.ApprovalRequired && (run.PromotionApprovalStatus == nil || *run.PromotionApprovalStatus != approvalApproved) {
			return &GateError{Code: CodePromotionApproval}
		}
		nextRun := st.runs[idx+1]
		nextRing, _ := st.ring(nextRun.RingID)
		if !windowOpen(nextRing, windows, now) {
			return &GateError{Code: CodeWindowClosed}
		}
		promoted := run
		by := c.Actor.UserID
		promoted.Status, promoted.PromotedAt, promoted.PromotedBy = RingPromoted, &now, &by
		if _, err := s.commitRun(ctx, tx, c, run, promoted, "promoted", "", nil); err != nil {
			return err
		}
		if err := publish(ctx, tx, c, EventRingPromoted, map[string]any{"deploymentId": st.d.ID, "ringId": run.RingID, "ringRunId": run.ID, "position": run.Position}); err != nil {
			return err
		}
		if err := s.activateRun(ctx, tx, c, nextRun, "activated"); err != nil {
			return err
		}
		out, err = s.store.DeploymentTx(ctx, tx, st.d.ID)
		return err
	})
	return out, err
}

func (s *Service) activateRun(ctx context.Context, tx pgx.Tx, c Caller, r DeploymentRingRun, op string) error {
	next := r
	now := s.now()
	next.Status, next.ActivatedAt = RingActive, &now
	if _, err := s.commitRun(ctx, tx, c, r, next, op, "", nil); err != nil {
		return err
	}
	return publish(ctx, tx, c, EventRingActivated, map[string]any{"deploymentId": r.DeploymentID, "ringId": r.RingID, "ringRunId": r.ID, "position": r.Position})
}

// promotionEvidence checks the soak and the success threshold on fresh evidence; it returns a gate code when a
// condition does not hold.
func (s *Service) promotionEvidence(ctx context.Context, tx pgx.Tx, run DeploymentRingRun, ring DeploymentRing, g gateResult, now time.Time) (string, error) {
	art := ""
	if g.pkg.ArtifactID != nil {
		art = *g.pkg.ArtifactID
	}
	counts, err := s.store.RingCountsTx(ctx, tx, run.ID, art, now.Add(-s.evidenceFresh))
	if err != nil {
		return "", err
	}
	return evidenceCode(run, ring, counts, now), nil
}

// evidenceCode evaluates the promotion conditions on the counts; it returns "" when they hold.
func evidenceCode(run DeploymentRingRun, ring DeploymentRing, c RingCounts, now time.Time) string {
	if c.Open() > 0 || run.SettledAt == nil {
		return CodeRingNotAwaiting
	}
	if now.Before(run.SettledAt.Add(time.Duration(ring.SoakMinutes) * time.Minute)) {
		return CodeSoakNotElapsed
	}
	den := c.Denominator()
	if den < c.MinEvidence() {
		return CodeNoEvidence
	}
	if c.ByState[TargetSuccessful]*100 < ring.SuccessThresholdPercent*den {
		return CodeThresholdNotMet
	}
	if c.FreshSuccessful*100 < ring.SuccessThresholdPercent*den {
		return CodeEvidenceNotFresh
	}
	if ring.MinFreshEvidencePercent != nil && c.FreshObserved*100 < *ring.MinFreshEvidencePercent*den {
		return CodeEvidenceNotFresh
	}
	return ""
}

// RequestRingApproval asks for the promotion Approval of a ring that waits for promotion and needs one
// (approvalRequired). The approver must hold deployments.approve and not have planned, started or requested.
func (s *Service) RequestRingApproval(ctx context.Context, c Caller, p Principal, id, ringID string, expected *int, approver Approver) (Deployment, error) {
	if err := s.ringOp(c, p, id, ringID, expected); err != nil {
		return Deployment{}, err
	}
	approver, err := normalizeApprover(approver)
	if err != nil {
		return Deployment{}, err
	}
	var out Deployment
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		st, err := s.lockExec(ctx, tx, id, expected, "request_ring_approval", DeploymentRunning, DeploymentPaused)
		if err != nil {
			return err
		}
		run, _, ok := st.run(strings.ToLower(ringID))
		if !ok {
			return ErrNotFound
		}
		ring, _ := st.ring(run.RingID)
		if !ring.ApprovalRequired || run.Status != RingAwaitingPromotion {
			return &InvalidTransitionError{Operation: "request_ring_approval", From: run.Status}
		}
		if run.PromotionApprovalStatus != nil && *run.PromotionApprovalStatus != approvalRejected {
			return &InvalidTransitionError{Operation: "request_ring_approval", From: "approval_" + *run.PromotionApprovalStatus}
		}
		sets, err := s.store.ShareTargetSetsTx(ctx, tx, ringSetIDs(st.rings))
		if err != nil {
			return err
		}
		extra := []string{c.Actor.UserID}
		if st.d.StartedBy != nil {
			extra = append(extra, *st.d.StartedBy)
		}
		excluded := planExclusions(st.d, sets, extra...)
		if err := s.checkApprover(ctx, approver, excluded); err != nil {
			return err
		}
		aid, err := s.approvals.RequestRingInTx(ctx, tx, c.Actor, c.CorrelationID, run.ID, st.d.Reference+" ring "+fmt.Sprint(run.Position), approver, excluded)
		if err != nil {
			return err
		}
		next := run
		status, by := approvalPending, c.Actor.UserID
		next.PromotionApprovalID, next.PromotionApprovalStatus, next.PromotionApprovalRequestedBy = &aid, &status, &by
		if _, err := s.commitRun(ctx, tx, c, run, next, "approval_requested", "", map[string]any{"approvalId": aid}); err != nil {
			return err
		}
		out = st.d
		return nil
	})
	return out, err
}

// onRingApprovalDecided applies the decision of a ring promotion Approval; it is idempotent and verifies the
// decision through the Approvals contract (status, decider holds deployments.approve and did not plan or start).
func (s *Service) onRingApprovalDecided(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent, runID, approvalID, decision string) error {
	if !validUUID(runID) {
		return events.Permanent(errors.New("approval decision names an invalid ring run id"))
	}
	pre, err := s.store.RingRunByIDTx(ctx, tx, strings.ToLower(runID))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	st, err := s.lockExec(ctx, tx, pre.DeploymentID, nil, "")
	if err != nil {
		return err
	}
	run, _, ok := st.run(pre.RingID)
	if !ok || run.PromotionApprovalID == nil || !strings.EqualFold(*run.PromotionApprovalID, approvalID) ||
		run.PromotionApprovalStatus == nil || *run.PromotionApprovalStatus != approvalPending {
		return nil
	}
	c := engineCaller(ev.CorrelationID)
	list, err := s.approvals.RingForSubject(ctx, run.ID)
	if err != nil {
		return err
	}
	want := approvalApproved
	if decision == "reject" {
		want = approvalRejected
	}
	refused := "approval_unknown"
	decider := ""
	for _, a := range list {
		if !strings.EqualFold(a.ID, approvalID) {
			continue
		}
		switch {
		case a.Status != want:
			refused = "status_mismatch"
		case a.DecidedByUserID == nil:
			refused = "no_decider"
		default:
			decider = *a.DecidedByUserID
			refused = ""
		}
	}
	if refused == "" {
		sets, err := s.store.ShareTargetSetsTx(ctx, tx, ringSetIDs(st.rings))
		if err != nil {
			return err
		}
		extra := []string{}
		if st.d.StartedBy != nil {
			extra = append(extra, *st.d.StartedBy)
		}
		if run.PromotionApprovalRequestedBy != nil {
			extra = append(extra, *run.PromotionApprovalRequestedBy)
		}
		if slices.Contains(planExclusions(st.d, sets, extra...), decider) {
			refused = "decider_excluded"
		} else if ok, err := s.holdsApprove(ctx, decider); err != nil {
			return err
		} else if !ok {
			refused = "decider_lacks_permission"
		}
	}
	next := run
	status := approvalApproved
	op := "approval_approved"
	if decision == "reject" {
		status, op = approvalRejected, "approval_rejected"
	}
	meta := map[string]any{"approvalId": approvalID, "decision": decision}
	if refused != "" {
		meta["refusal"] = refused
		if err := s.approvals.CancelRingBySubjectInTx(ctx, tx, c.Actor, c.CorrelationID, run.ID); err != nil {
			return err
		}
		status, op = approvalRejected, "approver_not_authorized"
	} else {
		meta["decidedBy"] = decider
	}
	next.PromotionApprovalStatus = &status
	_, err = s.commitRun(ctx, tx, c, run, next, op, "", meta)
	return err
}

// cancelRunning cancels a Deployment in an execution status: targets without a final state are cancelled, active
// and awaiting rings halted (deployment_cancelled); the clearing of every ring assignment the provider may hold (a set
// attempt exists, also one still in flight) is queued and done by the engine (idempotent, needs the write capability).
func (s *Service) cancelRunning(ctx context.Context, tx pgx.Tx, c Caller, st execState, reason string) (Deployment, error) {
	now := s.now()
	n, err := s.store.CancelTargetsTx(ctx, tx, st.d.ID, now, c.CorrelationID)
	if err != nil {
		return Deployment{}, err
	}
	for _, r := range st.runs {
		if r.Status == RingActive || r.Status == RingAwaitingPromotion {
			if _, err := s.haltRun(ctx, tx, c, r, "cancelled", ReasonDeploymentCancel); err != nil {
				return Deployment{}, err
			}
		}
	}
	if err := s.queueClear(ctx, tx, c, st.d.ID, reason); err != nil {
		return Deployment{}, err
	}
	next := st.d
	by := c.Actor.UserID
	var byPtr *string
	if by != "" {
		byPtr = &by
	}
	next.Status, next.StatusReason, next.CancelledBy, next.CancelledAt = DeploymentCancelled, &reason, byPtr, &now
	out, err := s.commitDeployment(ctx, tx, c, st.d, next, "cancelled", reason, map[string]any{"cancelledTargets": n, "previousStatus": st.d.Status})
	if err != nil {
		return Deployment{}, err
	}
	return out, publish(ctx, tx, c, EventDeploymentCancelled, map[string]any{"deploymentId": st.d.ID, "reason": reason, "previousStatus": st.d.Status})
}

// ---- reads ----

// deploymentForRead applies the plan read rule: deployments read access, owner, creator and approvers.
func (s *Service) deploymentForRead(ctx context.Context, p Principal, id string) (Deployment, error) {
	if !validUUID(id) {
		return Deployment{}, ErrNotFound
	}
	d, err := s.store.GetDeployment(ctx, strings.ToLower(id))
	if err != nil {
		return Deployment{}, err
	}
	if p.canViewDeployments() {
		return d, nil
	}
	if p.UserID != "" && (p.UserID == d.OwnerUserID || p.UserID == d.CreatedBy) {
		return d, nil
	}
	if p.UserID != "" {
		ok, err := s.isPlanApprover(ctx, d.ID, p.UserID)
		if err != nil {
			return Deployment{}, err
		}
		if ok {
			return d, nil
		}
	}
	return Deployment{}, ErrNotFound
}

func ringRate(c RingCounts) *float64 {
	den := c.Denominator()
	if den <= 0 {
		return nil
	}
	v := float64(c.FreshSuccessful) * 100 / float64(den)
	return &v
}

// DeploymentProgress returns the progress of every ring: counts by target state, the success rate on fresh
// evidence, the soak time left and the next gate.
func (s *Service) DeploymentProgress(ctx context.Context, p Principal, id string) (DeploymentProgress, error) {
	d, err := s.deploymentForRead(ctx, p, id)
	if err != nil {
		return DeploymentProgress{}, err
	}
	out := DeploymentProgress{Deployment: d, Rings: []RingProgress{}}
	out.ResolvingStuck = d.Status == DeploymentResolvingTargets && d.StartedAt != nil && s.now().Sub(*d.StartedAt) > ResolvingStuckAfter
	rings, err := s.store.Rings(ctx, d.ID)
	if err != nil {
		return DeploymentProgress{}, err
	}
	runs, err := s.store.RingRuns(ctx, d.ID)
	if err != nil {
		return DeploymentProgress{}, err
	}
	art := ""
	clearAttempts := map[string]int{}
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		pkg, ok, err := s.store.PublishedPackageTx(ctx, tx, d.SoftwareVersionID)
		if err == nil && ok && pkg.ArtifactID != nil {
			art = *pkg.ArtifactID
		}
		if err != nil {
			return err
		}
		for _, r := range runs {
			if needsClear(r) {
				n, _, err := s.store.AttemptStateTx(ctx, tx, r.ID, AttemptClear)
				if err != nil {
					return err
				}
				clearAttempts[r.ID] = n
			}
		}
		return nil
	})
	if err != nil {
		return DeploymentProgress{}, err
	}
	now := s.now()
	for i, run := range runs {
		ring, ok := findRing(rings, run.RingID)
		if !ok {
			continue
		}
		runArt := art
		if run.ManagementArtifactID != nil {
			runArt = *run.ManagementArtifactID
		}
		counts, err := s.store.RingCounts(ctx, run.ID, runArt, now.Add(-s.evidenceFresh))
		if err != nil {
			return DeploymentProgress{}, err
		}
		rp := RingProgress{Run: run, Ring: ring, Counts: counts, Rate: ringRate(counts),
			ClearPending: needsClear(run), ClearFailed: needsClear(run) && clearAttempts[run.ID] >= MaxClearAttempts}
		out.ClearPending = out.ClearPending || rp.ClearPending
		if run.SettledAt != nil {
			if left := run.SettledAt.Add(time.Duration(ring.SoakMinutes) * time.Minute).Sub(now); left > 0 {
				rp.SoakLeft = left
			}
		}
		rp.NextGate = nextGate(run, ring, counts, i+1 < len(runs), now)
		out.Rings = append(out.Rings, rp)
	}
	return out, nil
}

func nextGate(run DeploymentRingRun, ring DeploymentRing, c RingCounts, hasNext bool, now time.Time) string {
	switch run.Status {
	case RingPending:
		return "previous_ring"
	case RingHalted:
		return "resume"
	case RingPromoted:
		return "none"
	case RingActive:
		if c.Open() > 0 {
			return "observations"
		}
		switch evidenceCode(run, ring, c, now) {
		case CodeSoakNotElapsed:
			return "soak"
		case CodeThresholdNotMet:
			return "threshold"
		case CodeEvidenceNotFresh:
			return "fresh_evidence"
		case CodeNoEvidence:
			return "evidence"
		}
		return "observations"
	}
	if !hasNext {
		return "completion"
	}
	if ring.ApprovalRequired && (run.PromotionApprovalStatus == nil || *run.PromotionApprovalStatus != approvalApproved) {
		return "approval"
	}
	return "promotion"
}

// ListRingTargets lists the targets of a ring (keyset, optional state filter). Device names need endpoints.view.
func (s *Service) ListRingTargets(ctx context.Context, p Principal, id, ringID, state string, page Page) (TargetResult, error) {
	d, err := s.deploymentForRead(ctx, p, id)
	if err != nil {
		return TargetResult{}, err
	}
	if state != "" && !slices.Contains(TargetStates, state) {
		return TargetResult{}, invalid("state must be one of %s", strings.Join(TargetStates, ", "))
	}
	if !validUUID(ringID) {
		return TargetResult{}, ErrNotFound
	}
	runs, err := s.store.RingRuns(ctx, d.ID)
	if err != nil {
		return TargetResult{}, err
	}
	var runID string
	for _, r := range runs {
		if r.RingID == strings.ToLower(ringID) || r.ID == strings.ToLower(ringID) {
			runID = r.ID
		}
	}
	if runID == "" {
		return TargetResult{}, ErrNotFound
	}
	res, err := s.store.ListTargets(ctx, runID, TargetFilter{State: state, Page: page.Normalize()})
	if err != nil {
		return TargetResult{}, err
	}
	if !p.canView() {
		res.NamesRedacted = true
		for i := range res.Items {
			res.Items[i].DeviceName = ""
		}
	}
	return res, nil
}

// ListDeploymentAttempts lists the immutable provider write attempts of a Deployment.
func (s *Service) ListDeploymentAttempts(ctx context.Context, p Principal, id string, page Page) (AttemptResult, error) {
	d, err := s.deploymentForRead(ctx, p, id)
	if err != nil {
		return AttemptResult{}, err
	}
	return s.store.ListAttempts(ctx, d.ID, page.Normalize())
}
