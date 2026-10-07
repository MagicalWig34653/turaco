package public

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
)

// TicketInfo is the scope of a Ticket lookup: identity, state and the two people attached to it. No title,
// description, comments or device snapshot cross this contract.
type TicketInfo = repository.TicketFacts

// Tickets is the Service Desk's public Ticket lookup.
type Tickets struct{ repo *repository.Repository }

func NewTickets(pool *pgxpool.Pool) *Tickets { return &Tickets{repo: repository.New(pool)} }

// Ticket returns the Ticket facts; found is false for unknown ids. The caller decides who may see them.
func (t *Tickets) Ticket(ctx context.Context, id string) (TicketInfo, bool, error) {
	return t.repo.TicketFacts(ctx, id)
}

// IsOpen reports whether the status is one in which a Ticket is still worked on.
func IsOpen(status string) bool {
	switch status {
	case "new", "open", "in_progress", "waiting":
		return true
	}
	return false
}
