package application

import (
	"context"
	"errors"
)

// TicketTimeAccess is what the Time Tracking module may know about one Ticket for one User (F17 T-A): whether the
// Ticket exists, the Queue it is in (a snapshot for report scoping) and whether the User may view or work it.
// Reference is empty unless the User may view the Ticket. No title, text or people cross this contract.
type TicketTimeAccess struct {
	Found     bool
	Reference string
	QueueID   string
	CanView   bool
	CanWork   bool
}

// TimeAccess resolves the User's Queue access once and answers for each Ticket id: view access is tickets.view,
// tickets.manage or a view grant in the Ticket's Queue; work access is tickets.manage or a work grant. Unknown ids
// are answered with Found false. External accounts hold neither.
func (s *Service) TimeAccess(ctx context.Context, userID string, ticketIDs []string) (map[string]TicketTimeAccess, error) {
	out := make(map[string]TicketTimeAccess, len(ticketIDs))
	if userID == "" || len(ticketIDs) == 0 {
		return out, nil
	}
	p, err := s.principalOf(ctx, userID)
	if err != nil {
		return nil, err
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return nil, err
	}
	for _, id := range ticketIDs {
		if _, done := out[id]; done {
			continue
		}
		t, err := s.store.Get(ctx, id)
		if errors.Is(err, ErrNotFound) {
			out[id] = TicketTimeAccess{}
			continue
		}
		if err != nil {
			return nil, err
		}
		acc := TicketTimeAccess{Found: true, QueueID: t.QueueID, CanView: a.viewT(t), CanWork: a.workT(t)}
		if acc.CanView {
			acc.Reference = t.Reference
		}
		out[id] = acc
	}
	return out, nil
}

// ViewQueues returns the Queues whose Tickets the User may view (F17 T-C report scope): all of them (global) or a list
// of Queue ids. External accounts and users without Queue view access get an empty list and global false.
func (s *Service) ViewQueues(ctx context.Context, userID string) (global bool, queueIDs []string, err error) {
	if userID == "" {
		return false, nil, nil
	}
	p, err := s.principalOf(ctx, userID)
	if err != nil {
		return false, nil, err
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return false, nil, err
	}
	if a.global() {
		return true, nil, nil
	}
	return false, a.viewIDs(), nil
}
