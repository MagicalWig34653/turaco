package public

import (
	"context"
	"errors"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/attachments"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

// TicketAttachmentOwner is the owner type of file attachments on Tickets.
const TicketAttachmentOwner = "ticket"

const (
	permTicketsView   = "tickets.view"
	permTicketsManage = "tickets.manage"
)

// TicketAttachments authorizes the attachments of Tickets for the platform attachment service (ADR-0037): the
// reporter and affected User may attach and read like they may comment, staff read according to Queue access,
// people who work the Queue attach, and attachments of internal comments (audience privileged) stay staff only.
type TicketAttachments struct{ svc *application.Service }

// NewTicketAttachments builds the owner for the Ticket service.
func NewTicketAttachments(svc *application.Service) *TicketAttachments {
	return &TicketAttachments{svc: svc}
}

// Access implements attachments.Owner.
func (t *TicketAttachments) Access(ctx context.Context, p authorization.Principal, ticketID string) (attachments.Access, error) {
	r, err := t.svc.AttachmentRights(ctx, application.Principal{
		UserID: p.UserID, View: p.Has(permTicketsView), Manage: p.Has(permTicketsManage), QueuesManage: p.Has(application.PermQueuesManage),
		ChangesView: p.Has("changes.view") || p.Has("changes.manage") || p.Has("changes.execute"),
	}, ticketID)
	if errors.Is(err, application.ErrNotFound) {
		return attachments.Access{}, attachments.ErrOwnerNotFound
	}
	if err != nil {
		return attachments.Access{}, err
	}
	return attachments.Access{Read: r.Read, Attach: r.Attach, Privileged: r.Internal, Manage: r.Moderate}, nil
}
