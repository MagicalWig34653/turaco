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

// AdvisoryLabels returns advisory id -> its identifier (CVE or bulletin id from the source) when that has the shape
// of an identifier, otherwise the advisory reference. Never the title, summary or any other free text. Ids must be
// UUIDs; unknown ids are absent.
func (n *NameLookup) AdvisoryLabels(ctx context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := n.pool.Query(ctx, `
		SELECT id::text, CASE WHEN external_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$' THEN external_id ELSE reference END
		FROM security.advisories WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("advisory labels: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, label string
		if err := rows.Scan(&id, &label); err != nil {
			return nil, fmt.Errorf("scan advisory label: %w", err)
		}
		out[id] = label
	}
	return out, rows.Err()
}
