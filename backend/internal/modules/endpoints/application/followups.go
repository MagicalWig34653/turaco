package application

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// Work follow-up (F9 G4). When the engine halts a ring, pauses or fails a Deployment for a gate reason, or the
// correlation finds a failure cluster, ONE Task (context type deployment) is created for the Deployment owner, due
// FollowUpDueAfter later, and the owner is notified (category deployment.attention, reference only). The Task has a
// fixed, reference-based title and no free text. A follow-up is created at most once per Deployment, reason code and
// ring (deployment_followups); when its Task was completed or cancelled and the same condition recurs, no new Task is
// created (a person decides about a new rollout plan). The per-Deployment flag create_tasks switches the Tasks and the
// notification off. Tickets are not created: Service Desk has no contract for other modules to create them.
//
// Flood control: ring-level halt reasons (thresholds, assignment, evidence, not-applicable share) give one Task per
// ring; every other reason (gates, kill switches, window, artifact changes) gives one Task per Deployment. One run
// creates at most MaxFollowUpsPerRun new Tasks. The owner is assigned and notified only when active with deployments
// read access; otherwise the Task is created unassigned, nobody is notified, and the follow-up is counted as
// unassigned in the briefing health.
const (
	// DeploymentAttentionCategory tells the owner of a Deployment that it needs a decision.
	DeploymentAttentionCategory = "deployment.attention"
	// FollowUpTaskContext is the Task context type of follow-up Tasks (the Deployment id).
	FollowUpTaskContext = "deployment"
	FollowUpDueAfter    = 2 * 24 * time.Hour
	// FollowUpReasonCluster is the follow-up reason of a failure cluster.
	FollowUpReasonCluster = "failure_cluster"
)

// FollowUpTask is a Task created through the Tasks contract for a Deployment.
type FollowUpTask struct {
	Actor          audit.Actor
	CorrelationID  string
	DeploymentID   string
	Title          string
	DueAt          time.Time
	AssignedUserID string
}

// FollowUpTasks creates the follow-up Task in the caller's transaction (adapter over tasks/public, which allow-lists
// the context type deployment; an inactive assignee leaves the Task unassigned, which assigned reports).
type FollowUpTasks interface {
	CreateInTx(ctx context.Context, tx pgx.Tx, in FollowUpTask) (taskID string, assigned bool, err error)
}

// FollowUpNotifier creates notifications inside the caller's transaction.
type FollowUpNotifier interface {
	Create(ctx context.Context, tx pgx.Tx, in notifications.Intent) (bool, error)
}

// WithFollowUps connects the Tasks contract and the notification service to the correlation job. Without it no
// follow-up is created.
func (s *Service) WithFollowUps(tasks FollowUpTasks, notifier FollowUpNotifier) *Service {
	s.followTasks, s.followNotes = tasks, notifier
	return s
}

// WithFollowUpCap lowers the number of new follow-up Tasks one correlation run creates (tests; default and maximum
// MaxFollowUpsPerRun).
func (s *Service) WithFollowUpCap(n int) *Service {
	if n > 0 && n < MaxFollowUpsPerRun {
		s.followCap = n
	}
	return s
}

func (s *Service) newFollowBudget() *followBudget {
	if s.followCap > 0 {
		return &followBudget{left: s.followCap}
	}
	return &followBudget{left: MaxFollowUpsPerRun}
}

// followUpTitles are the fixed title phrases per reason code; anything else uses the generic one.
var followUpTitles = map[string]string{
	FollowUpReasonCluster:   "failures cluster, review the cause",
	ReasonFailureThreshold:  "ring halted, too many failures",
	ReasonThresholdNotMet:   "ring halted, success threshold not met",
	ReasonAssignmentFailed:  "ring halted, assignment failed",
	ReasonNoEvidence:        "ring halted, no evidence",
	ReasonNARatio:           "ring halted, too many not applicable devices",
	ReasonWindowClosed:      "paused, maintenance window closed",
	ReasonVersionRevoked:    "stopped, version revoked",
	ReasonProductBlocked:    "stopped, product blocked",
	ReasonPackageGateClosed: "stopped, package gate closed",
	ReasonPackageNotPublish: "stopped, package not published",
	ReasonArtifactUnlinked:  "stopped, artifact not linked",
	ReasonArtifactChanged:   "stopped, artifact changed",
	ReasonHashMismatch:      "stopped, hash mismatch",
	ReasonNoTargets:         "failed, no targets",
	ReasonTooManyTargets:    "failed, too many targets",
	ReasonEvalIncomplete:    "failed, target evaluation incomplete",
	ReasonBecameHighImpact:  "failed, became high impact",
	ReasonTargetsGrew:       "failed, targets grew",
}

// followUpTitle is the Task title: the Deployment reference and a fixed phrase. No Deployment name, device or
// provider text goes into it.
func followUpTitle(reference, reason string) string {
	phrase, ok := followUpTitles[reason]
	if !ok {
		phrase = "needs attention"
	}
	return "Deployment " + reference + ": " + phrase
}

// attentionReason reports whether a pause or halt reason asks for follow-up: a person's own pause, halt or cancel
// does not, and the pause that only follows a ring halt is covered by the ring's follow-up.
func attentionReason(reason string) bool {
	switch reason {
	case "", ReasonManualPause, ReasonManualHalt, ReasonRingHalted, ReasonDeploymentCancel:
		return false
	}
	return true
}

type followCandidate struct {
	reason string
	ringID *string
}

// ringLevelReason reports whether a halt reason concerns the ring alone (its own numbers). Every other reason (gate
// closed, version revoked, window closed, artifact changed and so on) concerns the whole Deployment, however many
// rings it stops.
func ringLevelReason(reason string) bool {
	switch reason {
	case ReasonFailureThreshold, ReasonThresholdNotMet, ReasonAssignmentFailed, ReasonNoEvidence, ReasonNARatio:
		return true
	}
	return false
}

// followCandidates lists the follow-ups the Deployment's state calls for.
func (s *Service) followCandidates(ctx context.Context, d Deployment, cluster bool) ([]followCandidate, error) {
	var cands []followCandidate
	if cluster {
		cands = append(cands, followCandidate{reason: FollowUpReasonCluster})
	}
	var halted []HaltedRing
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		halted, err = s.store.EngineHaltedRingsTx(ctx, tx, d.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	add := func(c followCandidate) {
		for _, x := range cands {
			if x.reason == c.reason && (x.ringID == nil) == (c.ringID == nil) && (x.ringID == nil || *x.ringID == *c.ringID) {
				return
			}
		}
		cands = append(cands, c)
	}
	for _, h := range halted {
		if !attentionReason(h.Reason) {
			continue
		}
		if ringLevelReason(h.Reason) {
			ring := h.RingID
			add(followCandidate{reason: h.Reason, ringID: &ring})
		} else {
			add(followCandidate{reason: h.Reason})
		}
	}
	if (d.Status == DeploymentPaused || d.Status == DeploymentFailed) && d.StatusReason != nil && attentionReason(*d.StatusReason) && len(halted) == 0 {
		add(followCandidate{reason: *d.StatusReason})
	}
	return cands, nil
}

// followUp creates the missing follow-up Tasks of a Deployment, every one in its own transaction: a failure is logged
// and the next candidate and the next Deployment go on (the cluster findings are already committed).
func (s *Service) followUp(ctx context.Context, c Caller, d Deployment, cluster bool, budget *followBudget) {
	if s.followTasks == nil {
		return
	}
	cands, err := s.followCandidates(ctx, d, cluster)
	if err != nil {
		slog.Warn("deployment follow-up: read candidates", "deployment_id", d.ID, "error", err)
		return
	}
	for _, cand := range cands {
		if err := s.createFollowUp(ctx, c, d.ID, cand, budget); err != nil {
			slog.Warn("deployment follow-up: create", "deployment_id", d.ID, "reason", cand.reason, "error", err)
		}
	}
}

// ownerEligible reports whether the owner holds a deployments read permission (the Tasks contract checks that the
// assignee is active).
func (s *Service) ownerEligible(ctx context.Context, userID string) (bool, error) {
	if userID == "" {
		return false, nil
	}
	perms, err := s.approvers.Permissions(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, p := range []string{PermDeploymentsView, PermDeploymentsManage, PermDeploymentsExecute} {
		if _, ok := perms[p]; ok {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) createFollowUp(ctx context.Context, c Caller, deploymentID string, cand followCandidate, budget *followBudget) error {
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		// The Deployment lock serializes the check and the insert and the flag create_tasks.
		d, err := s.store.LockDeploymentTx(ctx, tx, deploymentID)
		if err != nil {
			return err
		}
		if !d.CreateTasks {
			return nil
		}
		exists, err := s.store.FollowupExistsTx(ctx, tx, d.ID, cand.reason, cand.ringID)
		if err != nil || exists {
			return err
		}
		if !budget.take() {
			return nil
		}
		eligible, err := s.ownerEligible(ctx, d.OwnerUserID)
		if err != nil {
			return err
		}
		in := FollowUpTask{Actor: c.Actor, CorrelationID: c.CorrelationID, DeploymentID: d.ID,
			Title: followUpTitle(d.Reference, cand.reason), DueAt: s.now().Add(FollowUpDueAfter)}
		if eligible {
			in.AssignedUserID = d.OwnerUserID
		}
		taskID, assigned, err := s.followTasks.CreateInTx(ctx, tx, in)
		if err != nil {
			return fmt.Errorf("create follow-up task: %w", err)
		}
		assigned = assigned && eligible
		if err := s.store.InsertFollowupTx(ctx, tx, d.ID, cand.reason, cand.ringID, taskID, !assigned); err != nil {
			return err
		}
		meta := map[string]any{"reason": cand.reason, "taskId": taskID, "assigned": assigned}
		ringKey := ""
		if cand.ringID != nil {
			meta["ringId"] = *cand.ringID
			ringKey = *cand.ringID
		}
		if err := audit.Record(ctx, tx, audit.Change{Action: "endpoints.deployment.followup_created", TargetType: "deployment", TargetID: d.ID,
			Actor: c.Actor, CorrelationID: c.CorrelationID, Metadata: meta}); err != nil {
			return err
		}
		if s.followNotes == nil || !assigned {
			return nil
		}
		_, err = s.followNotes.Create(ctx, tx, notifications.Intent{RecipientUserID: d.OwnerUserID, Category: DeploymentAttentionCategory,
			Params: map[string]any{"title": d.Reference}, LinkType: FollowUpTaskContext, LinkID: d.ID,
			DedupeKey: fmt.Sprintf("deployment-attention:%s:%s:%s:%s", d.ID, cand.reason, ringKey, d.OwnerUserID)})
		if err != nil {
			return fmt.Errorf("notify deployment owner: %w", err)
		}
		return nil
	})
}
