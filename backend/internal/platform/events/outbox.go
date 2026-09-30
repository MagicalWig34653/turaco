package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type OutboxEvent struct {
	ID            string
	EventType     string
	EventVersion  int
	OccurredAt    time.Time
	ActorID       *string
	CorrelationID string
	Payload       json.RawMessage
}

func InsertOutbox(ctx context.Context, tx pgx.Tx, event OutboxEvent) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO platform.outbox_events
		(id, event_type, event_version, occurred_at, actor_id, correlation_id, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		event.ID, event.EventType, event.EventVersion, event.OccurredAt,
		event.ActorID, event.CorrelationID, event.Payload,
	)
	if err != nil {
		return fmt.Errorf("insert outbox event: %w", err)
	}
	return nil
}
