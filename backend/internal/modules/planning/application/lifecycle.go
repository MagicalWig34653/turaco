package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

var systemActor = audit.SystemActor("planning-workflow")

// Approver names who approves an Initiative: exactly one of User and Team.
type Approver struct {
	UserID *string
	TeamID *string
}

// move runs one status transition that needs planning.manage and
// expectedVersion: it locks the Initiative in one of from, lets apply set the
// new state and commits it with the operation and reason.
func (s *Service) move(ctx context.Context, c Caller, p Principal, id string, expected *int, op string, from []string, reason string,
	apply func(tx pgx.Tx, cur Initiative, next *Initiative) error) (Initiative, error) {
	if err := c.validate(); err != nil {
		return Initiative{}, err
	}
	if !p.Manage {
		return Initiative{}, ErrForbidden
	}
	exp, err := requireVersion(expected)
	if err != nil {
		return Initiative{}, err
	}
	var out Initiative
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockStatus(ctx, tx, id, exp, op, from...)
		if err != nil {
			return err
		}
		next := cur
		if err := apply(tx, cur, &next); err != nil {
			return err
		}
		out, err = s.commit(ctx, tx, c, cur, next, op, reason, nil)
		return err
	})
	return out, err
}

// StartPlanning moves an idea into planning.
func (s *Service) StartPlanning(ctx context.Context, c Caller, p Principal, id string, expected *int) (Initiative, error) {
	return s.move(ctx, c, p, id, expected, "planning_started", []string{StatusIdea}, "", func(_ pgx.Tx, _ Initiative, next *Initiative) error {
		next.Status, next.StatusReason = StatusPlanning, nil
		return nil
	})
}

// Propose sends an Initiative in planning to its approver: it requests an
// Approval (subject initiative) for exactly one approver User or Team and the
// Initiative becomes proposed. Separation of duties: the owner, the creator,
// the proposer and every editor can never approve it (the Approvals module
// refuses them, ErrNoEligibleApprover). Requires planning.manage and
// expectedVersion.
func (s *Service) Propose(ctx context.Context, c Caller, p Principal, id string, expected *int, approver Approver) (Initiative, error) {
	if approver.UserID == nil == (approver.TeamID == nil) {
		return Initiative{}, invalid("exactly one approver (user or team) is required")
	}
	for _, a := range []*string{approver.UserID, approver.TeamID} {
		if a != nil {
			if _, err := checkID(*a); err != nil {
				return Initiative{}, err
			}
		}
	}
	return s.move(ctx, c, p, id, expected, "proposed", []string{StatusPlanning}, "", func(tx pgx.Tx, cur Initiative, next *Initiative) error {
		*next = addEditor(cur, c.Actor.UserID)
		excluded := append([]string{cur.OwnerID, cur.CreatedBy}, next.Editors...)
		if c.Actor.UserID != "" {
			excluded = append(excluded, c.Actor.UserID)
		}
		slices.Sort(excluded)
		excluded = slices.Compact(excluded)
		approvalID, err := s.approvals.RequestInTx(ctx, tx, c.Actor, c.CorrelationID, ApprovalRequest{
			SubjectID: cur.ID, Label: cur.Reference, ApproverUserID: approver.UserID, ApproverTeamID: approver.TeamID, ExcludedUserIDs: excluded,
		})
		if err != nil {
			return err
		}
		proposer := c.Actor.UserID
		next.Status, next.StatusReason, next.ApprovalID, next.ProposedBy = StatusProposed, nil, &approvalID, &proposer
		return nil
	})
}

// OnApprovalDecided moves a proposed Initiative whose approval was decided: an
// approval makes it approved, a rejection returns it to planning with the
// reason approval_rejected (the approver's comment stays with the approval; it
// can be changed and proposed again). It runs in the dispatcher's claim
// transaction and is idempotent: events of other subjects and stale events
// (the Initiative is no longer proposed) change nothing. An event without an
// approval id, or naming another approval than the pending one, is a permanent
// error.
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
		return events.Permanent(fmt.Errorf("approval decision %q of initiative %s is unknown", p.Decision, p.SubjectID))
	}
	if p.ApprovalID == "" {
		return events.Permanent(fmt.Errorf("approval decision of initiative %s carries no approval id", p.SubjectID))
	}
	if !uuidPattern.MatchString(p.SubjectID) {
		return events.Permanent(fmt.Errorf("approval decision names an invalid initiative id"))
	}
	cur, err := s.store.LockTx(ctx, tx, strings.ToLower(p.SubjectID))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if cur.Status != StatusProposed {
		return nil
	}
	if cur.ApprovalID == nil || !strings.EqualFold(*cur.ApprovalID, p.ApprovalID) {
		return events.Permanent(fmt.Errorf("approval %s is not the pending approval of initiative %s", p.ApprovalID, cur.ID))
	}
	c := Caller{Actor: systemActor, CorrelationID: ev.CorrelationID}
	next := cur
	if p.Decision == "approve" {
		now := s.now()
		next.Status, next.StatusReason, next.ApprovedAt = StatusApproved, nil, &now
		_, err := s.commit(ctx, tx, c, cur, next, "approved", "", nil)
		return err
	}
	next.Status, next.StatusReason = StatusPlanning, strPtr(ReasonApprovalRejected)
	_, err = s.commit(ctx, tx, c, cur, next, "approval_rejected", ReasonApprovalRejected, nil)
	return err
}

// Activate starts the work on an approved Initiative.
func (s *Service) Activate(ctx context.Context, c Caller, p Principal, id string, expected *int) (Initiative, error) {
	return s.move(ctx, c, p, id, expected, "activated", []string{StatusApproved}, "", func(_ pgx.Tx, _ Initiative, next *Initiative) error {
		now := s.now()
		next.Status, next.StatusReason, next.ActivatedAt = StatusActive, nil, &now
		return nil
	})
}

// Hold pauses an active Initiative with a reason code.
func (s *Service) Hold(ctx context.Context, c Caller, p Principal, id string, expected *int, reason string) (Initiative, error) {
	if !oneOf(reason, HoldReasons) {
		return Initiative{}, invalid("reason must be one of %s", strings.Join(HoldReasons, ", "))
	}
	return s.move(ctx, c, p, id, expected, "held", []string{StatusActive}, reason, func(_ pgx.Tx, _ Initiative, next *Initiative) error {
		next.Status, next.StatusReason = StatusOnHold, &reason
		return nil
	})
}

// Resume continues an Initiative on hold.
func (s *Service) Resume(ctx context.Context, c Caller, p Principal, id string, expected *int) (Initiative, error) {
	return s.move(ctx, c, p, id, expected, "resumed", []string{StatusOnHold}, "", func(_ pgx.Tx, _ Initiative, next *Initiative) error {
		next.Status, next.StatusReason = StatusActive, nil
		return nil
	})
}

// Complete finishes an active Initiative. Its included records keep their own
// lifecycles; open Milestones stay as they are (history).
func (s *Service) Complete(ctx context.Context, c Caller, p Principal, id string, expected *int) (Initiative, error) {
	return s.move(ctx, c, p, id, expected, "completed", []string{StatusActive}, "", func(_ pgx.Tx, _ Initiative, next *Initiative) error {
		now := s.now()
		next.Status, next.StatusReason, next.ClosedAt = StatusCompleted, nil, &now
		return nil
	})
}

// Cancel cancels an Initiative that has not ended, with a reason code. A
// pending approval is cancelled with it.
func (s *Service) Cancel(ctx context.Context, c Caller, p Principal, id string, expected *int, reason string) (Initiative, error) {
	if !oneOf(reason, CancelReasons) {
		return Initiative{}, invalid("reason must be one of %s", strings.Join(CancelReasons, ", "))
	}
	from := []string{StatusIdea, StatusPlanning, StatusProposed, StatusApproved, StatusActive, StatusOnHold}
	return s.move(ctx, c, p, id, expected, "cancelled", from, reason, func(tx pgx.Tx, cur Initiative, next *Initiative) error {
		if cur.Status == StatusProposed {
			if err := s.approvals.CancelBySubjectInTx(ctx, tx, c.Actor, c.CorrelationID, cur.ID); err != nil {
				return fmt.Errorf("cancel approval: %w", err)
			}
		}
		now := s.now()
		next.Status, next.StatusReason, next.ClosedAt = StatusCancelled, &reason, &now
		return nil
	})
}
