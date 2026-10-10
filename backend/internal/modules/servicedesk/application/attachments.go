package application

import (
	"context"
)

// AttachmentRights is what a caller may do with the file attachments of one Ticket (ADR-0037). It reuses the
// Ticket authorization: reading follows ticket visibility, attaching follows the comment ability.
type AttachmentRights struct {
	// Read: the reporter, the affected User and staff with access to the Ticket's Queue.
	Read bool
	// Attach: those who may comment, while the Ticket is open.
	Attach bool
	// Internal: staff, who see attachments of internal comments.
	Internal bool
	// Moderate: people who work the Queue may remove any attachment.
	Moderate bool
}

// AttachmentRights returns the caller's rights on the Ticket, or ErrNotFound for a Ticket the caller cannot see.
func (s *Service) AttachmentRights(ctx context.Context, p Principal, id string) (AttachmentRights, error) {
	a, err := s.resolve(ctx, p)
	if err != nil {
		return AttachmentRights{}, err
	}
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return AttachmentRights{}, err
	}
	ep := a.eff(p, t.QueueID)
	if !ep.staff() && !s.isOwner(t, p) {
		return AttachmentRights{}, ErrNotFound
	}
	ab := AbilitiesOf(t, ep, s.queues != nil)
	return AttachmentRights{Read: true, Attach: ab.Comment, Internal: ep.staff(), Moderate: ep.Manage}, nil
}
