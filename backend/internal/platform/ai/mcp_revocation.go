package ai

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// RevokeMCPGrantsForSessionChange is registered with authentication at composition time.
// The caller's transaction also contains the account or credential change.
func RevokeMCPGrantsForSessionChange(ctx context.Context, tx pgx.Tx, userID, reason string, actor audit.Actor, corr string, at time.Time) error {
	grantReason := "credential_changed"
	if reason == "user_deactivated" || reason == "emergency_account_disabled" {
		grantReason = "user_deactivated"
	}
	rows, err := tx.Query(ctx, `UPDATE ai.mcp_grants SET revoked_at=$2, revoked_reason=$3
		WHERE user_id=$1 AND revoked_at IS NULL RETURNING id::text, tenant_id`, userID, at, grantReason)
	if err != nil {
		return fmt.Errorf("revoke mcp grants: %w", err)
	}
	type grant struct{ id, tenant string }
	var grants []grant
	for rows.Next() {
		var g grant
		if err := rows.Scan(&g.id, &g.tenant); err != nil {
			rows.Close()
			return err
		}
		grants = append(grants, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// Outstanding consent codes and both kinds of token are invalidated too.
	if _, err := tx.Exec(ctx, `DELETE FROM ai.mcp_codes WHERE user_id=$1`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ai.mcp_tokens WHERE grant_id IN (SELECT id FROM ai.mcp_grants WHERE user_id=$1 AND revoked_at=$2)`, userID, at); err != nil {
		return err
	}
	for _, g := range grants {
		if err := audit.Record(ctx, tx, audit.Change{Action: "ai.mcp.grant.revoked", TargetType: "ai_mcp_grant", TargetID: g.id,
			Actor: actor, CorrelationID: corr, Via: "mcp", TenantID: g.tenant, OccurredAt: at,
			Metadata: map[string]any{"reason": grantReason}}); err != nil {
			return err
		}
	}
	return nil
}
