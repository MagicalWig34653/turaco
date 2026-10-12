package public

import (
	"context"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
)

// TimeAccessInfo is the Ticket access answer for Time Tracking.
type TimeAccessInfo = application.TicketTimeAccess

// TimeAccess is the Service Desk contract of the Time Tracking module (F17 T-A): which of the given Tickets a User may
// view and work, and the Queue each is in. The caller decides what to do with it.
type TimeAccess struct{ svc *application.Service }

// NewTimeAccess wraps the Ticket service.
func NewTimeAccess(svc *application.Service) *TimeAccess { return &TimeAccess{svc: svc} }

// Tickets returns the access of the User per Ticket id (every requested id has an entry).
func (t *TimeAccess) Tickets(ctx context.Context, userID string, ticketIDs []string) (map[string]TimeAccessInfo, error) {
	return t.svc.TimeAccess(ctx, userID, ticketIDs)
}
