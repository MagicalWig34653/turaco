package repository

import (
	"context"
	"fmt"
	"time"
)

// Briefing reads of the Deployments (F9 G4): counts and references of rollouts that need a look, and the health of the
// execution engine. No target or device data.

// RolloutAttention is a running or paused Deployment that needs attention or a decision.
type RolloutAttention struct {
	ID          string
	Reference   string
	Name        string
	ProductName string
	// Kind is ring_halted, paused or awaiting_promotion.
	Kind  string
	Since time.Time
}

// RolloutOverview is the briefing's view of the rollouts: the ones that need a look (at most limit, more reports
// true) and the number of rollouts that simply progress.
type RolloutOverview struct {
	Attention  []RolloutAttention
	More       bool
	InProgress int
	// AttentionCounts counts all rollouts that need a look per kind (not bounded by the list limit).
	AttentionCounts map[string]int
	// UnassignedFollowups counts the follow-up Tasks created without an assignee for Deployments that are still
	// running, paused or recently finished with errors.
	UnassignedFollowups int
}

func (r *Repository) RolloutOverview(ctx context.Context, limit int) (RolloutOverview, error) {
	var out RolloutOverview
	rows, err := r.pool.Query(ctx, `
		SELECT id, reference, name, product_name, kind, since FROM (
			SELECT d.id::text AS id, d.reference, d.name, p.name AS product_name, d.updated_at AS since,
				CASE WHEN EXISTS (SELECT 1 FROM endpoints.deployment_ring_runs rr WHERE rr.deployment_id = d.id AND rr.status = 'halted') THEN 'ring_halted'
					WHEN d.status = 'paused' THEN 'paused'
					WHEN EXISTS (SELECT 1 FROM endpoints.deployment_ring_runs rr WHERE rr.deployment_id = d.id AND rr.status = 'awaiting_promotion') THEN 'awaiting_promotion'
					ELSE 'in_progress' END AS kind
			FROM endpoints.deployments d JOIN endpoints.software_versions v ON v.id = d.software_version_id
				JOIN endpoints.software_products p ON p.id = v.software_product_id
			WHERE d.status IN ('running', 'paused')) x
		WHERE kind <> 'in_progress'
		ORDER BY CASE kind WHEN 'ring_halted' THEN 0 WHEN 'paused' THEN 1 ELSE 2 END, id DESC LIMIT $1`, limit+1)
	if err != nil {
		return out, fmt.Errorf("rollout overview: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var a RolloutAttention
		if err := rows.Scan(&a.ID, &a.Reference, &a.Name, &a.ProductName, &a.Kind, &a.Since); err != nil {
			return out, fmt.Errorf("rollout overview: scan: %w", err)
		}
		out.Attention = append(out.Attention, a)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(out.Attention) > limit {
		out.Attention, out.More = out.Attention[:limit], true
	}
	out.AttentionCounts = map[string]int{}
	crows, err := r.pool.Query(ctx, `SELECT kind, count(*) FROM (
			SELECT CASE WHEN EXISTS (SELECT 1 FROM endpoints.deployment_ring_runs rr WHERE rr.deployment_id = d.id AND rr.status = 'halted') THEN 'ring_halted'
					WHEN d.status = 'paused' THEN 'paused'
					WHEN EXISTS (SELECT 1 FROM endpoints.deployment_ring_runs rr WHERE rr.deployment_id = d.id AND rr.status = 'awaiting_promotion') THEN 'awaiting_promotion'
					ELSE 'in_progress' END AS kind
			FROM endpoints.deployments d WHERE d.status IN ('running', 'paused')) x
		WHERE kind <> 'in_progress' GROUP BY kind`)
	if err != nil {
		return out, fmt.Errorf("rollout overview: counts: %w", err)
	}
	for crows.Next() {
		var kind string
		var n int
		if err := crows.Scan(&kind, &n); err != nil {
			crows.Close()
			return out, fmt.Errorf("rollout overview: counts: scan: %w", err)
		}
		out.AttentionCounts[kind] = n
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return out, err
	}
	err = r.pool.QueryRow(ctx, `SELECT count(*) FROM endpoints.deployment_followups f JOIN endpoints.deployments d ON d.id = f.deployment_id
		WHERE f.unassigned AND (d.status IN ('running', 'paused') OR d.finished_at > now() - interval '14 days')`).Scan(&out.UnassignedFollowups)
	if err != nil {
		return out, fmt.Errorf("rollout overview: unassigned followups: %w", err)
	}
	err = r.pool.QueryRow(ctx, `SELECT count(*) FROM endpoints.deployments d WHERE d.status = 'running'
		AND NOT EXISTS (SELECT 1 FROM endpoints.deployment_ring_runs rr WHERE rr.deployment_id = d.id AND rr.status IN ('halted', 'awaiting_promotion'))`).Scan(&out.InProgress)
	if err != nil {
		return out, fmt.Errorf("rollout overview: in progress: %w", err)
	}
	return out, nil
}

// EngineHealth is the state of the Deployment execution engine.
type EngineHealth struct {
	// LastTickAt is the latest tick of a Deployment with work; Active the Deployments that need ticks.
	LastTickAt     *time.Time
	Active         int
	ClearPending   int
	ResolvingStuck int
}

func (r *Repository) EngineHealth(ctx context.Context, stuckAfter time.Duration) (EngineHealth, error) {
	var h EngineHealth
	err := r.pool.QueryRow(ctx, `SELECT
		(SELECT max(last_ticked_at) FROM endpoints.deployments WHERE status IN ('running', 'resolving_targets')),
		(SELECT count(*) FROM endpoints.deployments WHERE status IN ('running', 'resolving_targets')),
		(SELECT count(*) FROM endpoints.deployment_ring_runs WHERE clear_requested_at IS NOT NULL AND assignment_cleared_at IS NULL),
		(SELECT count(*) FROM endpoints.deployments WHERE status = 'resolving_targets' AND started_at < now() - make_interval(secs => $1))`,
		stuckAfter.Seconds()).Scan(&h.LastTickAt, &h.Active, &h.ClearPending, &h.ResolvingStuck)
	if err != nil {
		return h, fmt.Errorf("engine health: %w", err)
	}
	return h, nil
}
