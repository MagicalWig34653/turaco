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
	rows, err := r.pool.Query(ctx, `SELECT `+advisoryCols+` FROM security.advisories WHERE status IN ('applicable','remediation_planned','remediating') ORDER BY id DESC LIMIT 201`)
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
	truncated := len(out) > 200
	if truncated {
		out = out[:200]
	}
	return out, truncated, nil
}
