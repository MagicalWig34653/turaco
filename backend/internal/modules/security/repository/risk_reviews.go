package repository

import (
	"context"
	"fmt"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
	"time"
)

func (r *Repository) RiskReviewsDue(ctx context.Context, from, to time.Time, afterDate time.Time, afterID string, limit int) ([]application.RiskReviewRecord, error) {
	if limit < 1 || limit > 200 {
		limit = 200
	}
	rows, err := r.pool.Query(ctx, `SELECT f.id::text,f.reference,f.advisory_id::text,f.risk_review_by FROM security.vulnerability_findings f JOIN security.advisories a ON a.id=f.advisory_id WHERE f.status='risk_accepted' AND a.status IN ('applicable','remediation_planned','remediating') AND f.risk_review_by BETWEEN $1::date AND $2::date AND ($3::date IS NULL OR (f.risk_review_by,f.id)>($3::date,$4::uuid)) ORDER BY f.risk_review_by,f.id LIMIT $5`, from, to, nilDate(afterDate), nilIfEmpty(afterID), limit)
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

func nilDate(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
