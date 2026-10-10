package externalrefs

import (
	"context"
	"time"
)

// LastActivity returns when a reference of the integration last synchronized successfully and when one last failed.
// Both are nil when there was none. It carries no error text and no record identifiers.
func LastActivity(ctx context.Context, q Querier, system, entityType string) (lastSynced, lastFailed *time.Time, err error) {
	err = q.QueryRow(ctx, `SELECT max(last_synced_at), max(updated_at) FILTER (WHERE sync_state='failed')
 FROM platform.external_references WHERE system=$1 AND entity_type=$2`, system, entityType).Scan(&lastSynced, &lastFailed)
	return lastSynced, lastFailed, err
}
