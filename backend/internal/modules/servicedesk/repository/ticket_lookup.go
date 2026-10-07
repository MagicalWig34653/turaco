package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// TicketFacts are the few Ticket facts other modules may read.
type TicketFacts struct {
	ID             string
	Reference      string
	Status         string
	AffectedUserID string
	ReporterUserID string
	// AssigneeUserID is empty when the Ticket is unassigned.
	AssigneeUserID string
}

// TicketFacts returns the facts of a Ticket (found false when unknown or the id is no UUID).
func (b *Repository) TicketFacts(ctx context.Context, id string) (TicketFacts, bool, error) {
	var t TicketFacts
	if !validUUID(id) {
		return t, false, nil
	}
	err := b.pool.QueryRow(ctx, `SELECT id::text, reference, status, affected_user_id::text, reporter_user_id::text, COALESCE(assignee_user_id::text, '')
		FROM servicedesk.tickets WHERE id = $1::uuid`, id).Scan(&t.ID, &t.Reference, &t.Status, &t.AffectedUserID, &t.ReporterUserID, &t.AssigneeUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return TicketFacts{}, false, nil
	}
	if err != nil {
		return TicketFacts{}, false, fmt.Errorf("ticket facts: %w", err)
	}
	return t, true, nil
}
