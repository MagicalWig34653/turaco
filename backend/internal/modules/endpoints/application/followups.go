package application

import (
	"context"
	"fmt"
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
// the context type deployment; an inactive assignee leaves the Task unassigned).
type FollowUpTasks interface {
	CreateInTx(ctx context.Context, tx pgx.Tx, in FollowUpTask) (string, error)
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

// followUp creates the missing follow-up Tasks of a Deployment in the caller's transaction (the advisory lock of
// the correlation serializes the check and the insert).
func (s *Service) followUp(ctx context.Context, tx pgx.Tx, c Caller, d Deployment, cluster bool) error {
	if s.followTasks == nil {
		return nil
	}
	var cands []followCandidate
	if cluster {
		cands = append(cands, followCandidate{reason: FollowUpReasonCluster})
	}
	halted, err := s.store.EngineHaltedRingsTx(ctx, tx, d.ID)
	if err != nil {
		return err
	}
	for _, h := range halted {
		if attentionReason(h.Reason) {
			ring := h.RingID
			cands = append(cands, followCandidate{reason: h.Reason, ringID: &ring})
		}
	}
	if (d.Status == DeploymentPaused || d.Status == DeploymentFailed) && d.StatusReason != nil && attentionReason(*d.StatusReason) && len(halted) == 0 {
		cands = append(cands, followCandidate{reason: *d.StatusReason})
	}
	for _, cand := range cands {
		exists, err := s.store.FollowupExistsTx(ctx, tx, d.ID, cand.reason, cand.ringID)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		taskID, err := s.followTasks.CreateInTx(ctx, tx, FollowUpTask{Actor: c.Actor, CorrelationID: c.CorrelationID, DeploymentID: d.ID,
			Title: followUpTitle(d.Reference, cand.reason), DueAt: s.now().Add(FollowUpDueAfter), AssignedUserID: d.OwnerUserID})
		if err != nil {
			return fmt.Errorf("create follow-up task: %w", err)
		}
		if err := s.store.InsertFollowupTx(ctx, tx, d.ID, cand.reason, cand.ringID, taskID); err != nil {
			return err
		}
		meta := map[string]any{"reason": cand.reason, "taskId": taskID}
		if cand.ringID != nil {
			meta["ringId"] = *cand.ringID
		}
		if err := audit.Record(ctx, tx, audit.Change{Action: "endpoints.deployment.followup_created", TargetType: "deployment", TargetID: d.ID,
			Actor: c.Actor, CorrelationID: c.CorrelationID, Metadata: meta}); err != nil {
			return err
		}
		if s.followNotes == nil {
			continue
		}
		ringKey := ""
		if cand.ringID != nil {
			ringKey = *cand.ringID
		}
		_, err = s.followNotes.Create(ctx, tx, notifications.Intent{RecipientUserID: d.OwnerUserID, Category: DeploymentAttentionCategory,
			Params: map[string]any{"title": d.Reference}, LinkType: FollowUpTaskContext, LinkID: d.ID,
			DedupeKey: fmt.Sprintf("deployment-attention:%s:%s:%s:%s", d.ID, cand.reason, ringKey, d.OwnerUserID)})
		if err != nil {
			return fmt.Errorf("notify deployment owner: %w", err)
		}
	}
	return nil
}
