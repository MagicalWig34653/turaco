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

// ActiveQueuesWithTeam counts active Ticket Queues that have a default Team.
func (c *SetupCounts) ActiveQueuesWithTeam(ctx context.Context) (int, error) {
	var n int
	if err := c.pool.QueryRow(ctx, `SELECT count(*) FROM servicedesk.queues WHERE status = 'active' AND default_team_id IS NOT NULL`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count queues: %w", err)
	}
	return n, nil
}
