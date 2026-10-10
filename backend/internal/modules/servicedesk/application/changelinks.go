package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	changespublic "github.com/MagicalWig34653/turaco/backend/internal/modules/changes/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

// Ticket-to-Change links use the platform relationship graph ("ticket RELATES_TO change"); the Service Desk owns
// the triple and does not change the Change. The Changes module's public reader supplies the Change summaries.

// Triples are the relationships the Service Desk registers.
var Triples = []relationships.Triple{{SourceType: "ticket", Type: "RELATES_TO", TargetType: "change", Owner: "servicedesk"}}

// MaxLinkedChanges bounds the Changes linked to one Ticket (and the Tickets listed for one Change).
const MaxLinkedChanges = 25

// ChangeReader is the Changes public reader (changes/public.Changes).
type ChangeReader interface {
	Lookup(ctx context.Context, ids []string, scope changespublic.ReadScope) (map[string]changespublic.ChangeInfo, error)
}

// WithChanges connects the Change reader and the relationship graph; q reads the graph outside transactions.
func (s *Service) WithChanges(changes ChangeReader, graph *relationships.Graph, q relationships.Querier) *Service {
	s.changes, s.graph, s.graphDB = changes, graph, q
	return s
}

// ChangeLink is a Change linked to a Ticket as the caller may see it; Hidden links carry no Change data.
type ChangeLink struct {
	ID        string
	ChangeID  string
	Reference string
	Title     string
	Status    string
	Hidden    bool
}

// TicketLink is a Ticket linked to a Change as the caller may see it.
type TicketLink struct {
	TicketID  string
	Reference string
	Title     string
	Status    string
}

func lower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

var errNoChanges = fmt.Errorf("servicedesk: change links are not configured")

func ticketNode(id string) relationships.Node { return relationships.Node{Type: "ticket", ID: id} }
func changeNode(id string) relationships.Node { return relationships.Node{Type: "change", ID: id} }

// mayReadChange applies the Changes read rule: changes.view|manage|execute, or being the requester or owner.
func mayReadChange(p Principal, c changespublic.ChangeInfo) bool {
	return p.ChangesView || c.RequesterID == p.UserID || (c.OwnerID != nil && *c.OwnerID == p.UserID)
}

// LinkChange relates the Ticket to a Change (idempotent). Requires work access in the Ticket's Queue; the caller
// must be allowed to read the Change. Audited as servicedesk.ticket.change_linked.
func (s *Service) LinkChange(ctx context.Context, c Caller, p Principal, ticketID, changeID string) (ChangeLink, error) {
	if err := c.validate(); err != nil {
		return ChangeLink{}, err
	}
	if s.changes == nil || s.graph == nil {
		return ChangeLink{}, errNoChanges
	}
	changeID = lower(changeID)
	if !uuidPattern.MatchString(changeID) {
		return ChangeLink{}, invalid("changeId must be a change id")
	}
	found, err := s.changes.Lookup(ctx, []string{changeID}, changespublic.ReadScope{IncludeDetails: true})
	if err != nil {
		return ChangeLink{}, fmt.Errorf("load change: %w", err)
	}
	info, ok := found[changeID]
	if !ok || !mayReadChange(p, info) {
		return ChangeLink{}, ErrNotFound
	}
	m, err := s.memberships(ctx, p.UserID)
	if err != nil {
		return ChangeLink{}, err
	}
	var out ChangeLink
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, _, ep, err := s.work(ctx, tx, p, m, ticketID)
		if err != nil {
			return err
		}
		if !ep.Manage {
			return ErrForbidden
		}
		page, err := s.graph.Outgoing(ctx, tx, ticketNode(cur.ID), []string{"RELATES_TO"}, "", MaxLinkedChanges+1)
		if err != nil {
			return err
		}
		for _, l := range page.Items {
			if l.Target.Type == "change" && l.Target.ID == changeID {
				out = changeLink(l.ID, info)
				return nil
			}
		}
		if len(page.Items) >= MaxLinkedChanges {
			return invalid("linked change limit reached")
		}
		rel, created, err := s.graph.Link(ctx, tx, relationships.LinkInput{Owner: "servicedesk", Source: ticketNode(cur.ID), Type: "RELATES_TO", Target: changeNode(changeID),
			Confidence: relationships.ConfidenceDeclared, CreatedBy: c.Actor.UserID, RecordedBy: "servicedesk"})
		if err != nil {
			return err
		}
		out = changeLink(rel.ID, info)
		if !created {
			return nil
		}
		return audit.Record(ctx, tx, audit.Change{Action: "servicedesk.ticket.change_linked", TargetType: "ticket", TargetID: cur.ID, Actor: c.Actor,
			CorrelationID: c.CorrelationID, Metadata: map[string]any{"changeId": changeID}})
	})
	return out, err
}

func changeLink(id string, c changespublic.ChangeInfo) ChangeLink {
	return ChangeLink{ID: id, ChangeID: c.ID, Reference: c.Reference, Title: c.Title, Status: c.Status}
}

// UnlinkChange ends the relationship (idempotent). Requires work access in the Ticket's Queue.
func (s *Service) UnlinkChange(ctx context.Context, c Caller, p Principal, ticketID, changeID string) error {
	if err := c.validate(); err != nil {
		return err
	}
	if s.graph == nil {
		return errNoChanges
	}
	changeID = lower(changeID)
	if !uuidPattern.MatchString(changeID) {
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
		ended, err := s.graph.UnlinkTriple(ctx, tx, "servicedesk", ticketNode(cur.ID), "RELATES_TO", changeNode(changeID), "removed", c.Actor.UserID)
		if err != nil || !ended {
			return err
		}
		return audit.Record(ctx, tx, audit.Change{Action: "servicedesk.ticket.change_unlinked", TargetType: "ticket", TargetID: cur.ID, Actor: c.Actor,
			CorrelationID: c.CorrelationID, Metadata: map[string]any{"changeId": changeID}})
	})
}

// LinkedChanges lists the Changes related to a Ticket for people who view its Queue. Changes the caller may not
// read are returned as hidden links without data.
func (s *Service) LinkedChanges(ctx context.Context, p Principal, ticketID string) ([]ChangeLink, error) {
	if s.changes == nil || s.graph == nil {
		return []ChangeLink{}, nil
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
	page, err := s.graph.Outgoing(ctx, s.graphDB, ticketNode(t.ID), []string{"RELATES_TO"}, "", MaxLinkedChanges+1)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(page.Items))
	for _, l := range page.Items {
		ids = append(ids, l.Target.ID)
	}
	found, err := s.changes.Lookup(ctx, ids, changespublic.ReadScope{IncludeDetails: true})
	if err != nil {
		return nil, fmt.Errorf("load changes: %w", err)
	}
	out := make([]ChangeLink, 0, len(page.Items))
	for i, l := range page.Items {
		info, ok := found[l.Target.ID]
		if !ok {
			continue
		}
		if !mayReadChange(p, info) {
			out = append(out, ChangeLink{ID: fmt.Sprintf("hidden-%d", i+1), Hidden: true})
			continue
		}
		out = append(out, changeLink(l.ID, info))
	}
	return out, nil
}

// TicketsOfChange lists the Tickets related to a Change that the caller may see (view access in their Queue). The
// Change must be readable by the caller, otherwise ErrNotFound.
func (s *Service) TicketsOfChange(ctx context.Context, p Principal, changeID string) ([]TicketLink, error) {
	if s.changes == nil || s.graph == nil {
		return []TicketLink{}, nil
	}
	changeID = lower(changeID)
	if !uuidPattern.MatchString(changeID) {
		return nil, ErrNotFound
	}
	found, err := s.changes.Lookup(ctx, []string{changeID}, changespublic.ReadScope{IncludeDetails: true})
	if err != nil {
		return nil, fmt.Errorf("load change: %w", err)
	}
	if info, ok := found[changeID]; !ok || !mayReadChange(p, info) {
		return nil, ErrNotFound
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return nil, err
	}
	page, err := s.graph.Incoming(ctx, s.graphDB, changeNode(changeID), []string{"RELATES_TO"}, "", MaxLinkedChanges+1)
	if err != nil {
		return nil, err
	}
	out := []TicketLink{}
	for _, l := range page.Items {
		if l.Source.Type != "ticket" {
			continue
		}
		t, err := s.store.Get(ctx, l.Source.ID)
		if err != nil {
			continue // a ticket that vanished is not listed
		}
		if !a.eff(p, t.QueueID).staff() {
			continue
		}
		if err := s.shape(ctx, a, &t); err != nil {
			return nil, err
		}
		out = append(out, TicketLink{TicketID: t.ID, Reference: t.Reference, Title: t.Title, Status: t.Status})
	}
	return out, nil
}
