package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
)

var _ application.RoutingStore = (*Repository)(nil)

const ruleCols = `id::text, queue_id::text, position, name, keywords, COALESCE(impact, ''), patient_impact, enabled, version, created_at, updated_at`

func scanRule(row pgx.Row) (application.RoutingRule, error) {
	var r application.RoutingRule
	err := row.Scan(&r.ID, &r.QueueID, &r.Position, &r.Name, &r.Keywords, &r.Impact, &r.PatientImpact, &r.Enabled, &r.Version, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func collectRules(rows pgx.Rows) ([]application.RoutingRule, error) {
	defer rows.Close()
	out := []application.RoutingRule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan routing rule: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (r *Repository) ListRules(ctx context.Context) ([]application.RoutingRule, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+ruleCols+` FROM servicedesk.queue_routing_rules ORDER BY position, id`)
	if err != nil {
		return nil, fmt.Errorf("list routing rules: %w", err)
	}
	return collectRules(rows)
}

func (r *Repository) LockRulesTx(ctx context.Context, tx pgx.Tx) ([]application.RoutingRule, error) {
	// One advisory lock serializes all rule changes (the rows alone would not stop two inserts at the end).
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('servicedesk.queue_routing_rules', 0))`); err != nil {
		return nil, fmt.Errorf("lock routing rules: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT `+ruleCols+` FROM servicedesk.queue_routing_rules ORDER BY position, id FOR UPDATE`)
	if err != nil {
		return nil, fmt.Errorf("lock routing rules: %w", err)
	}
	return collectRules(rows)
}

func (r *Repository) InsertRuleTx(ctx context.Context, tx pgx.Tx, in application.RoutingRule, createdBy string) (application.RoutingRule, error) {
	out, err := scanRule(tx.QueryRow(ctx, `INSERT INTO servicedesk.queue_routing_rules (queue_id, position, name, keywords, impact, patient_impact, enabled, created_by)
		VALUES ($1::uuid, $2, $3, $4, NULLIF($5, ''), $6, $7, NULLIF($8, '')::uuid) RETURNING `+ruleCols,
		in.QueueID, in.Position, in.Name, nonNil(in.Keywords), in.Impact, in.PatientImpact, in.Enabled, createdBy))
	if err != nil {
		return application.RoutingRule{}, fmt.Errorf("insert routing rule: %w", err)
	}
	return out, nil
}

func (r *Repository) UpdateRuleTx(ctx context.Context, tx pgx.Tx, in application.RoutingRule) (application.RoutingRule, error) {
	out, err := scanRule(tx.QueryRow(ctx, `UPDATE servicedesk.queue_routing_rules SET queue_id = $2::uuid, name = $3, keywords = $4, impact = NULLIF($5, ''),
		patient_impact = $6, enabled = $7, version = version + 1, updated_at = now() WHERE id = $1::uuid RETURNING `+ruleCols,
		in.ID, in.QueueID, in.Name, nonNil(in.Keywords), in.Impact, in.PatientImpact, in.Enabled))
	if err != nil {
		return application.RoutingRule{}, fmt.Errorf("update routing rule: %w", err)
	}
	return out, nil
}

func (r *Repository) DeleteRuleTx(ctx context.Context, tx pgx.Tx, id string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM servicedesk.queue_routing_rules WHERE id = $1::uuid`, id); err != nil {
		return fmt.Errorf("delete routing rule: %w", err)
	}
	return nil
}

func (r *Repository) SetPositionsTx(ctx context.Context, tx pgx.Tx, ids []string) error {
	for i, id := range ids {
		if _, err := tx.Exec(ctx, `UPDATE servicedesk.queue_routing_rules SET position = $2, updated_at = now() WHERE id = $1::uuid`, strings.ToLower(id), i); err != nil {
			return fmt.Errorf("set routing rule position: %w", err)
		}
	}
	return nil
}
