package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
)

var _ application.ParticipantStore = (*Repository)(nil)

// ParticipantLevel implements application.ParticipantStore.
func (r *Repository) ParticipantLevel(ctx context.Context, ticketID, userID string) (string, bool, error) {
	if !validUUID(ticketID) || !validUUID(userID) {
		return "", false, nil
	}
	var level string
	err := r.pool.QueryRow(ctx, `
		SELECT level FROM servicedesk.ticket_participants
		WHERE ticket_id = $1::uuid AND user_id = $2::uuid AND (expires_at IS NULL OR expires_at > now())`, ticketID, userID).Scan(&level)
	if err == pgx.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("participant level: %w", err)
	}
	return level, true, nil
}

// UpsertParticipantTx implements application.ParticipantStore. The Ticket row is locked by the caller, so the
// participant count cannot race.
func (r *Repository) UpsertParticipantTx(ctx context.Context, tx pgx.Tx, p application.Participant) (bool, error) {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM servicedesk.ticket_participants WHERE ticket_id = $1::uuid AND user_id = $2::uuid)`,
		p.TicketID, p.UserID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check participant: %w", err)
	}
	if !exists {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM servicedesk.ticket_participants WHERE ticket_id = $1::uuid`, p.TicketID).Scan(&n); err != nil {
			return false, fmt.Errorf("count participants: %w", err)
		}
		if n >= application.MaxParticipants {
			return false, application.ErrParticipantLimit
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO servicedesk.ticket_participants (ticket_id, user_id, level, added_by, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $5)
		ON CONFLICT (ticket_id, user_id) DO UPDATE SET level = EXCLUDED.level, added_by = EXCLUDED.added_by, added_at = now(), expires_at = EXCLUDED.expires_at`,
		p.TicketID, p.UserID, p.Level, p.AddedBy, p.ExpiresAt); err != nil {
		return false, fmt.Errorf("upsert participant: %w", err)
	}
	return !exists, nil
}

// DeleteParticipantTx implements application.ParticipantStore.
func (r *Repository) DeleteParticipantTx(ctx context.Context, tx pgx.Tx, ticketID, userID string) (bool, error) {
	tag, err := tx.Exec(ctx, `DELETE FROM servicedesk.ticket_participants WHERE ticket_id = $1::uuid AND user_id = $2::uuid`, ticketID, userID)
	if err != nil {
		return false, fmt.Errorf("delete participant: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// Participants implements application.ParticipantStore.
func (r *Repository) Participants(ctx context.Context, ticketID string) ([]application.Participant, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT ticket_id::text, user_id::text, level, added_by::text, added_at, expires_at
		FROM servicedesk.ticket_participants WHERE ticket_id = $1::uuid ORDER BY added_at, user_id`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("list participants: %w", err)
	}
	defer rows.Close()
	out := []application.Participant{}
	for rows.Next() {
		var p application.Participant
		var added time.Time
		if err := rows.Scan(&p.TicketID, &p.UserID, &p.Level, &p.AddedBy, &added, &p.ExpiresAt); err != nil {
			return nil, fmt.Errorf("list participants: scan: %w", err)
		}
		p.AddedAt = added
		out = append(out, p)
	}
	return out, rows.Err()
}
