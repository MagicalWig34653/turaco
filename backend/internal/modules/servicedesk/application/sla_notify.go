package application

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

// SLANotifyFacts is what the notification consumer re-reads about a clock.
type SLANotifyFacts struct {
	ClockStatus   string
	QueueID       string
	DefaultTeamID *string
}

// SLANotifyStore re-reads a clock inside the consumer's transaction.
type SLANotifyStore interface {
	SLANotifyFacts(ctx context.Context, tx pgx.Tx, ticketID, kind string) (SLANotifyFacts, bool, error)
}

// SLARecipients resolves who may be told: the members of a Team and whether a User can still view a Queue.
type SLARecipients interface {
	TeamMemberIDs(ctx context.Context, teamID string) ([]string, error)
	MaySeeQueue(ctx context.Context, tx pgx.Tx, userID, queueID string) (bool, error)
}

// WithSLA enables the SLA notification consumers. now may be nil (the wall clock).
func (c *Consumers) WithSLA(r SLARecipients, now func() time.Time) *Consumers {
	c.slaRecipients = r
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	c.now = now
	return c
}

// OnTicketSLAWarning tells the recipients that a clock reached its warning point.
func (c *Consumers) OnTicketSLAWarning(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	return c.onSLA(ctx, tx, ev, "ticket.sla_warning")
}

// OnTicketSLABreached tells the recipients that a clock missed its target.
func (c *Consumers) OnTicketSLABreached(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	return c.onSLA(ctx, tx, ev, "ticket.sla_breached")
}

// onSLA notifies the assignee, or when there is none the active members of the Queue's default Team who can view
// the Queue. It re-reads the clock and drops the notification when the clock no longer runs, and drops events older
// than SLANotifyMaxAge. The notification carries the reference and title like ticket.assigned.
func (c *Consumers) onSLA(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent, category string) error {
	var p struct {
		TicketID  string `json:"ticketId"`
		ClockKind string `json:"clockKind"`
	}
	if err := decodePayload(ev, &p); err != nil {
		return err
	}
	ns, ok := c.store.(SLANotifyStore)
	if !ok || c.slaRecipients == nil {
		return nil
	}
	if c.now().Sub(ev.OccurredAt) > SLANotifyMaxAge {
		return nil
	}
	facts, found, err := ns.SLANotifyFacts(ctx, tx, p.TicketID, p.ClockKind)
	if err != nil || !found || facts.ClockStatus != "running" {
		return err
	}
	t, ok, err := c.load(ctx, p.TicketID)
	if err != nil || !ok {
		return err
	}
	var candidates []string
	switch {
	case t.AssigneeID != nil:
		candidates = []string{*t.AssigneeID}
	case facts.DefaultTeamID != nil:
		if candidates, err = c.slaRecipients.TeamMemberIDs(ctx, *facts.DefaultTeamID); err != nil {
			return fmt.Errorf("load default team members: %w", err)
		}
	}
	var recipients []string
	for _, id := range candidates {
		may, err := c.slaRecipients.MaySeeQueue(ctx, tx, id, facts.QueueID)
		if err != nil {
			return err
		}
		if may {
			recipients = append(recipients, id)
		}
	}
	return c.notify(ctx, tx, ev, category, t, recipients)
}
