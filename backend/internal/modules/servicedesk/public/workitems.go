package public

import (
	"context"
	"errors"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/workitems"
)

// TicketWorkItems is a Service Desk source of My Work. Two instances exist: the open Tickets assigned to the caller
// (key "tickets") and the unassigned open Tickets routed to one of the caller's Teams (key "team_tickets", the "to
// pick up" list). Every row is authorized for the caller: they must still be able to view the Ticket.
type TicketWorkItems struct {
	svc *application.Service
	key string
	src application.WorkSource
}

// NewAssignedWorkItems is the source "tickets".
func NewAssignedWorkItems(svc *application.Service) *TicketWorkItems {
	return &TicketWorkItems{svc: svc, key: "tickets", src: application.WorkAssigned}
}

// NewTeamWorkItems is the source "team_tickets".
func NewTeamWorkItems(svc *application.Service) *TicketWorkItems {
	return &TicketWorkItems{svc: svc, key: "team_tickets", src: application.WorkTeam}
}

var _ workitems.Source = (*TicketWorkItems)(nil)

func (w *TicketWorkItems) Key() string    { return w.key }
func (w *TicketWorkItems) Module() string { return "servicedesk" }

func ticketPrincipal(p authorization.Principal) application.Principal {
	return application.Principal{UserID: p.UserID, View: p.Has("tickets.view"), Manage: p.Has("tickets.manage"), QueuesManage: p.Has(application.PermQueuesManage)}
}

// Items implements workitems.Source.
func (w *TicketWorkItems) Items(ctx context.Context, p authorization.Principal, cursor string, limit int) ([]workitems.Entry, error) {
	rows, err := w.svc.MyWorkTickets(ctx, ticketPrincipal(p), w.src, cursor, limit)
	if errors.Is(err, application.ErrInvalidCursor) {
		return nil, workitems.ErrInvalidCursor
	}
	if err != nil {
		return nil, err
	}
	out := make([]workitems.Entry, 0, len(rows))
	for _, r := range rows {
		t := r.Ticket
		it := workitems.Item{ID: t.ID, Source: w.key, Kind: "ticket", Title: t.Title, Reference: t.Reference, Status: t.Status, Priority: t.Priority,
			UpdatedAt: t.UpdatedAt, Href: "/tickets/" + t.ID}
		if t.WaitingReason != nil {
			it.WaitingReason = *t.WaitingReason
		}
		out = append(out, workitems.Entry{Item: it, Rank: r.Rank, Cursor: r.Cursor})
	}
	return out, nil
}

// Count implements workitems.Source.
func (w *TicketWorkItems) Count(ctx context.Context, p authorization.Principal) (workitems.Count, error) {
	n, err := w.svc.MyWorkTicketCount(ctx, ticketPrincipal(p), w.src, workitems.CountCap)
	if err != nil {
		return workitems.Count{}, err
	}
	return workitems.Count{N: n, Capped: n > workitems.CountCap}, nil
}
