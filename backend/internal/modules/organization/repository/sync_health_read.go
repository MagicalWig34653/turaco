package repository

import (
	"context"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
)

// DirectorySyncStatus returns at most 21 providers and omits raw error text.
func (h *Repository) DirectorySyncStatus(ctx context.Context, scope orgpublic.SyncScope) ([]orgpublic.DirectorySyncStatus, error) {
	summaryColumn := "NULL::text"
	failureColumn := "NULL::text AS error"
	if scope.IncludeErrorSummary {
		summaryColumn = "left(failure.error,500)"
		failureColumn = "error"
	}
	rows, err := h.pool.Query(ctx, `WITH RECURSIVE providers AS (
 SELECT min(provider_key) AS provider_key FROM organization.directory_sync_runs
 UNION ALL
 SELECT (SELECT min(provider_key) FROM organization.directory_sync_runs WHERE provider_key > p.provider_key)
 FROM providers p WHERE p.provider_key IS NOT NULL
 )
 SELECT p.provider_key, success.finished_at, failure.finished_at,
 `+summaryColumn+`
 FROM providers p
 LEFT JOIN LATERAL (
 SELECT id, finished_at FROM organization.directory_sync_runs s
 WHERE s.provider_key=p.provider_key AND outcome='succeeded' ORDER BY id DESC LIMIT 1
 ) success ON true
 LEFT JOIN LATERAL (
 SELECT id, finished_at, `+failureColumn+` FROM organization.directory_sync_runs f
 WHERE f.provider_key=p.provider_key AND outcome='failed' ORDER BY id DESC LIMIT 1
 ) failure ON true
 WHERE p.provider_key IS NOT NULL AND failure.id IS NOT NULL
 AND (success.id IS NULL OR failure.id > success.id)
 ORDER BY p.provider_key LIMIT 21`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []orgpublic.DirectorySyncStatus{}
	for rows.Next() {
		var v orgpublic.DirectorySyncStatus
		var summary *string
		if err = rows.Scan(&v.Provider, &v.LastSuccessAt, &v.LastFailureAt, &summary); err != nil {
			return nil, err
		}
		v.LastErrorCode = "sync_failed"
		if scope.IncludeErrorSummary && summary != nil {
			v.LastErrorSummary = *summary
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
