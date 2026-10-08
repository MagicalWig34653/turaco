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
	// Via is the channel of an AI-assisted action ("ai" or "mcp"); empty for ordinary actions.
	Via string
	// TenantID is the data plane of the acting principal; set whenever Via is set.
	TenantID string
	// ProposalID is the AI Proposal an action executes (empty otherwise).
	ProposalID string
}

func Insert(ctx context.Context, tx pgx.Tx, entry Entry) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO platform.audit_events
		(id, occurred_at, actor_id, action, target_type, target_id, correlation_id, before_data, after_data, metadata, via, tenant_id, ai_proposal_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,''),NULLIF($12,''),NULLIF($13,'')::uuid)`,
		entry.ID, entry.OccurredAt, entry.ActorID, entry.Action, entry.TargetType,
		entry.TargetID, entry.CorrelationID, entry.Before, entry.After, entry.Metadata,
		entry.Via, entry.TenantID, entry.ProposalID,
	)
	if err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}
