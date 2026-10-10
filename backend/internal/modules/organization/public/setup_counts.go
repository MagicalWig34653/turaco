package public

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SetupCounts answers the counting questions of the administrator setup checklist. Counts only, no records.
type SetupCounts struct{ pool *pgxpool.Pool }

// NewSetupCounts returns the contract over the database pool.
func NewSetupCounts(pool *pgxpool.Pool) *SetupCounts { return &SetupCounts{pool: pool} }

// TeamsWithMembers counts active Teams that have at least one current member.
func (c *SetupCounts) TeamsWithMembers(ctx context.Context) (int, error) {
	var n int
	err := c.pool.QueryRow(ctx, `SELECT count(*) FROM organization.teams t WHERE t.active AND EXISTS (
		SELECT 1 FROM organization.team_memberships m WHERE m.team_id = t.id AND (m.valid_until IS NULL OR m.valid_until > now()))`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count teams with members: %w", err)
	}
	return n, nil
}

// ActiveSites counts active Locations of kind site.
func (c *SetupCounts) ActiveSites(ctx context.Context) (int, error) {
	var n int
	if err := c.pool.QueryRow(ctx, `SELECT count(*) FROM organization.locations WHERE active AND kind = 'site'`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count sites: %w", err)
	}
	return n, nil
}
