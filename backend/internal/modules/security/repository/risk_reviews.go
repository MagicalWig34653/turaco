package repository

import (
	"context"
	"fmt"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
	"time"
)

func (r *Repository) RiskReviewsDue(ctx context.Context, from, to time.Time) ([]application.RiskReviewRecord, error) {
	rows, err := r.pool.Query(ctx, `SELECT id::text,reference,advisory_id::text,risk_review_by FROM security.vulnerability_findings WHERE status='risk_accepted' AND risk_review_by BETWEEN $1::date AND $2::date ORDER BY risk_review_by,id`, from, to)
	if err != nil {
		return nil, fmt.Errorf("risk reviews due: %w", err)
	}
	defer rows.Close()
	out := []application.RiskReviewRecord{}
	for rows.Next() {
		var v application.RiskReviewRecord
		if err := rows.Scan(&v.FindingID, &v.FindingReference, &v.AdvisoryID, &v.ReviewBy); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
