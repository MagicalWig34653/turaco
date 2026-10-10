package roles

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RoleNames returns role id -> role name for the given ids, including soft-deleted roles (the audit log still
// shows their history). Ids must be UUIDs; unknown ids are absent. One query per call; names only.
func RoleNames(ctx context.Context, pool *pgxpool.Pool, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := pool.Query(ctx, `SELECT id::text, name FROM platform.roles WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("role names: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("scan role name: %w", err)
		}
		out[id] = name
	}
	return out, rows.Err()
}
