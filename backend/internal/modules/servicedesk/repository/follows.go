package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Following defaults to true so existing requester notifications keep working.
func (r *Repository) Following(ctx context.Context, ticketID, userID string) (bool, error) {
	var following bool
	err := r.pool.QueryRow(ctx, `SELECT COALESCE((SELECT following FROM servicedesk.ticket_follows WHERE ticket_id = $1::uuid AND user_id = $2::uuid), true)`, ticketID, userID).Scan(&following)
	if err != nil {
		return false, fmt.Errorf("read ticket follow: %w", err)
	}
	return following, nil
}

func (r *Repository) FollowingTx(ctx context.Context, tx pgx.Tx, ticketID, userID string) (bool, error) {
	var following bool
	err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT following FROM servicedesk.ticket_follows WHERE ticket_id = $1::uuid AND user_id = $2::uuid), true)`, ticketID, userID).Scan(&following)
	if err != nil {
		return false, fmt.Errorf("read ticket follow: %w", err)
	}
	return following, nil
}

func (r *Repository) SetFollowingTx(ctx context.Context, tx pgx.Tx, ticketID, userID string, following bool) error {
	_, err := tx.Exec(ctx, `INSERT INTO servicedesk.ticket_follows(ticket_id, user_id, following) VALUES ($1::uuid, $2::uuid, $3)
        ON CONFLICT (ticket_id, user_id) DO UPDATE SET following = EXCLUDED.following, updated_at = now()`, ticketID, userID, following)
	if err != nil {
		return fmt.Errorf("set ticket follow: %w", err)
	}
	return nil
}
