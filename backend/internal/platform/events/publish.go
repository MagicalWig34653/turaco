package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Publication is a domain event to record in the outbox. The version comes
// from the event registry; Payload is marshalled to JSON and must not contain
// secrets or free text that consumers do not need.
type Publication struct {
	Type          string
	ActorID       *string // nil for system actors
	CorrelationID string
	Payload       any
}

// Publish writes the event to the outbox inside tx, so the event commits with
// the state change that caused it (api-events-conventions).
func Publish(ctx context.Context, tx pgx.Tx, p Publication) error {
	if p.CorrelationID == "" {
		return errors.New("publish event: correlation id is required")
	}
	version := 0
	for _, def := range Registry {
		if def.Name == p.Type {
			version = def.Version
			break
		}
	}
	if version == 0 {
		return fmt.Errorf("publish event: unregistered event type %q", p.Type)
	}
	payload, err := json.Marshal(p.Payload)
	if err != nil {
		return fmt.Errorf("publish event %s: marshal payload: %w", p.Type, err)
	}
	var id string
	if err := tx.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id); err != nil {
		return fmt.Errorf("publish event %s: generate id: %w", p.Type, err)
	}
	return InsertOutbox(ctx, tx, OutboxEvent{
		ID: id, EventType: p.Type, EventVersion: version, OccurredAt: time.Now().UTC().Truncate(time.Microsecond),
		ActorID: p.ActorID, CorrelationID: p.CorrelationID, Payload: payload,
	})
}
