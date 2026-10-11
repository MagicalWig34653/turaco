package application

import (
	"context"
	"errors"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/jackc/pgx/v5"
)

type FollowStore interface {
	Following(ctx context.Context, ticketID, userID string) (bool, error)
	FollowingTx(ctx context.Context, tx pgx.Tx, ticketID, userID string) (bool, error)
	SetFollowingTx(ctx context.Context, tx pgx.Tx, ticketID, userID string, following bool) error
}

// Following reads the caller's preference only after checking requester visibility.
func (s *Service) Following(ctx context.Context, p Principal, id string) (bool, error) {
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return false, err
	}
	if p.External || !s.isOwner(t, p) {
		return false, ErrNotFound
	}
	fs, ok := s.store.(FollowStore)
	if !ok {
		return true, nil
	}
	return fs.Following(ctx, t.ID, p.UserID)
}

// SetFollowing changes only the requester's own preference. It does not bump the ticket version.
func (s *Service) SetFollowing(ctx context.Context, c Caller, p Principal, id string, following bool) (bool, error) {
	if err := c.validate(); err != nil {
		return false, err
	}
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return false, err
	}
	if p.External || !s.isOwner(t, p) {
		return false, ErrNotFound
	}
	fs, ok := s.store.(FollowStore)
	if !ok {
		return false, errors.New("servicedesk: ticket follow store unavailable")
	}
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		// Serialize changes to this preference through the Ticket row. The requester
		// relation is checked again under the lock before the preference is written.
		locked, err := s.store.LockTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if !s.isOwner(locked, p) {
			return ErrNotFound
		}
		before, err := fs.FollowingTx(ctx, tx, t.ID, p.UserID)
		if err != nil || before == following {
			return err
		}
		if err := fs.SetFollowingTx(ctx, tx, t.ID, p.UserID, following); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Change{Action: "servicedesk.ticket.follow_changed", TargetType: "ticket", TargetID: t.ID,
			Actor: c.Actor, CorrelationID: c.CorrelationID, Metadata: map[string]any{"following": following}})
	})
	return following, err
}
