package repository

import (
	"context"
	"fmt"
	"time"
)

// DueRiskReviews returns accepted Findings whose review date is exactly 14
// or 1 day away. UUID cursor keeps the daily scan bounded in memory.
func (r *Repository) DueRiskReviews(ctx context.Context, today time.Time, after string, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT id::text FROM security.vulnerability_findings WHERE status='risk_accepted' AND risk_review_by IN (($1::date + 14),($1::date + 1)) AND ($2::uuid IS NULL OR id > $2::uuid) ORDER BY id LIMIT $3`, today.UTC(), nilIfEmpty(after), limit)
	if err != nil {
		return nil, fmt.Errorf("due risk reviews: %w", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func nilIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}
