package repository

import (
	"context"
	"fmt"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
	"time"
)

// OverviewCounts reads Security-owned counts from one SQL statement.
func (r *Repository) OverviewCounts(ctx context.Context, now time.Time) (application.Overview, error) {
	out := application.Overview{ApplicableBySeverity: map[string]int{}, OpenFindingsByConfidence: map[string]int{}}
	for _, v := range application.Severities {
		out.ApplicableBySeverity[v] = 0
	}
	for _, v := range application.Confidences {
		out.OpenFindingsByConfidence[v] = 0
	}
	rows, err := r.pool.Query(ctx, `WITH live AS (SELECT id,severity FROM security.advisories WHERE status IN ('applicable','remediation_planned','remediating'))
 SELECT 'advisory',severity,count(*) FROM live GROUP BY severity
 UNION ALL SELECT 'finding',f.confidence,count(*) FROM security.vulnerability_findings f JOIN live a ON a.id=f.advisory_id WHERE f.status IN ('open','investigating','accepted','remediation_planned','remediating','risk_accepted') GROUP BY f.confidence
 UNION ALL SELECT 'review','',count(*) FROM security.vulnerability_findings f JOIN live a ON a.id=f.advisory_id WHERE f.status='risk_accepted' AND f.risk_review_by BETWEEN $1::date AND ($1::date+30)`, now.UTC())
	if err != nil {
		return out, fmt.Errorf("overview counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind, key string
		var count int
		if err = rows.Scan(&kind, &key, &count); err != nil {
			return out, err
		}
		switch kind {
		case "advisory":
			out.ApplicableBySeverity[key] = count
		case "finding":
			out.OpenFindingsByConfidence[key] = count
		case "review":
			out.RiskAcceptancesDueWithin30Days = count
		}
	}
	return out, rows.Err()
}

// ApplicableTaskContextIDs supplies only IDs; Tasks owns the overdue predicate.
func (r *Repository) ApplicableTaskContextIDs(ctx context.Context) (map[string][]string, error) {
	out := map[string][]string{"security_advisory": {}, "security_finding": {}}
	rows, err := r.pool.Query(ctx, `SELECT 'security_advisory',id::text FROM security.advisories WHERE status IN ('applicable','remediation_planned','remediating') UNION ALL SELECT 'security_finding',f.id::text FROM security.vulnerability_findings f JOIN security.advisories a ON a.id=f.advisory_id WHERE a.status IN ('applicable','remediation_planned','remediating')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var typ, id string
		if err := rows.Scan(&typ, &id); err != nil {
			return nil, err
		}
		out[typ] = append(out[typ], id)
	}
	return out, rows.Err()
}
