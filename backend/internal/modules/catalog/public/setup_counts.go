package public

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SetupCounts answers the counting questions of the administrator setup checklist. Counts only.
type SetupCounts struct{ pool *pgxpool.Pool }

// NewSetupCounts returns the contract over the database pool.
func NewSetupCounts(pool *pgxpool.Pool) *SetupCounts { return &SetupCounts{pool: pool} }

// ActiveItems counts active Catalog Items.
func (c *SetupCounts) ActiveItems(ctx context.Context) (int, error) {
	var n int
	if err := c.pool.QueryRow(ctx, `SELECT count(*) FROM catalog.items WHERE active`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count catalog items: %w", err)
	}
	return n, nil
}
