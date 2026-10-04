package repository

import (
	"context"
	"fmt"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
	"time"
)

// OverviewCounts reads only Security-owned records; Task counts come through
// the Tasks public contract in the application service.
func (r *Repository) OverviewCounts(ctx context.Context, now time.Time) (application.Overview, error) {
	out := application.Overview{ApplicableBySeverity: map[string]int{}, OpenFindingsByConfidence: map[string]int{}}
	for _, severity := range application.Severities {
		out.ApplicableBySeverity[severity] = 0
	}
	for _, confidence := range application.Confidences {
		out.OpenFindingsByConfidence[confidence] = 0
	}
	rows, err := r.pool.Query(ctx, `SELECT severity,count(*) FROM security.advisories WHERE status IN ('applicable','remediation_planned','remediating') GROUP BY severity`)
	if err != nil {
		return out, fmt.Errorf("overview advisories: %w", err)
	}
	for rows.Next() {
		var severity string
		var count int
		if err = rows.Scan(&severity, &count); err != nil {
			rows.Close()
			return out, err
		}
		out.ApplicableBySeverity[severity] = count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = r.pool.Query(ctx, `SELECT f.confidence,count(*) FROM security.vulnerability_findings f JOIN security.advisories a ON a.id=f.advisory_id WHERE a.status IN ('applicable','remediation_planned','remediating') AND f.status IN ('open','investigating','accepted','remediation_planned','remediating','risk_accepted') GROUP BY f.confidence`)
	if err != nil {
		return out, fmt.Errorf("overview findings: %w", err)
	}
	for rows.Next() {
		var confidence string
		var count int
		if err = rows.Scan(&confidence, &count); err != nil {
			rows.Close()
			return out, err
		}
		out.OpenFindingsByConfidence[confidence] = count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	err = r.pool.QueryRow(ctx, `SELECT count(*) FROM security.vulnerability_findings f JOIN security.advisories a ON a.id=f.advisory_id WHERE a.status IN ('applicable','remediation_planned','remediating') AND f.status='risk_accepted' AND f.risk_review_by >= $1::date AND f.risk_review_by <= ($1::date + 30)`, now.UTC()).Scan(&out.RiskAcceptancesDueWithin30Days)
	return out, err
}
