package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type Entry struct {
	ID            string
	OccurredAt    time.Time
	ActorID       *string
	Action        string
	TargetType    string
	TargetID      string
	CorrelationID string
	Before        json.RawMessage
	After         json.RawMessage
	Metadata      json.RawMessage
}

func Insert(ctx context.Context, tx pgx.Tx, entry Entry) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO platform.audit_events
		(id, occurred_at, actor_id, action, target_type, target_id, correlation_id, before_data, after_data, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		entry.ID, entry.OccurredAt, entry.ActorID, entry.Action, entry.TargetType,
		entry.TargetID, entry.CorrelationID, entry.Before, entry.After, entry.Metadata,
	)
	if err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}
