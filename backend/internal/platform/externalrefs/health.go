package externalrefs

import (
	"context"
	"time"
)

// Health summarizes unsynchronized references for one integration and entity
// type. It deliberately omits error text and individual record identifiers.
type Health struct {
	Failed          int
	Pending         int
	OldestFailureAt *time.Time
}

func SyncHealth(ctx context.Context, q Querier, system, entityType string) (Health, error) {
	var h Health
	err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE sync_state='failed'),
 count(*) FILTER (WHERE sync_state='pending'), min(updated_at) FILTER (WHERE sync_state='failed')
 FROM platform.external_references WHERE system=$1 AND entity_type=$2`, system, entityType).Scan(&h.Failed, &h.Pending, &h.OldestFailureAt)
	return h, err
}
