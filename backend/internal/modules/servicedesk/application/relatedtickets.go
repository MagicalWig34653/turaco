package application

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

// Ticket-to-Ticket links use the platform relationship graph ("ticket RELATES_TO ticket"), like the Change links.
// The relation is symmetric for the user: it is stored once (from the Ticket it was created on) and read in both
// directions. Duplicates are not links: they are the lifecycle operation of duplicate.go and are listed separately.

// MaxRelatedTickets bounds the Tickets linked to one Ticket (both directions together).
const MaxRelatedTickets = 25

// MaxListedDuplicates bounds the duplicates listed on the surviving Ticket.
const MaxListedDuplicates = 50

// RelatedTicket is a Ticket linked to or marked as a duplicate of another as the caller may see it; Hidden links
// carry no Ticket data.
type RelatedTicket struct {
	ID        string
	TicketID  string
	Reference string
	Title     string
	Status    string
	Hidden    bool
}

func relatedTicket(linkID string, t Ticket) RelatedTicket {
	return RelatedTicket{ID: linkID, TicketID: t.ID, Reference: t.Reference, Title: t.Title, Status: t.Status}
}

// Duplicates lists the Tickets marked as duplicates of the Ticket, for people who view its Queue. Duplicates in a
// Queue the caller may not view are left out.
func (s *Service) Duplicates(ctx context.Context, p Principal, ticketID string) ([]RelatedTicket, error) {
	ds, ok := s.store.(DuplicateStore)
	if !ok {
		return []RelatedTicket{}, nil
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return nil, err
	}
	t, err := s.store.Get(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	if !a.eff(p, t.QueueID).staff() {
		return nil, ErrNotFound
	}
	dups, err := ds.Duplicates(ctx, t.ID, MaxListedDuplicates)
	if err != nil {
		return nil, err
	}
	out := []RelatedTicket{}
	for _, d := range dups {
		if !a.eff(p, d.QueueID).staff() {
			continue
		}
		if err := s.shape(ctx, a, &d); err != nil {
			return nil, err
		}
		out = append(out, relatedTicket(d.ID, d))
	}
	return out, nil
}

// LinkTicket relates the Ticket to another one (idempotent, in either direction). Requires work access in both
// Tickets' Queues; an unknown and an invisible other Ticket are the same ErrNotFound. A Ticket cannot be linked
// to itself. Audited as servicedesk.ticket.ticket_linked on the Ticket it is called on.
func (s *Service) LinkTicket(ctx context.Context, c Caller, p Principal, ticketID, otherID string) (RelatedTicket, error) {
	if err := c.validate(); err != nil {
		return RelatedTicket{}, err
	}
	if s.graph == nil {
		return RelatedTicket{}, errNoChanges
	}
	otherID = lower(otherID)
	if !uuidPattern.MatchString(otherID) {
		return RelatedTicket{}, invalid("ticketId must be a ticket id")
	}
	if otherID == lower(ticketID) {
		return RelatedTicket{}, invalid("a ticket cannot be linked to itself")
	}
	m, err := s.memberships(ctx, p.UserID)
	if err != nil {
		return RelatedTicket{}, err
	}
	var out RelatedTicket
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, a, ep, err := s.work(ctx, tx, p, m, ticketID)
		if err != nil {
			return err
		}
		if !ep.Manage {
			return ErrForbidden
		}
		other, err := s.store.Get(ctx, otherID)
		if err != nil {
			return err
		}
		if oe := a.eff(p, other.QueueID); !oe.staff() {
			return ErrNotFound
		} else if !oe.Manage {
			return ErrForbidden
		}
		existing, err := s.ticketLinks(ctx, tx, cur.ID)
		if err != nil {
			return err
		}
		for _, l := range existing {
			if l.other == other.ID {
				out = relatedTicket(l.id, other)
				return nil
			}
		}
		if len(existing) >= MaxRelatedTickets {
			return invalid("linked ticket limit reached")
		}
		rel, created, err := s.graph.Link(ctx, tx, relationships.LinkInput{Owner: "servicedesk", Source: ticketNode(cur.ID), Type: "RELATES_TO", Target: ticketNode(other.ID),
			Confidence: relationships.ConfidenceDeclared, CreatedBy: c.Actor.UserID, RecordedBy: "servicedesk"})
		if err != nil {
			return err
		}
		out = relatedTicket(rel.ID, other)
		if !created {
			return nil
		}
		return audit.Record(ctx, tx, audit.Change{Action: "servicedesk.ticket.ticket_linked", TargetType: "ticket", TargetID: cur.ID, Actor: c.Actor,
			CorrelationID: c.CorrelationID, Metadata: map[string]any{"relatedTicketId": other.ID}})
	})
	return out, err
}

// UnlinkTicket ends the link between two Tickets, whichever way it was created (idempotent). Requires work access
// in the Queue of the Ticket it is called on; the other Ticket may be in a Queue the caller cannot see.
func (s *Service) UnlinkTicket(ctx context.Context, c Caller, p Principal, ticketID, otherID string) error {
	if err := c.validate(); err != nil {
		return err
	}
	if s.graph == nil {
		return errNoChanges
	}
	otherID = lower(otherID)
	if !uuidPattern.MatchString(otherID) {
		return ErrNotFound
	}
	m, err := s.memberships(ctx, p.UserID)
	if err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, _, ep, err := s.work(ctx, tx, p, m, ticketID)
		if err != nil {
			return err
		}
		if !ep.Manage {
			return ErrForbidden
		}
		ended := false
		for _, pair := range [][2]string{{cur.ID, otherID}, {otherID, cur.ID}} {
			e, err := s.graph.UnlinkTriple(ctx, tx, "servicedesk", ticketNode(pair[0]), "RELATES_TO", ticketNode(pair[1]), "removed", c.Actor.UserID)
			if err != nil {
				return err
			}
			ended = ended || e
		}
		if !ended {
			return nil
		}
		return audit.Record(ctx, tx, audit.Change{Action: "servicedesk.ticket.ticket_unlinked", TargetType: "ticket", TargetID: cur.ID, Actor: c.Actor,
			CorrelationID: c.CorrelationID, Metadata: map[string]any{"relatedTicketId": otherID}})
	})
}

type ticketLink struct{ id, other string }

// ticketLinks reads the live Ticket links of a Ticket in both directions (the other end's id).
func (s *Service) ticketLinks(ctx context.Context, q relationships.Querier, ticketID string) ([]ticketLink, error) {
	var out []ticketLink
	for _, incoming := range []bool{false, true} {
		var page relationships.Page
		var err error
		if incoming {
			page, err = s.graph.Incoming(ctx, q, ticketNode(ticketID), []string{"RELATES_TO"}, "", MaxRelatedTickets+1)
		} else {
			page, err = s.graph.Outgoing(ctx, q, ticketNode(ticketID), []string{"RELATES_TO"}, "", MaxRelatedTickets+1)
		}
		if err != nil {
			return nil, err
		}
		for _, l := range page.Items {
			switch {
			case !incoming && l.Target.Type == "ticket":
				out = append(out, ticketLink{l.ID, l.Target.ID})
			case incoming && l.Source.Type == "ticket":
				out = append(out, ticketLink{l.ID, l.Source.ID})
			}
		}
	}
	return out, nil
}

// RelatedTickets lists the Tickets linked to a Ticket for people who view its Queue. Tickets in a Queue the caller
// may not view are returned as hidden placeholders without data.
func (s *Service) RelatedTickets(ctx context.Context, p Principal, ticketID string) ([]RelatedTicket, error) {
	if s.graph == nil {
		return []RelatedTicket{}, nil
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return nil, err
	}
	t, err := s.store.Get(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	if !a.eff(p, t.QueueID).staff() {
		return nil, ErrNotFound
	}
	links, err := s.ticketLinks(ctx, s.graphDB, t.ID)
	if err != nil {
		return nil, err
	}
	out := []RelatedTicket{}
	for i, l := range links {
		o, err := s.store.Get(ctx, l.other)
		if err != nil {
			continue // a ticket that vanished is not listed
		}
		if !a.eff(p, o.QueueID).staff() {
			out = append(out, RelatedTicket{ID: fmt.Sprintf("hidden-%d", i+1), Hidden: true})
			continue
		}
		if err := s.shape(ctx, a, &o); err != nil {
			return nil, err
		}
		out = append(out, relatedTicket(l.id, o))
	}
	return out, nil
}
