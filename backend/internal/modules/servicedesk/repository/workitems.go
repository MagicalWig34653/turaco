package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
)

var _ application.WorkStore = (*Repository)(nil)

const workRank = `((CASE priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END) + (CASE WHEN status = 'waiting' THEN 4 ELSE 0 END))`

// workWhere builds the shared predicate of a work list: the open Tickets of the list that the caller may still
// view. Everything is a bind parameter.
func workWhere(q application.WorkQuery) (string, []any) {
	args := []any{q.UserID}
	// $1 is referenced in every variant (the Global team list would otherwise leave it untyped, which PostgreSQL rejects).
	where := []string{"$1::uuid IS NOT NULL", "status IN ('new', 'open', 'in_progress', 'waiting')"}
	switch q.Source {
	case application.WorkAssigned:
		where = append(where, "assignee_user_id = $1::uuid")
	default:
		args = append(args, nonNil(q.TeamIDs))
		where = append(where, fmt.Sprintf("assignee_user_id IS NULL AND queue_team_id = ANY($%d::text[]::uuid[])", len(args)))
	}
	if !q.Global {
		args = append(args, nonNil(q.QueueIDs))
		where = append(where, fmt.Sprintf("(reporter_user_id = $1::uuid OR affected_user_id = $1::uuid OR queue_id = ANY($%d::text[]::uuid[]))", len(args)))
	}
	return strings.Join(where, " AND "), args
}

// WorkTickets implements application.WorkStore.
func (r *Repository) WorkTickets(ctx context.Context, q application.WorkQuery) ([]application.WorkRow, error) {
	if !validUUID(q.UserID) {
		return []application.WorkRow{}, nil
	}
	where, args := workWhere(q)
	if q.AfterID != "" {
		if !validUUID(q.AfterID) {
			return nil, application.ErrInvalidCursor
		}
		args = append(args, q.AfterRank, q.AfterID)
		where += fmt.Sprintf(" AND (%s, id) > ($%d::int, $%d::uuid)", workRank, len(args)-1, len(args))
	}
	args = append(args, q.Limit)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s, %s FROM servicedesk.tickets WHERE %s ORDER BY %s, id LIMIT $%d`, columns, workRank, where, workRank, len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("work tickets: %w", err)
	}
	defer rows.Close()
	out := []application.WorkRow{}
	for rows.Next() {
		var rank int
		t, err := scanWith(rows, &rank)
		if err != nil {
			return nil, fmt.Errorf("work tickets: scan: %w", err)
		}
		out = append(out, application.WorkRow{Ticket: t, Rank: rank})
	}
	return out, rows.Err()
}

// WorkCount implements application.WorkStore: at most limit+1 rows are read.
func (r *Repository) WorkCount(ctx context.Context, q application.WorkQuery, limit int) (int, error) {
	if !validUUID(q.UserID) {
		return 0, nil
	}
	where, args := workWhere(q)
	args = append(args, limit+1)
	var n int
	err := r.pool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM (SELECT 1 FROM servicedesk.tickets WHERE %s LIMIT $%d) c`, where, len(args)), args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("work count: %w", err)
	}
	return n, nil
}
