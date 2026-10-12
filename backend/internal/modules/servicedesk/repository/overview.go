package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
)

var _ application.OverviewStore = (*Repository)(nil)

// overviewOpen selects the open Tickets. Every overview query is also limited to the viewable Queues (all of them
// for a global viewer); Tickets the caller sees only as reporter or affected User are deliberately not included.
const overviewOpen = `status IN ('new', 'open', 'in_progress', 'waiting')`

// TicketOverview implements application.OverviewStore. All reads share one snapshot, so the totals agree.
func (r *Repository) TicketOverview(ctx context.Context, q application.OverviewQuery) (application.OverviewData, error) {
	out := application.OverviewData{}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, fmt.Errorf("begin overview: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// $1 is the Queue id list and $2 says whether it is ignored.
	queues := nonNil(q.QueueIDs)
	scope := `($2::bool OR queue_id = ANY($1::text[]::uuid[]))`

	// The SLA counts use the very expression of the sla_state list field, so each number equals its filtered list.
	atRisk, breached := "0", "0"
	if q.SLA {
		state := application.SLAStateSQL()
		atRisk, breached = "count(*) FILTER (WHERE "+state+" = 'at_risk')", "count(*) FILTER (WHERE "+state+" = 'breached')"
	}
	rows, err := tx.Query(ctx, `
		SELECT queue_id::text, count(*), count(*) FILTER (WHERE assignee_user_id IS NULL),
		       count(*) FILTER (WHERE status = 'waiting'), min(created_at), `+atRisk+`, `+breached+`
		FROM servicedesk.tickets t WHERE `+overviewOpen+` AND `+scope+`
		GROUP BY queue_id ORDER BY queue_id`, queues, q.Global)
	if err != nil {
		return out, fmt.Errorf("overview queues: %w", err)
	}
	for rows.Next() {
		var c application.OverviewQueueCounts
		if err := rows.Scan(&c.QueueID, &c.Open, &c.Unassigned, &c.Waiting, &c.OldestOpenAt, &c.AtRisk, &c.Breached); err != nil {
			rows.Close()
			return out, fmt.Errorf("overview queues: scan: %w", err)
		}
		out.Queues = append(out.Queues, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("overview queues: %w", err)
	}

	// One extra row tells whether the list was cut.
	rows, err = tx.Query(ctx, `
		SELECT assignee_user_id::text, count(*), count(*) FILTER (WHERE status = 'waiting')
		FROM servicedesk.tickets WHERE `+overviewOpen+` AND assignee_user_id IS NOT NULL AND `+scope+`
		GROUP BY assignee_user_id ORDER BY count(*) DESC, assignee_user_id LIMIT $3`, queues, q.Global, q.AssigneeLimit+1)
	if err != nil {
		return out, fmt.Errorf("overview assignees: %w", err)
	}
	for rows.Next() {
		var c application.OverviewAssigneeCount
		if err := rows.Scan(&c.UserID, &c.Open, &c.Waiting); err != nil {
			rows.Close()
			return out, fmt.Errorf("overview assignees: scan: %w", err)
		}
		out.Assignees = append(out.Assignees, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("overview assignees: %w", err)
	}
	if len(out.Assignees) > q.AssigneeLimit {
		out.Assignees, out.AssigneesTruncated = out.Assignees[:q.AssigneeLimit], true
	}

	oldest := func(where, order string) ([]application.OverviewTicket, error) {
		rows, err := tx.Query(ctx, `
			SELECT id::text, reference, title, priority, status, queue_id::text, assignee_user_id::text, created_at, updated_at
			FROM servicedesk.tickets WHERE `+where+` AND `+scope+`
			ORDER BY `+order+`, id LIMIT $3`, queues, q.Global, q.OldestLimit)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		list := []application.OverviewTicket{}
		for rows.Next() {
			var t application.OverviewTicket
			if err := rows.Scan(&t.ID, &t.Reference, &t.Title, &t.Priority, &t.Status, &t.QueueID, &t.AssigneeID, &t.CreatedAt, &t.UpdatedAt); err != nil {
				return nil, err
			}
			list = append(list, t)
		}
		return list, rows.Err()
	}
	if out.OldestUnassigned, err = oldest(overviewOpen+` AND assignee_user_id IS NULL`, `created_at`); err != nil {
		return out, fmt.Errorf("overview oldest unassigned: %w", err)
	}
	if out.OldestWaiting, err = oldest(`status = 'waiting'`, `updated_at`); err != nil {
		return out, fmt.Errorf("overview oldest waiting: %w", err)
	}
	if q.SLA {
		if out.NextToBreach, err = nextToBreach(ctx, tx, queues, q); err != nil {
			return out, fmt.Errorf("overview next to breach: %w", err)
		}
	}
	return out, nil
}
