package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

var systemActor = audit.SystemActor("changes-workflow")

// Approver names who approves a Change: exactly one of User and Team.
type Approver struct {
	UserID *string
	TeamID *string
}

func (a Approver) set() bool { return a.UserID != nil || a.TeamID != nil }

// denyExecute is the refusal for a caller who may not run the Change: someone
// who cannot even read it gets ErrNotFound, never a hint that it exists.
func denyExecute(p Principal, c Change) error {
	if p.readsAll() || p.UserID != "" && p.UserID == c.RequesterID {
		return ErrForbidden
	}
	return ErrNotFound
}

// Submit sends a draft Change to assessment. It needs a maintenance window, a
// rollback plan for medium and high risk and, unless the Change is standard,
// at least one affected resource. Requires changes.manage.
func (s *Service) Submit(ctx context.Context, c Caller, p Principal, id string, expected *int) (Change, error) {
	if err := c.validate(); err != nil {
		return Change{}, err
	}
	if !p.Manage {
		return Change{}, ErrForbidden
	}
	var out Change
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockStatus(ctx, tx, id, expected, "submit", StatusDraft)
		if err != nil {
			return err
		}
		if cur.WindowStart == nil {
			return invalid("a maintenance window is required before a change can be submitted")
		}
		if cur.Risk != RiskLow && cur.RollbackPlan == nil {
			return invalid("a rollback plan is required for medium and high risk")
		}
		if cur.Kind != KindStandard {
			page, err := s.graph.Outgoing(ctx, tx, relationships.Node{Type: NodeChange, ID: cur.ID}, []string{RelAffects}, "", 1)
			if err != nil {
				return fmt.Errorf("count affected resources: %w", err)
			}
			if len(page.Items) == 0 {
				return invalid("at least one affected resource is required before a change can be submitted")
			}
		}
		next := addEditor(cur, c.Actor.UserID)
		next.Status, next.StatusReason = StatusAssessment, nil
		out, err = s.commit(ctx, tx, c, cur, next, "submitted", "", nil)
		if err != nil {
			return err
		}
		return publish(ctx, tx, c, "ChangeSubmitted", map[string]any{"changeId": out.ID, "kind": out.Kind, "risk": out.Risk})
	})
	return out, err
}

// Assessment is the outcome of the assessment step. Risk is the assessed risk.
// A Change that needs approval (medium or high risk, or an emergency change)
// needs exactly one Approver; an emergency change may instead carry an explicit
// EmergencyJustification and skip the approval. Everything else is approved
// directly and takes no Approver.
type Assessment struct {
	Risk                   string
	Approver               Approver
	EmergencyJustification string
}

// Assess completes the assessment: it fixes the risk and either requests the
// approval (the Change becomes pending_approval), approves the Change when no
// approval is required, or approves an emergency change on its justification
// (audited as the emergency path; the Change then needs a review before it can
// be closed). The requester, everybody who edited, submitted or assessed the
// Change can never approve it. Requires changes.manage.
func (s *Service) Assess(ctx context.Context, c Caller, p Principal, id string, expected *int, in Assessment) (Change, error) {
	if err := c.validate(); err != nil {
		return Change{}, err
	}
	if !p.Manage {
		return Change{}, ErrForbidden
	}
	if !oneOf(in.Risk, Risks) {
		return Change{}, invalid("risk must be one of %s", strings.Join(Risks, ", "))
	}
	if in.Approver.UserID != nil && in.Approver.TeamID != nil {
		return Change{}, invalid("name either an approver user or an approver team")
	}
	for _, a := range []*string{in.Approver.UserID, in.Approver.TeamID} {
		if a != nil {
			if _, err := checkID(*a); err != nil {
				return Change{}, err
			}
		}
	}
	justification, err := cleanText("emergency justification", in.EmergencyJustification, maxJustify)
	if err != nil {
		return Change{}, err
	}
	var out Change
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockStatus(ctx, tx, id, expected, "assess", StatusAssessment)
		if err != nil {
			return err
		}
		if in.Risk != RiskLow && cur.RollbackPlan == nil {
			return invalid("a rollback plan is required for medium and high risk")
		}
		next := addEditor(cur, c.Actor.UserID)
		next.Risk = in.Risk
		needsApproval := in.Risk != RiskLow || cur.Kind == KindEmergency
		switch {
		case justification != nil:
			if cur.Kind != KindEmergency {
				return invalid("only an emergency change can be approved on a justification")
			}
			if in.Approver.set() {
				return invalid("an emergency justification replaces the approver")
			}
			next.Status, next.StatusReason, next.EmergencyJustification = StatusApproved, strPtr(reasonEmergency), justification
			out, err = s.commit(ctx, tx, c, cur, next, "emergency_approved", reasonEmergency, nil)
			if err != nil {
				return err
			}
			return publish(ctx, tx, c, "ChangeApproved", map[string]any{"changeId": out.ID, "emergency": true})
		case needsApproval:
			if in.Approver.UserID == nil == (in.Approver.TeamID == nil) {
				return invalid("exactly one approver (user or team) is required")
			}
			excluded := append([]string{cur.RequesterID}, next.Editors...)
			slices.Sort(excluded)
			excluded = slices.Compact(excluded)
			next.Status, next.StatusReason = StatusPendingApproval, nil
			approvalID, err := s.approvals.RequestInTx(ctx, tx, c.Actor, c.CorrelationID, ApprovalRequest{
				SubjectID: cur.ID, Label: cur.Reference, ApproverUserID: in.Approver.UserID, ApproverTeamID: in.Approver.TeamID, ExcludedUserIDs: excluded,
			})
			if err != nil {
				return err
			}
			next.ApprovalID = &approvalID
			out, err = s.commit(ctx, tx, c, cur, next, "assessed", "", nil)
			return err
		default:
			if in.Approver.set() {
				return invalid("this change needs no approval, so no approver may be named")
			}
			next.Status, next.StatusReason = StatusApproved, nil
			out, err = s.commit(ctx, tx, c, cur, next, "assessed", "", map[string]any{"approval": "not_required"})
			if err != nil {
				return err
			}
			return publish(ctx, tx, c, "ChangeApproved", map[string]any{"changeId": out.ID, "emergency": false})
		}
	})
	return out, err
}

// OnApprovalDecided moves a Change whose approval was decided: an approval
// makes it `approved`, a rejection makes it `rejected` (terminal; the reason
// `approval_rejected` is stored and the approver's comment stays with the
// approval). It runs in the dispatcher's claim transaction and is idempotent:
// events of other subjects and stale events (the Change is no longer pending
// approval) change nothing.
func (s *Service) OnApprovalDecided(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		ApprovalID  string `json:"approvalId"`
		SubjectType string `json:"subjectType"`
		SubjectID   string `json:"subjectId"`
		Decision    string `json:"decision"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return events.Permanent(fmt.Errorf("decode ApprovalDecided payload: %w", err))
	}
	if p.SubjectType != SubjectType {
		return nil
	}
	if p.Decision != "approve" && p.Decision != "reject" {
		return events.Permanent(fmt.Errorf("approval decision %q of change %s is unknown", p.Decision, p.SubjectID))
	}
	cur, err := s.store.LockTx(ctx, tx, strings.ToLower(p.SubjectID))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if cur.Status != StatusPendingApproval || cur.ApprovalID != nil && p.ApprovalID != "" && *cur.ApprovalID != p.ApprovalID {
		return nil
	}
	c := Caller{Actor: systemActor, CorrelationID: ev.CorrelationID}
	next := cur
	if p.Decision == "approve" {
		next.Status, next.StatusReason = StatusApproved, nil
		out, err := s.commit(ctx, tx, c, cur, next, "approved", "", nil)
		if err != nil {
			return err
		}
		return publish(ctx, tx, c, "ChangeApproved", map[string]any{"changeId": out.ID, "emergency": false})
	}
	next.Status, next.StatusReason = StatusRejected, strPtr(ReasonApprovalRejected)
	out, err := s.commit(ctx, tx, c, cur, next, "approval_rejected", ReasonApprovalRejected, nil)
	if err != nil {
		return err
	}
	return publish(ctx, tx, c, "ChangeRejected", map[string]any{"changeId": out.ID})
}

// ScheduleInput optionally replaces the maintenance window while scheduling
// (an approval can take longer than the window planned before it).
type ScheduleInput struct {
	Window *Window
}

// Schedule schedules an approved Change. It needs a maintenance window that
// starts in the future; an emergency change is exempt from that rule. Requires
// changes.manage.
func (s *Service) Schedule(ctx context.Context, c Caller, p Principal, id string, expected *int, in ScheduleInput) (Change, error) {
	if err := c.validate(); err != nil {
		return Change{}, err
	}
	if !p.Manage {
		return Change{}, ErrForbidden
	}
	var ws, we *time.Time
	if in.Window != nil {
		ws, we = utc(in.Window.Start), utc(in.Window.End)
		if ws == nil {
			return Change{}, invalid("a maintenance window needs a start and an end")
		}
		if err := checkWindow(ws, we); err != nil {
			return Change{}, err
		}
	}
	var out Change
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockStatus(ctx, tx, id, expected, "schedule", StatusApproved)
		if err != nil {
			return err
		}
		next := cur
		if in.Window != nil {
			next.WindowStart, next.WindowEnd = ws, we
		}
		if next.WindowStart == nil {
			return invalid("a maintenance window is required to schedule a change")
		}
		if cur.Kind != KindEmergency && !next.WindowStart.After(s.now()) {
			return invalid("the maintenance window must start in the future")
		}
		next.Status, next.StatusReason, next.RemindedFor = StatusScheduled, nil, nil
		out, err = s.commit(ctx, tx, c, cur, next, "scheduled", "", map[string]any{"windowChanged": in.Window != nil})
		if err != nil {
			return err
		}
		return publish(ctx, tx, c, "ChangeScheduled", map[string]any{"changeId": out.ID, "emergency": out.Kind == KindEmergency,
			"windowStart": out.WindowStart.Format(time.RFC3339), "windowEnd": out.WindowEnd.Format(time.RFC3339)})
	})
	return out, err
}

// Start begins the execution of a scheduled Change. Only the Change's owner or
// a holder of changes.execute may start it.
func (s *Service) Start(ctx context.Context, c Caller, p Principal, id string, expected *int) (Change, error) {
	if err := c.validate(); err != nil {
		return Change{}, err
	}
	var out Change
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockExecute(ctx, tx, p, id, expected, "start", StatusScheduled)
		if err != nil {
			return err
		}
		now := s.now()
		next := cur
		next.Status, next.StartedAt = StatusInProgress, &now
		out, err = s.commit(ctx, tx, c, cur, next, "started", "", nil)
		if err != nil {
			return err
		}
		return publish(ctx, tx, c, "ChangeStarted", map[string]any{"changeId": out.ID})
	})
	return out, err
}

// lockExecute locks a Change in one of the statuses for an execution operation
// and checks that the caller may run it.
func (s *Service) lockExecute(ctx context.Context, tx pgx.Tx, p Principal, id string, expected *int, op string, from ...string) (Change, error) {
	if !uuidPattern.MatchString(id) {
		return Change{}, ErrNotFound
	}
	cur, err := s.store.LockTx(ctx, tx, strings.ToLower(id))
	if err != nil {
		return Change{}, err
	}
	if !p.canExecute(cur) {
		return Change{}, denyExecute(p, cur)
	}
	if expected != nil && *expected != cur.Version {
		return Change{}, ErrVersionConflict
	}
	if !slices.Contains(from, cur.Status) {
		return Change{}, &InvalidTransitionError{Operation: op, From: cur.Status}
	}
	return cur, nil
}

// openTasks returns the unfinished execution Tasks of a Change.
func (s *Service) openTasks(ctx context.Context, tx pgx.Tx, changeID string) ([]string, error) {
	ids, err := s.store.TaskIDsTx(ctx, tx, changeID)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	statuses, err := s.tasks.StatusesInTx(ctx, tx, ids)
	if err != nil {
		return nil, fmt.Errorf("load task statuses: %w", err)
	}
	var open []string
	for _, id := range ids {
		if st, ok := statuses[id]; ok && !taskFinished(st) {
			open = append(open, id)
		}
	}
	return open, nil
}

// Complete finishes the execution of a Change that is in progress. Open
// execution Tasks block it unless force is "tasks_waived", which cancels them.
// Only the Change's owner or a holder of changes.execute may complete it.
func (s *Service) Complete(ctx context.Context, c Caller, p Principal, id string, expected *int, force string) (Change, error) {
	if err := c.validate(); err != nil {
		return Change{}, err
	}
	if force != "" && force != ReasonTasksWaived {
		return Change{}, invalid("force must be empty or %s", ReasonTasksWaived)
	}
	var out Change
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockExecute(ctx, tx, p, id, expected, "complete", StatusInProgress)
		if err != nil {
			return err
		}
		open, err := s.openTasks(ctx, tx, cur.ID)
		if err != nil {
			return err
		}
		reason, meta := "", map[string]any(nil)
		if len(open) > 0 {
			if force != ReasonTasksWaived {
				return ErrOpenTasks
			}
			if _, err := s.tasks.CancelByContextInTx(ctx, tx, c.Actor, c.CorrelationID, cur.ID, "change completed with waived tasks"); err != nil {
				return fmt.Errorf("cancel waived tasks: %w", err)
			}
			reason, meta = ReasonTasksWaived, map[string]any{"waivedTasks": len(open)}
		}
		now := s.now()
		next := cur
		next.Status, next.CompletedAt, next.StatusReason = StatusCompleted, &now, strPtr(reason)
		out, err = s.commit(ctx, tx, c, cur, next, "completed", reason, meta)
		if err != nil {
			return err
		}
		return publish(ctx, tx, c, "ChangeCompleted", map[string]any{"changeId": out.ID})
	})
	return out, err
}

// Fail records that a Change in progress failed, with a reason code and whether
// it was rolled back. Open execution Tasks stay (rollback work may still be
// tracked in them). Only the Change's owner or a holder of changes.execute may
// fail it.
func (s *Service) Fail(ctx context.Context, c Caller, p Principal, id string, expected *int, reason string, rollbackDone bool) (Change, error) {
	if err := c.validate(); err != nil {
		return Change{}, err
	}
	if !oneOf(reason, FailReasons) {
		return Change{}, invalid("reason must be one of %s", strings.Join(FailReasons, ", "))
	}
	var out Change
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockExecute(ctx, tx, p, id, expected, "fail", StatusInProgress)
		if err != nil {
			return err
		}
		now := s.now()
		next := cur
		next.Status, next.StatusReason, next.RollbackDone, next.CompletedAt = StatusFailed, &reason, &rollbackDone, &now
		out, err = s.commit(ctx, tx, c, cur, next, "failed", reason, map[string]any{"rollbackDone": rollbackDone})
		if err != nil {
			return err
		}
		return publish(ctx, tx, c, "ChangeFailed", map[string]any{"changeId": out.ID, "reason": reason, "rollbackDone": rollbackDone})
	})
	return out, err
}

// Review records the post-implementation review of a completed or failed
// Change with a short outcome note. Requires changes.manage.
func (s *Service) Review(ctx context.Context, c Caller, p Principal, id string, expected *int, outcomeNote string) (Change, error) {
	if err := c.validate(); err != nil {
		return Change{}, err
	}
	if !p.Manage {
		return Change{}, ErrForbidden
	}
	note, err := cleanText("outcome note", outcomeNote, maxOutcome)
	if err != nil {
		return Change{}, err
	}
	var out Change
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockStatus(ctx, tx, id, expected, "review", StatusCompleted, StatusFailed)
		if err != nil {
			return err
		}
		next := cur
		next.Status, next.OutcomeNote = StatusReview, note
		out, err = s.commit(ctx, tx, c, cur, next, "reviewed", "", nil)
		return err
	})
	return out, err
}

// Close closes a completed, failed or reviewed Change. An emergency change
// cannot be closed without a review (ErrReviewRequired). Requires changes.manage.
func (s *Service) Close(ctx context.Context, c Caller, p Principal, id string, expected *int) (Change, error) {
	if err := c.validate(); err != nil {
		return Change{}, err
	}
	if !p.Manage {
		return Change{}, ErrForbidden
	}
	var out Change
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockStatus(ctx, tx, id, expected, "close", StatusCompleted, StatusFailed, StatusReview)
		if err != nil {
			return err
		}
		if cur.ReviewRequired() && cur.Status != StatusReview {
			return ErrReviewRequired
		}
		now := s.now()
		next := cur
		next.Status, next.ClosedAt = StatusClosed, &now
		out, err = s.commit(ctx, tx, c, cur, next, "closed", "", nil)
		return err
	})
	return out, err
}

// Cancel cancels a Change that has not started (draft up to scheduled) with a
// reason code. A pending approval is cancelled and the execution Tasks of a
// scheduled Change are cancelled with it. Requires changes.manage.
func (s *Service) Cancel(ctx context.Context, c Caller, p Principal, id string, expected *int, reason string) (Change, error) {
	if err := c.validate(); err != nil {
		return Change{}, err
	}
	if !p.Manage {
		return Change{}, ErrForbidden
	}
	if !oneOf(reason, CancelReasons) {
		return Change{}, invalid("reason must be one of %s", strings.Join(CancelReasons, ", "))
	}
	var out Change
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockStatus(ctx, tx, id, expected, "cancel", StatusDraft, StatusAssessment, StatusPendingApproval, StatusApproved, StatusScheduled)
		if err != nil {
			return err
		}
		meta := map[string]any{}
		if cur.Status == StatusPendingApproval {
			if err := s.approvals.CancelBySubjectInTx(ctx, tx, c.Actor, c.CorrelationID, cur.ID); err != nil {
				return err
			}
		}
		if cur.Status == StatusScheduled {
			n, err := s.tasks.CancelByContextInTx(ctx, tx, c.Actor, c.CorrelationID, cur.ID, "change cancelled")
			if err != nil {
				return fmt.Errorf("cancel change tasks: %w", err)
			}
			meta["cancelledTasks"] = n
		}
		now := s.now()
		next := cur
		next.Status, next.StatusReason, next.ClosedAt = StatusCancelled, &reason, &now
		out, err = s.commit(ctx, tx, c, cur, next, "cancelled", reason, meta)
		return err
	})
	return out, err
}

// NewTask describes an execution Task (a checklist step) of a Change.
type NewTask struct {
	Title          string
	Description    string
	DueAt          *time.Time
	AssignedUserID *string
	AssignedTeamID *string
}

// AddTask adds an execution Task with the context type "change" to a
// scheduled or running Change. The Change's owner, a holder of changes.execute
// and changes.manage may add Tasks; at most 50 per Change.
func (s *Service) AddTask(ctx context.Context, c Caller, p Principal, id string, in NewTask) (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	title := strings.TrimSpace(in.Title)
	if title == "" || len([]rune(title)) > maxTaskTitle {
		return "", invalid("task title must be 1-%d characters", maxTaskTitle)
	}
	var taskID string
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		if !uuidPattern.MatchString(id) {
			return ErrNotFound
		}
		cur, err := s.store.LockTx(ctx, tx, strings.ToLower(id))
		if err != nil {
			return err
		}
		if !p.Manage && !p.canExecute(cur) {
			return denyExecute(p, cur)
		}
		if cur.Status != StatusScheduled && cur.Status != StatusInProgress {
			return &InvalidTransitionError{Operation: "add_task", From: cur.Status}
		}
		ids, err := s.store.TaskIDsTx(ctx, tx, cur.ID)
		if err != nil {
			return err
		}
		if len(ids) >= MaxTasks {
			return ErrTooMany
		}
		taskID, err = s.tasks.CreateInTx(ctx, tx, c.Actor, c.CorrelationID, TaskInput{ChangeID: cur.ID, Title: title,
			Description: in.Description, DueAt: in.DueAt, AssignedUserID: in.AssignedUserID, AssignedTeamID: in.AssignedTeamID})
		if err != nil {
			return err
		}
		if err := s.store.AddTaskTx(ctx, tx, cur.ID, taskID, userPtr(c)); err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "task_added", cur.ID, nil, nil, map[string]any{"taskId": taskID})
	})
	return taskID, err
}
