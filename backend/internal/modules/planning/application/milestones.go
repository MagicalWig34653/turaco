package application

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// MilestoneInput describes a Milestone to add. A nil Position appends it after the last one.
type MilestoneInput struct {
	Title    string
	DueDate  time.Time
	Position *int
}

// MilestoneChange changes a Milestone; nil fields stay unchanged.
type MilestoneChange struct {
	Title    *string
	DueDate  *time.Time
	Position *int
}

func checkPosition(p *int) error {
	if p != nil && (*p < 0 || *p > maxPosition) {
		return invalid("position must be between 0 and %d", maxPosition)
	}
	return nil
}

// lockEditable locks an Initiative in an editable status for a Milestone or
// item operation. initiativeVersion, when given, must match.
func (s *Service) lockEditable(ctx context.Context, tx pgx.Tx, id string, initiativeVersion *int, op string) (Initiative, error) {
	if !uuidPattern.MatchString(id) {
		return Initiative{}, ErrNotFound
	}
	cur, err := s.store.LockTx(ctx, tx, strings.ToLower(id))
	if err != nil {
		return Initiative{}, err
	}
	if initiativeVersion != nil && *initiativeVersion != cur.Version {
		return Initiative{}, ErrVersionConflict
	}
	if !oneOf(cur.Status, editableStatuses) {
		return Initiative{}, &InvalidTransitionError{Operation: op, From: cur.Status}
	}
	return cur, nil
}

// AddMilestone adds a dated Milestone to an editable Initiative (at most
// MaxMilestones live ones). expectedVersion is the Initiative's version; it is
// checked, not bumped. Requires planning.manage.
func (s *Service) AddMilestone(ctx context.Context, c Caller, p Principal, initiativeID string, expected *int, in MilestoneInput) (Milestone, error) {
	if err := c.validate(); err != nil {
		return Milestone{}, err
	}
	if !p.Manage {
		return Milestone{}, ErrForbidden
	}
	exp, err := requireVersion(expected)
	if err != nil {
		return Milestone{}, err
	}
	title, err := cleanTitle(in.Title)
	if err != nil {
		return Milestone{}, err
	}
	if in.DueDate.IsZero() {
		return Milestone{}, invalid("dueDate is required")
	}
	due := day(in.DueDate)
	if err := checkDate("dueDate", &due); err != nil {
		return Milestone{}, err
	}
	if err := checkPosition(in.Position); err != nil {
		return Milestone{}, err
	}
	var out Milestone
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockEditable(ctx, tx, initiativeID, &exp, "add_milestone")
		if err != nil {
			return err
		}
		count, maxPos, err := s.store.LiveMilestonesTx(ctx, tx, cur.ID)
		if err != nil {
			return err
		}
		if count >= MaxMilestones {
			return ErrTooMany
		}
		pos := maxPos + 1
		if count == 0 {
			pos = 0
		}
		if in.Position != nil {
			pos = *in.Position
		}
		if pos > maxPosition {
			return invalid("position must be between 0 and %d", maxPosition)
		}
		by := c.Actor.UserID
		out, err = s.store.InsertMilestoneTx(ctx, tx, Milestone{InitiativeID: cur.ID, Title: title, DueDate: due, Position: pos, CreatedBy: strPtr(by)})
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "milestone_added", cur.ID, nil, nil, map[string]any{"milestoneId": out.ID})
	})
	return out, err
}

// milestoneOp locks the Initiative (editable) and the Milestone (not removed,
// at the expected version) and commits what apply changed.
func (s *Service) milestoneOp(ctx context.Context, c Caller, p Principal, initiativeID, milestoneID string, expected *int, op, action string,
	apply func(m *Milestone) (map[string]any, error)) (Milestone, error) {
	if err := c.validate(); err != nil {
		return Milestone{}, err
	}
	if !p.Manage {
		return Milestone{}, ErrForbidden
	}
	exp, err := requireVersion(expected)
	if err != nil {
		return Milestone{}, err
	}
	if !uuidPattern.MatchString(milestoneID) {
		return Milestone{}, ErrNotFound
	}
	var out Milestone
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockEditable(ctx, tx, initiativeID, nil, op)
		if err != nil {
			return err
		}
		m, err := s.store.LockMilestoneTx(ctx, tx, cur.ID, strings.ToLower(milestoneID))
		if err != nil {
			return err
		}
		if m.RemovedAt != nil {
			return ErrNotFound
		}
		if m.Version != exp {
			return ErrVersionConflict
		}
		next := m
		meta, err := apply(&next)
		if err != nil {
			return err
		}
		if meta == nil {
			out = m
			return nil
		}
		if out, err = s.store.UpdateMilestoneTx(ctx, tx, next); err != nil {
			return err
		}
		meta["milestoneId"] = m.ID
		return recordAudit(ctx, tx, c, action, cur.ID, nil, nil, meta)
	})
	return out, err
}

// UpdateMilestone changes a Milestone's title, due date or position.
// expectedVersion is the Milestone's version. Requires planning.manage.
func (s *Service) UpdateMilestone(ctx context.Context, c Caller, p Principal, initiativeID, milestoneID string, expected *int, in MilestoneChange) (Milestone, error) {
	var title string
	var err error
	if in.Title != nil {
		if title, err = cleanTitle(*in.Title); err != nil {
			return Milestone{}, err
		}
	}
	due := dayPtr(in.DueDate)
	if err := checkDate("dueDate", due); err != nil {
		return Milestone{}, err
	}
	if err := checkPosition(in.Position); err != nil {
		return Milestone{}, err
	}
	return s.milestoneOp(ctx, c, p, initiativeID, milestoneID, expected, "update_milestone", "milestone_updated", func(m *Milestone) (map[string]any, error) {
		var changed []string
		if in.Title != nil && title != m.Title {
			m.Title = title
			changed = append(changed, "title")
		}
		if due != nil && !due.Equal(m.DueDate) {
			m.DueDate = *due
			changed = append(changed, "dueDate")
		}
		if in.Position != nil && *in.Position != m.Position {
			m.Position = *in.Position
			changed = append(changed, "position")
		}
		if len(changed) == 0 {
			return nil, nil
		}
		return map[string]any{"changedFields": changed}, nil
	})
}

// CompleteMilestone marks an open Milestone done.
func (s *Service) CompleteMilestone(ctx context.Context, c Caller, p Principal, initiativeID, milestoneID string, expected *int) (Milestone, error) {
	return s.milestoneOp(ctx, c, p, initiativeID, milestoneID, expected, "complete_milestone", "milestone_completed", func(m *Milestone) (map[string]any, error) {
		if m.DoneAt != nil {
			return nil, &InvalidTransitionError{Operation: "complete_milestone", From: "done"}
		}
		now, by := s.now(), c.Actor.UserID
		m.DoneAt, m.DoneBy = &now, strPtr(by)
		return map[string]any{}, nil
	})
}

// ReopenMilestone marks a done Milestone open again.
func (s *Service) ReopenMilestone(ctx context.Context, c Caller, p Principal, initiativeID, milestoneID string, expected *int) (Milestone, error) {
	return s.milestoneOp(ctx, c, p, initiativeID, milestoneID, expected, "reopen_milestone", "milestone_reopened", func(m *Milestone) (map[string]any, error) {
		if m.DoneAt == nil {
			return nil, &InvalidTransitionError{Operation: "reopen_milestone", From: "open"}
		}
		m.DoneAt, m.DoneBy = nil, nil
		return map[string]any{}, nil
	})
}

// RemoveMilestone removes a Milestone with a reason code; the row stays (history).
func (s *Service) RemoveMilestone(ctx context.Context, c Caller, p Principal, initiativeID, milestoneID string, expected *int, reason string) (Milestone, error) {
	if !oneOf(reason, MilestoneRemoveReasons) {
		return Milestone{}, invalid("reason must be one of %s", strings.Join(MilestoneRemoveReasons, ", "))
	}
	return s.milestoneOp(ctx, c, p, initiativeID, milestoneID, expected, "remove_milestone", "milestone_removed", func(m *Milestone) (map[string]any, error) {
		now, by := s.now(), c.Actor.UserID
		m.RemovedAt, m.RemoveReason, m.RemovedBy = &now, &reason, strPtr(by)
		return map[string]any{"reason": reason}, nil
	})
}
