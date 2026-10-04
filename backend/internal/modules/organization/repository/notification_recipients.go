package repository

import (
	"context"
	"fmt"
)

// ActiveUserIDs pages active Users by id for permission-filtered notification fan-out.
func (r *Repository) ActiveUserIDs(ctx context.Context, after string, limit int) ([]string, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := r.pool.Query(ctx, `SELECT id::text FROM organization.users
		WHERE status = 'active' AND id > COALESCE(NULLIF($1, '')::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		ORDER BY id LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list notification recipients: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan notification recipient: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
