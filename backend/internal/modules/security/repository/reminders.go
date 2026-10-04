package repository

import (
	"context"
	"fmt"
	"time"
)

// DueRiskReviews pages accepted Findings within the reminder window.
func (r *Repository) DueRiskReviews(ctx context.Context, today time.Time, after string, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT f.id::text FROM security.vulnerability_findings f JOIN security.advisories a ON a.id=f.advisory_id WHERE f.status='risk_accepted' AND a.status IN ('applicable','remediation_planned','remediating') AND f.risk_review_by BETWEEN $1::date AND ($1::date + 14) AND ($2::uuid IS NULL OR f.id > $2::uuid) ORDER BY f.id LIMIT $3`, today.UTC(), nilIfEmpty(after), limit)
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
