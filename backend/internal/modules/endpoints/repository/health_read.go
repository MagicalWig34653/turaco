package repository

import (
	"context"
	"time"
)

type ProviderSyncStatus struct {
	Provider           string
	LastCompletedAt    *time.Time
	OpenProviderErrors int
}

// SyncHealth is a bounded provider read (at most 20 providers plus one for truncation).
// The sync state stores completion time only; it has no durable last-error field.
func (h *Repository) SyncHealth(ctx context.Context) ([]ProviderSyncStatus, error) {
	rows, err := h.pool.Query(ctx, `SELECT provider,last_completed_at FROM endpoints.provider_sync_state WHERE last_completed_at IS NULL OR last_completed_at <= now() - interval '24 hours' ORDER BY provider LIMIT 21`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProviderSyncStatus{}
	for rows.Next() {
		var v ProviderSyncStatus
		if err = rows.Scan(&v.Provider, &v.LastCompletedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// OpenProviderErrors counts findings reported by providers; no device details cross this contract.
func (h *Repository) OpenProviderErrors(ctx context.Context) (int, error) {
	var n int
	err := h.pool.QueryRow(ctx, `SELECT count(*) FROM endpoints.findings WHERE kind='provider_reported_error' AND status='open'`).Scan(&n)
	return n, err
}
