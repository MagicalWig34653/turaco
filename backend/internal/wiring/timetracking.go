package wiring

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	servicedeskpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/public"
	timetrackingapp "github.com/MagicalWig34653/turaco/backend/internal/modules/timetracking/application"
	timetrackingrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/timetracking/repository"
)

// TimeTracking builds the Time Tracking service (F17 T-A) over Service Desk's Ticket access contract and
// Organization's work directory.
func TimeTracking(pool *pgxpool.Pool) *timetrackingapp.Service {
	return timetrackingapp.NewService(timetrackingrepository.New(pool),
		timeTickets{servicedeskpublic.NewTimeAccess(ServiceDesk(pool))},
		orgpublic.NewWorkDirectory(orgrepository.New(pool)))
}

// timeTickets adapts Service Desk's TimeAccess contract to Time Tracking's Tickets port.
type timeTickets struct{ *servicedeskpublic.TimeAccess }

func (t timeTickets) Access(ctx context.Context, userID string, ticketIDs []string) (map[string]timetrackingapp.TicketAccess, error) {
	in, err := t.TimeAccess.Tickets(ctx, userID, ticketIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]timetrackingapp.TicketAccess, len(in))
	for id, a := range in {
		out[id] = timetrackingapp.TicketAccess{Found: a.Found, Reference: a.Reference, QueueID: a.QueueID, CanView: a.CanView, CanWork: a.CanWork}
	}
	return out, nil
}

func (t timeTickets) ViewQueues(ctx context.Context, userID string) (bool, []string, error) {
	return t.TimeAccess.ViewQueues(ctx, userID)
}
