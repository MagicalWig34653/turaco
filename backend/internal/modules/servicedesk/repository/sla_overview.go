package repository

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
)

// nextToBreach lists the open Tickets whose running clocks breach soonest: the nearest due time still ahead of
// now. A clock already past due is a breach (counted, and listed under its filter), not "next". Each row carries
// only what a list row shows, like the other overview lists.
func nextToBreach(ctx context.Context, tx pgx.Tx, queues []string, q application.OverviewQuery) ([]application.OverviewTicket, error) {
	const next = `least(CASE WHEN sla_response_due_at > now() THEN sla_response_due_at END, CASE WHEN sla_resolution_due_at > now() THEN sla_resolution_due_at END)`
	rows, err := tx.Query(ctx, `
		SELECT id::text, reference, title, priority, status, queue_id::text, assignee_user_id::text, created_at, updated_at, `+next+`
		FROM servicedesk.tickets WHERE `+overviewOpen+` AND ($2::bool OR queue_id = ANY($1::text[]::uuid[])) AND `+next+` IS NOT NULL
		ORDER BY `+next+`, id LIMIT $3`, queues, q.Global, q.OldestLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []application.OverviewTicket{}
	for rows.Next() {
		var t application.OverviewTicket
		if err := rows.Scan(&t.ID, &t.Reference, &t.Title, &t.Priority, &t.Status, &t.QueueID, &t.AssigneeID, &t.CreatedAt, &t.UpdatedAt, &t.SLADueAt); err != nil {
			return nil, err
		}
		list = append(list, t)
	}
	return list, rows.Err()
}
