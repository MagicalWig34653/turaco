package repository

import (
	"context"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
)

func (r *Repository) AffectedDeviceCounts(ctx context.Context, ids []string) (map[string]int, error) {
	out := map[string]int{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT advisory_id::text,count(DISTINCT device_id) FROM security.vulnerability_findings WHERE advisory_id=ANY($1::text[]::uuid[]) AND status IN ('open','investigating','accepted','remediation_planned','remediating','risk_accepted') GROUP BY advisory_id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			return nil, err
		}
		out[id] = count
	}
	return out, rows.Err()
}

func (r *Repository) ApplicableAdvisories(ctx context.Context) ([]application.Advisory, bool, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+advisoryCols+` FROM security.advisories WHERE status IN ('applicable','remediation_planned','remediating') ORDER BY known_exploited DESC, CASE severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END, id DESC LIMIT 21`)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []application.Advisory{}
	for rows.Next() {
		a, e := scanAdvisory(rows)
		if e != nil {
			return nil, false, e
		}
		out = append(out, a)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(out) > 20
	if truncated {
		out = out[:20]
	}
	return out, truncated, nil
}

// DeploymentAdvisories reads the applicable Advisories with open Findings for a product on a set of Devices (F9 G4).
func (r *Repository) DeploymentAdvisories(ctx context.Context, productID string, deviceIDs []string, limit int) ([]application.DeploymentAdvisory, application.DeploymentContextTotals, error) {
	var totals application.DeploymentContextTotals
	const scope = `FROM security.vulnerability_findings f JOIN security.advisories a ON a.id = f.advisory_id
		WHERE f.software_product_id = $1::uuid AND f.device_id = ANY($2::uuid[])
		  AND f.status IN ('open', 'investigating', 'accepted', 'remediation_planned', 'remediating')
		  AND a.status IN ('applicable', 'remediation_planned', 'remediating')`
	if err := r.pool.QueryRow(ctx, `SELECT count(DISTINCT f.advisory_id), count(*) `+scope, productID, deviceIDs).Scan(&totals.Advisories, &totals.Findings); err != nil {
		return nil, totals, err
	}
	rows, err := r.pool.Query(ctx, `SELECT a.id::text, a.reference, a.title, a.severity, a.status, a.known_exploited, count(*), count(DISTINCT f.device_id) `+scope+`
		GROUP BY a.id ORDER BY a.known_exploited DESC, CASE a.severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END, a.id DESC LIMIT $3`,
		productID, deviceIDs, limit)
	if err != nil {
		return nil, totals, err
	}
	defer rows.Close()
	out := []application.DeploymentAdvisory{}
	for rows.Next() {
		var a application.DeploymentAdvisory
		if err := rows.Scan(&a.ID, &a.Reference, &a.Title, &a.Severity, &a.Status, &a.KnownExploited, &a.OpenFindings, &a.AffectedDevices); err != nil {
			return nil, totals, err
		}
		out = append(out, a)
	}
	return out, totals, rows.Err()
}
