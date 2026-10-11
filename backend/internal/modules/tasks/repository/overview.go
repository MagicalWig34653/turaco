package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
)

var _ application.OverviewStore = (*Repository)(nil)

const overviewOpen = `status IN ('open', 'in_progress', 'blocked')`

// TaskOverview implements application.OverviewStore. All reads share one snapshot, so the totals agree.
func (r *Repository) TaskOverview(ctx context.Context, q application.OverviewQuery) (application.OverviewData, error) {
	out := application.OverviewData{}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, fmt.Errorf("begin overview: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	err = tx.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE due_at < $1), count(*) FILTER (WHERE status = 'blocked'),
		       count(*) FILTER (WHERE assigned_user_id IS NULL AND assigned_team_id IS NULL)
		FROM platform.tasks WHERE `+overviewOpen, q.Now).
		Scan(&out.Totals.Open, &out.Totals.Overdue, &out.Totals.Blocked, &out.Totals.Unowned)
	if err != nil {
		return out, fmt.Errorf("overview totals: %w", err)
	}

	owners := func(column string) ([]application.OverviewOwner, bool, error) {
		// One extra row tells whether the list was cut.
		rows, err := tx.Query(ctx, `
			SELECT `+column+`::text, count(*), count(*) FILTER (WHERE due_at < $1)
			FROM platform.tasks WHERE `+overviewOpen+` AND `+column+` IS NOT NULL
			GROUP BY `+column+` ORDER BY count(*) FILTER (WHERE due_at < $1) DESC, count(*) DESC, `+column+` LIMIT $2`,
			q.Now, q.OwnerLimit+1)
		if err != nil {
			return nil, false, err
		}
		defer rows.Close()
		list := []application.OverviewOwner{}
		for rows.Next() {
			var o application.OverviewOwner
			if err := rows.Scan(&o.ID, &o.Open, &o.Overdue); err != nil {
				return nil, false, err
			}
			list = append(list, o)
		}
		if err := rows.Err(); err != nil {
			return nil, false, err
		}
		if len(list) > q.OwnerLimit {
			return list[:q.OwnerLimit], true, nil
		}
		return list, false, nil
	}
	if out.Users, out.UsersTruncated, err = owners("assigned_user_id"); err != nil {
		return out, fmt.Errorf("overview users: %w", err)
	}
	if out.Teams, out.TeamsTruncated, err = owners("assigned_team_id"); err != nil {
		return out, fmt.Errorf("overview teams: %w", err)
	}

	oldest := func(where, order string, args ...any) ([]application.Task, error) {
		args = append(args, q.OldestLimit)
		rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT %s FROM platform.tasks WHERE %s ORDER BY %s, id LIMIT $%d`, columns, where, order, len(args)), args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		list := []application.Task{}
		for rows.Next() {
			t, err := scanWith(rows)
			if err != nil {
				return nil, err
			}
			list = append(list, t)
		}
		return list, rows.Err()
	}
	if out.OldestOverdue, err = oldest(overviewOpen+` AND due_at < $1`, `due_at`, q.Now); err != nil {
		return out, fmt.Errorf("overview oldest overdue: %w", err)
	}
	if out.OldestUnowned, err = oldest(overviewOpen+` AND assigned_user_id IS NULL AND assigned_team_id IS NULL`, `created_at`); err != nil {
		return out, fmt.Errorf("overview oldest unowned: %w", err)
	}
	return out, nil
}
