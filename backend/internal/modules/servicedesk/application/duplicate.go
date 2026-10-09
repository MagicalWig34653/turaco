package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Duplicate handling: an explicit lifecycle operation, not a generic status update. Marking a ticket as the duplicate
// of another cancels it with the status reason StatusReasonDuplicate and points it to the ticket that carries the
// work. Rules: the caller works the duplicate's Queue and may view the target; the duplicate is still active (new,
// open, in progress or waiting); the target is another ticket that is not cancelled, is not itself a duplicate and is
// not the master of other duplicates in a way that would form a chain (a ticket that already has duplicates cannot
// become one).
const StatusReasonDuplicate = "duplicate"

// ErrDuplicateTarget means the target cannot take a duplicate (itself, cancelled, or a chain would form).
var ErrDuplicateTarget = errors.New("servicedesk: invalid duplicate target")

// DuplicateStore is the persistence port of the duplicate relation.
type DuplicateStore interface {
	// MarkDuplicateTx cancels the locked ticket as a duplicate of target (status_reason "duplicate").
	MarkDuplicateTx(ctx context.Context, tx pgx.Tx, id, targetID string) (Ticket, error)
	// DuplicateStateTx reports whether the ticket is itself a duplicate and how many tickets are marked as its duplicates.
	DuplicateStateTx(ctx context.Context, tx pgx.Tx, id string) (isDuplicate bool, duplicates int, err error)
}

// MarkDuplicate marks the ticket as a duplicate of another one. expected is the duplicate's version. targetID
// names the other ticket (the caller needs view access to it; an unknown and an invisible one are the same
// ErrNotFound). reason is an optional short note.
func (s *Service) MarkDuplicate(ctx context.Context, c Caller, p Principal, id string, expected *int, targetID, reason string) (Ticket, error) {
	if err := c.validate(); err != nil {
		return Ticket{}, err
	}
	ds, ok := s.store.(DuplicateStore)
	if !ok {
		return Ticket{}, fmt.Errorf("servicedesk: store does not support duplicates")
	}
	reason, err := cleanText(reason, maxReason, false, "reason")
	if err != nil {
		return Ticket{}, err
	}
	targetID = strings.ToLower(strings.TrimSpace(targetID))
	if !uuidPattern.MatchString(targetID) {
		return Ticket{}, invalid("duplicateOfTicketId must be a ticket id")
	}
	if strings.EqualFold(targetID, id) {
		return Ticket{}, ErrDuplicateTarget
	}
	m, err := s.memberships(ctx, p.UserID)
	if err != nil {
		return Ticket{}, err
	}
	var out Ticket
	var acc access
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, a, ep, err := s.work(ctx, tx, p, m, id)
		if err != nil {
			return err
		}
		acc = a
		if !ep.Manage {
			return ErrForbidden
		}
		if expected != nil && *expected != cur.Version {
			return ErrVersionConflict
		}
		if !slices.Contains(rules[OpCancel].from, cur.Status) {
			return &InvalidTransitionError{Operation: "mark_duplicate", From: cur.Status}
		}
		target, err := s.store.LockTx(ctx, tx, targetID)
		if err != nil {
			return err // unknown target: ErrNotFound
		}
		if !a.eff(p, target.QueueID).staff() {
			return ErrNotFound
		}
		if target.Status == StatusCancelled || target.DuplicateOfID != nil {
			return ErrDuplicateTarget
		}
		if _, n, err := ds.DuplicateStateTx(ctx, tx, cur.ID); err != nil {
			return err
		} else if n > 0 {
			return ErrDuplicateTarget
		}
		out, err = ds.MarkDuplicateTx(ctx, tx, cur.ID, target.ID)
		if err != nil {
			return err
		}
		meta := map[string]any{"operation": "mark_duplicate", "duplicateOfId": target.ID, "reasonCode": StatusReasonDuplicate}
		if reason != "" {
			meta["reason"] = reason
		}
		if err := record(ctx, tx, c, "servicedesk.ticket.marked_duplicate", &cur, &out, meta); err != nil {
			return err
		}
		return publish(ctx, tx, c, "TicketStatusChanged", map[string]any{"ticketId": out.ID, "operation": "mark_duplicate", "duplicateOfId": target.ID})
	})
	if err != nil {
		return Ticket{}, err
	}
	return out, s.shape(ctx, acc, &out)
}
