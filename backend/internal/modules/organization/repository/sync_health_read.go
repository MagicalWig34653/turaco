package repository

import (
	"context"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
)

// DirectorySyncStatus returns at most 21 providers and omits raw error text.
func (h *Repository) DirectorySyncStatus(ctx context.Context, scope orgpublic.SyncScope) ([]orgpublic.DirectorySyncStatus, error) {
	rows, err := h.pool.Query(ctx, `SELECT p.provider_key,
 (SELECT max(finished_at) FROM organization.directory_sync_runs s WHERE s.provider_key=p.provider_key AND outcome='succeeded'),
 (SELECT max(finished_at) FROM organization.directory_sync_runs s WHERE s.provider_key=p.provider_key AND outcome='failed'),
 (SELECT left(error,500) FROM organization.directory_sync_runs s WHERE s.provider_key=p.provider_key AND outcome='failed' ORDER BY finished_at DESC,id DESC LIMIT 1)
 FROM (SELECT DISTINCT provider_key FROM organization.directory_sync_runs) p ORDER BY p.provider_key LIMIT 21`)
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
		if v.LastFailureAt != nil && (v.LastSuccessAt == nil || v.LastFailureAt.After(*v.LastSuccessAt)) {
			v.LastErrorCode = "sync_failed"
			if scope.IncludeErrorSummary && summary != nil {
				v.LastErrorSummary = *summary
			}
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
