package repository

import (
	"context"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
	"github.com/jackc/pgx/v5"
	"time"
)

// ProgressSnapshot reads Security's counts and bounded Finding IDs in one snapshot.
func (r *Repository) ProgressSnapshot(ctx context.Context, id string) (application.Advisory, application.FindingAggregate, []string, bool, error) {
	var a application.Advisory
	out := application.FindingAggregate{ByStatus: map[string]int{}, ByConfidence: map[string]int{}}
	if !validUUID(id) {
		return a, out, nil, false, application.ErrNotFound
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return a, out, nil, false, err
	}
	defer tx.Rollback(ctx)
	a, err = scanAdvisory(tx.QueryRow(ctx, `SELECT `+advisoryCols+` FROM security.advisories WHERE id=$1::uuid`, id))
	a, err = advisoryResult(a, err, "get")
	if err != nil {
		return a, out, nil, false, err
	}
	rows, err := tx.Query(ctx, `SELECT status, confidence, count(*), min(risk_review_by) FILTER (WHERE status='risk_accepted'), min(first_seen_at) FILTER (WHERE status IN ('open','investigating','accepted','remediation_planned','remediating','risk_accepted')) FROM security.vulnerability_findings WHERE advisory_id=$1::uuid GROUP BY status,confidence`, id)
	if err != nil {
		return a, out, nil, false, err
	}
	for rows.Next() {
		var status, confidence string
		var count int
		var review, oldest *time.Time
		if err = rows.Scan(&status, &confidence, &count, &review, &oldest); err != nil {
			break
		}
		out.ByStatus[status] += count
		out.ByConfidence[confidence] += count
		out.Total += count
		if status == application.FindingRiskAccepted {
			out.Accepted += count
			if review != nil && (out.EarliestReview == nil || review.Before(*out.EarliestReview)) {
				out.EarliestReview = review
			}
		}
		if status == application.FindingOpen || status == application.FindingInvestigating || status == application.FindingAccepted || status == application.FindingRemediationPlanned || status == application.FindingRemediating || status == application.FindingRiskAccepted {
			if confidence == application.ConfidenceProbable {
				out.Probable += count
			} else {
				out.Potential += count
			}
			if oldest != nil && (out.Oldest == nil || oldest.Before(*out.Oldest)) {
				out.Oldest = oldest
			}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return a, out, nil, false, err
	}
	ids := []string{}
	rows, err = tx.Query(ctx, `SELECT id::text FROM security.vulnerability_findings WHERE advisory_id=$1::uuid ORDER BY id LIMIT 20001`, id)
	if err != nil {
		return a, out, nil, false, err
	}
	for rows.Next() {
		var v string
		if err = rows.Scan(&v); err != nil {
			break
		}
		ids = append(ids, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return a, out, nil, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return a, out, nil, false, err
	}
	truncated := len(ids) > 20000
	if truncated {
		ids = ids[:20000]
	}
	return a, out, ids, truncated, nil
}
