package public

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NameLookup answers read-only display-name questions for other modules (the audit log).
type NameLookup struct{ pool *pgxpool.Pool }

// NewNameLookup returns the contract over the database pool.
func NewNameLookup(pool *pgxpool.Pool) *NameLookup { return &NameLookup{pool: pool} }

// AssetLabels returns asset id -> asset tag, or the asset reference (AST-000001) when the asset has no tag.
// Ids must be UUIDs; unknown ids are absent. One query per call; no serial numbers or assignees.
func (n *NameLookup) AssetLabels(ctx context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := n.pool.Query(ctx, `SELECT id::text, COALESCE(asset_tag, reference) FROM assets.assets WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("asset labels: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, label string
		if err := rows.Scan(&id, &label); err != nil {
			return nil, fmt.Errorf("scan asset label: %w", err)
		}
		out[id] = label
	}
	return out, rows.Err()
}
