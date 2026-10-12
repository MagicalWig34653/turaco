package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

var _ application.SLASummaryStore = (*Repository)(nil)

// slaListColumns are selected after queryColumns by the list query. The state is the very expression of the sla_state
// catalog field, so the chip in a row and the filter agree. The service clears the values for callers who may not see them.
var slaListColumns = `, t.sla_response_due_at, t.sla_resolution_due_at, ` + application.SLAStateSQL()

// QueryTickets runs a compiled query plan (ADR-0033) in a read-only transaction.
func (r *Repository) QueryTickets(ctx context.Context, plan *query.Plan, visibility query.Fragment) (query.Page[application.Ticket], error) {
	return query.Run(ctx, r.pool, plan, query.Select{Columns: queryColumns + slaListColumns, Visibility: visibility},
		func(rows pgx.Rows, extra []any) (application.Ticket, error) {
			var sla application.TicketSLA
			t, err := scanWith(rows, append([]any{&sla.ResponseDueAt, &sla.ResolutionDueAt, &sla.State}, extra...)...)
			if err == nil {
				t.SLA = &sla
			}
			return t, err
		})
}

// SLASummary implements application.SLASummaryStore.
func (r *Repository) SLASummary(ctx context.Context, ticketID string) (application.TicketSLA, error) {
	var s application.TicketSLA
	err := r.pool.QueryRow(ctx, `SELECT t.sla_response_due_at, t.sla_resolution_due_at, `+application.SLAStateSQL()+` FROM servicedesk.tickets t WHERE t.id = $1::uuid`, ticketID).
		Scan(&s.ResponseDueAt, &s.ResolutionDueAt, &s.State)
	if err != nil {
		return application.TicketSLA{}, fmt.Errorf("sla summary: %w", err)
	}
	return s, nil
}
