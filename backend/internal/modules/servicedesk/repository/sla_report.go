package repository

import (
	"context"
	"fmt"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
)

var _ application.SLAReportStore = (*Repository)(nil)

// SLAReport implements application.SLAReportStore. A finished clock is a completed clock (measured: met when it
// carries no breach, otherwise breached) at its completion time, or a clock cancelled as exempted at the time of that
// transition; the other cancellations (cancelled Ticket, no target) were never measured and are not reported. The
// group is the Ticket's current Queue or Priority, or the UTC month of the event. Nothing is grouped or filtered by
// person (ADR-0041 D5).
func (r *Repository) SLAReport(ctx context.Context, q application.SLAReportQuery) ([]application.SLAReportRow, error) {
	var group string
	switch q.GroupBy {
	case application.SLAGroupQueue:
		group = `t.queue_id::text`
	case application.SLAGroupPriority:
		group = `t.priority`
	case application.SLAGroupMonth:
		group = `to_char(e.at AT TIME ZONE 'UTC', 'YYYY-MM')`
	default:
		return nil, fmt.Errorf("unknown grouping %q", q.GroupBy)
	}
	rows, err := r.pool.Query(ctx, `
		WITH e AS (
			SELECT c.ticket_id, c.kind, c.completed_at AS at, true AS done, c.breached_at IS NOT NULL AS breached, c.elapsed_seconds
			FROM servicedesk.ticket_sla_clocks c
			WHERE c.status = 'completed' AND c.completed_at >= $1 AND c.completed_at < $2
			UNION ALL
			SELECT c.ticket_id, c.kind, max(tr.occurred_at), false, false, 0
			FROM servicedesk.ticket_sla_clocks c
			JOIN servicedesk.ticket_sla_clock_transitions tr ON tr.ticket_id = c.ticket_id AND tr.kind = c.kind AND tr.to_status = 'cancelled'
			WHERE c.status = 'cancelled' AND c.end_reason = 'exempted' AND tr.occurred_at >= $1 AND tr.occurred_at < $2
			GROUP BY c.ticket_id, c.kind
		)
		SELECT `+group+`, e.kind,
		       count(*) FILTER (WHERE e.done),
		       count(*) FILTER (WHERE e.done AND NOT e.breached),
		       count(*) FILTER (WHERE e.done AND e.breached),
		       count(*) FILTER (WHERE NOT e.done),
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY e.elapsed_seconds) FILTER (WHERE e.done),
		       percentile_cont(0.9) WITHIN GROUP (ORDER BY e.elapsed_seconds) FILTER (WHERE e.done)
		FROM e JOIN servicedesk.tickets t ON t.id = e.ticket_id
		WHERE ($3::bool OR t.queue_id = ANY($4::text[]::uuid[]))
		GROUP BY 1, 2 ORDER BY 1, 2`, q.From, q.To, q.Global, nonNil(q.QueueIDs))
	if err != nil {
		return nil, fmt.Errorf("sla report: %w", err)
	}
	defer rows.Close()
	out := []application.SLAReportRow{}
	for rows.Next() {
		var row application.SLAReportRow
		if err := rows.Scan(&row.Group, &row.Kind, &row.Measured, &row.Met, &row.Breached, &row.Exempted, &row.MedianSeconds, &row.P90Seconds); err != nil {
			return nil, fmt.Errorf("sla report: scan: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
