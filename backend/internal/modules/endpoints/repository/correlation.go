package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
)

// Failure correlation, follow-up and report reads (F9 G4).

func (r *Repository) CorrelationDeployments(ctx context.Context, recentSince time.Time, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text FROM endpoints.deployments d
		WHERE d.status IN ('running', 'paused')
		   OR (d.status IN ('completed_with_errors', 'failed') AND d.finished_at >= $1)
		   OR EXISTS (SELECT 1 FROM endpoints.findings f WHERE f.deployment_id = d.id AND f.status = 'open')
		ORDER BY d.id DESC LIMIT $2`, recentSince, limit)
	if err != nil {
		return nil, fmt.Errorf("correlation deployments: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("correlation deployments: scan: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// errorCodeSQL is the failure code of a target: the provider's raw status of the pinned artifact on the Device, or
// Turaco's reason code when the provider reported none. Expects the aliases t (target), o (observation).
const errorCodeSQL = `COALESCE(NULLIF(left(btrim(o.raw_status), 200), ''), t.state_reason)`

const targetJoins = `FROM endpoints.deployment_targets t
	JOIN endpoints.deployment_ring_runs rr ON rr.id = t.ring_run_id
	JOIN endpoints.deployment_rings rg ON rg.id = rr.ring_id
	JOIN endpoints.devices dv ON dv.id = t.device_id
	LEFT JOIN endpoints.management_observations o ON o.artifact_id = rr.management_artifact_id AND o.device_id = t.device_id`

func (r *Repository) FailureGroupsTx(ctx context.Context, tx pgx.Tx, deploymentID string) (int, int, []application.FailureGroup, error) {
	rows, err := tx.Query(ctx, `
		WITH t AS (
			SELECT t.state, rr.ring_id::text AS ring_id, rg.name AS ring_name, dv.manufacturer, dv.model, dv.os_version, `+errorCodeSQL+` AS code
			`+targetJoins+`
			WHERE t.deployment_id = $1::uuid AND t.state NOT IN ('cancelled', 'not_applicable', 'already_satisfied')
		), f AS (SELECT * FROM t WHERE state IN ('failed', 'expired'))
		SELECT 'total', '', '', (SELECT count(*) FROM f), (SELECT count(*) FROM t)
		UNION ALL SELECT 'error_code', code, code, count(*), 0 FROM f WHERE code IS NOT NULL GROUP BY code HAVING count(*) >= $2
		UNION ALL SELECT 'model', model, model, count(*) FILTER (WHERE state IN ('failed', 'expired')), count(*) FROM t
			WHERE model IS NOT NULL AND model <> '' GROUP BY model HAVING count(*) FILTER (WHERE state IN ('failed', 'expired')) >= $2
		UNION ALL SELECT 'manufacturer', manufacturer, manufacturer, count(*) FILTER (WHERE state IN ('failed', 'expired')), count(*) FROM t
			WHERE manufacturer IS NOT NULL AND manufacturer <> '' GROUP BY manufacturer HAVING count(*) FILTER (WHERE state IN ('failed', 'expired')) >= $2
		UNION ALL SELECT 'os_version', os_version, os_version, count(*) FILTER (WHERE state IN ('failed', 'expired')), count(*) FROM t
			WHERE os_version IS NOT NULL AND os_version <> '' GROUP BY os_version HAVING count(*) FILTER (WHERE state IN ('failed', 'expired')) >= $2
		UNION ALL SELECT 'ring', ring_id, ring_name, count(*) FILTER (WHERE state IN ('failed', 'expired')), count(*) FROM t
			GROUP BY ring_id, ring_name HAVING count(*) FILTER (WHERE state IN ('failed', 'expired')) >= $2`,
		deploymentID, application.MinClusterTargets)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("failure groups: %w", err)
	}
	defer rows.Close()
	var failures, targets int
	var groups []application.FailureGroup
	for rows.Next() {
		var g application.FailureGroup
		var failed, total int64
		if err := rows.Scan(&g.Dimension, &g.Key, &g.Value, &failed, &total); err != nil {
			return 0, 0, nil, fmt.Errorf("failure groups: scan: %w", err)
		}
		g.Failed, g.Total = int(failed), int(total)
		if g.Dimension == "total" {
			failures, targets = g.Failed, g.Total
			continue
		}
		groups = append(groups, g)
	}
	return failures, targets, groups, rows.Err()
}

func (r *Repository) OpenClusterFindingTx(ctx context.Context, tx pgx.Tx, deploymentID, key string, detail []byte) (string, bool, error) {
	var id string
	var inserted bool
	err := tx.QueryRow(ctx, `
		INSERT INTO endpoints.findings (kind, deployment_id, cluster_key, detail) VALUES ('deployment_failure_cluster', $1::uuid, $2, $3::jsonb)
		ON CONFLICT (kind, deployment_id, cluster_key) WHERE status = 'open' AND deployment_id IS NOT NULL DO UPDATE SET detail = EXCLUDED.detail
			WHERE endpoints.findings.detail IS DISTINCT FROM EXCLUDED.detail
		RETURNING id::text, (xmax = 0)`, deploymentID, key, detail).Scan(&id, &inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("open cluster finding: %w", err)
	}
	return id, inserted, nil
}

func (r *Repository) OpenClusterKeysTx(ctx context.Context, tx pgx.Tx, deploymentID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT cluster_key FROM endpoints.findings WHERE kind = 'deployment_failure_cluster' AND deployment_id = $1::uuid AND status = 'open' ORDER BY id`, deploymentID)
	if err != nil {
		return nil, fmt.Errorf("open cluster keys: %w", err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("open cluster keys: scan: %w", err)
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (r *Repository) ResolveClusterFindingTx(ctx context.Context, tx pgx.Tx, deploymentID, key string) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE endpoints.findings SET status = 'resolved', resolved_at = now()
		WHERE kind = 'deployment_failure_cluster' AND deployment_id = $1::uuid AND cluster_key = $2 AND status = 'open'`, deploymentID, key)
	if err != nil {
		return false, fmt.Errorf("resolve cluster finding: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *Repository) EngineHaltedRingsTx(ctx context.Context, tx pgx.Tx, deploymentID string) ([]application.HaltedRing, error) {
	rows, err := tx.Query(ctx, `
		SELECT rr.ring_id::text, rr.status_reason FROM endpoints.deployment_ring_runs rr
		WHERE rr.deployment_id = $1::uuid AND rr.status = 'halted' AND rr.status_reason IS NOT NULL
		  AND COALESCE((SELECT rt.actor_system IS NOT NULL FROM endpoints.deployment_ring_transitions rt
				WHERE rt.ring_run_id = rr.id AND rt.to_status = 'halted' ORDER BY rt.id DESC LIMIT 1), false)
		ORDER BY rr.position`, deploymentID)
	if err != nil {
		return nil, fmt.Errorf("engine halted rings: %w", err)
	}
	defer rows.Close()
	var out []application.HaltedRing
	for rows.Next() {
		var h application.HaltedRing
		if err := rows.Scan(&h.RingID, &h.Reason); err != nil {
			return nil, fmt.Errorf("engine halted rings: scan: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

const zeroUUID = `'00000000-0000-0000-0000-000000000000'::uuid`

func (r *Repository) FollowupExistsTx(ctx context.Context, tx pgx.Tx, deploymentID, reason string, ringID *string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM endpoints.deployment_followups WHERE deployment_id = $1::uuid AND reason = $2
		AND COALESCE(ring_id, `+zeroUUID+`) = COALESCE($3::uuid, `+zeroUUID+`))`, deploymentID, reason, ringID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("followup exists: %w", err)
	}
	return ok, nil
}

func (r *Repository) InsertFollowupTx(ctx context.Context, tx pgx.Tx, deploymentID, reason string, ringID *string, taskID string) error {
	_, err := tx.Exec(ctx, `INSERT INTO endpoints.deployment_followups (deployment_id, reason, ring_id, task_id) VALUES ($1::uuid, $2, $3::uuid, $4::uuid)`,
		deploymentID, reason, ringID, taskID)
	if err != nil {
		return fmt.Errorf("insert followup: %w", err)
	}
	return nil
}

func (r *Repository) LockFollowupsTx(ctx context.Context, tx pgx.Tx, deploymentID string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('endpoints.followup:' || $1, 0))`, deploymentID); err != nil {
		return fmt.Errorf("lock followups: %w", err)
	}
	return nil
}

// ---- reports ----

func (r *Repository) ReportData(ctx context.Context, deploymentID string) (application.ReportData, error) {
	out := application.ReportData{Counts: map[string]map[string]int{}, Medians: map[string]int64{}}
	rows, err := r.pool.Query(ctx, `SELECT ring_run_id::text, state, count(*) FROM endpoints.deployment_targets WHERE deployment_id = $1::uuid GROUP BY 1, 2`, deploymentID)
	if err != nil {
		return out, fmt.Errorf("report counts: %w", err)
	}
	for rows.Next() {
		var run, state string
		var n int64
		if err := rows.Scan(&run, &state, &n); err != nil {
			rows.Close()
			return out, fmt.Errorf("report counts: scan: %w", err)
		}
		if out.Counts[run] == nil {
			out.Counts[run] = map[string]int{}
		}
		out.Counts[run][state] = int(n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("report counts: %w", err)
	}

	rows, err = r.pool.Query(ctx, `SELECT ring_run_id::text, percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM decided_at - assignment_requested_at))
		FROM endpoints.deployment_targets WHERE deployment_id = $1::uuid AND state = 'successful' AND assignment_requested_at IS NOT NULL AND decided_at >= assignment_requested_at
		GROUP BY ROLLUP (ring_run_id)`, deploymentID)
	if err != nil {
		return out, fmt.Errorf("report medians: %w", err)
	}
	for rows.Next() {
		var run *string
		var secs *float64
		if err := rows.Scan(&run, &secs); err != nil {
			rows.Close()
			return out, fmt.Errorf("report medians: scan: %w", err)
		}
		if secs == nil {
			continue
		}
		v := int64(*secs + 0.5)
		if run == nil {
			out.OverallMedian = &v
		} else {
			out.Medians[*run] = v
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("report medians: %w", err)
	}

	rows, err = r.pool.Query(ctx, `SELECT COALESCE(code, 'unknown'), count(*) FROM (
			SELECT `+errorCodeSQL+` AS code `+targetJoins+` WHERE t.deployment_id = $1::uuid AND t.state IN ('failed', 'expired')) x
		GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT $2`, deploymentID, application.MaxReportFailureReasons)
	if err != nil {
		return out, fmt.Errorf("report reasons: %w", err)
	}
	for rows.Next() {
		var fr application.FailureReason
		var n int64
		if err := rows.Scan(&fr.Code, &n); err != nil {
			rows.Close()
			return out, fmt.Errorf("report reasons: scan: %w", err)
		}
		fr.Count = int(n)
		out.Reasons = append(out.Reasons, fr)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("report reasons: %w", err)
	}

	rows, err = r.pool.Query(ctx, `SELECT id::text, detail, raised_at FROM endpoints.findings
		WHERE kind = 'deployment_failure_cluster' AND deployment_id = $1::uuid AND status = 'open' ORDER BY id LIMIT 50`, deploymentID)
	if err != nil {
		return out, fmt.Errorf("report clusters: %w", err)
	}
	for rows.Next() {
		var c application.ClusterInfo
		var detail []byte
		if err := rows.Scan(&c.FindingID, &detail, &c.RaisedAt); err != nil {
			rows.Close()
			return out, fmt.Errorf("report clusters: scan: %w", err)
		}
		var d struct {
			Dimension string `json:"dimension"`
			Value     string `json:"value"`
			Failed    int    `json:"failed"`
			Total     int    `json:"total"`
		}
		if json.Unmarshal(detail, &d) == nil {
			c.Dimension, c.Value, c.Failed, c.Total = d.Dimension, d.Value, d.Failed, d.Total
		}
		out.Clusters = append(out.Clusters, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("report clusters: %w", err)
	}

	rows, err = r.pool.Query(ctx, `SELECT ring_run_id::text, from_status, to_status, operation, reason, actor_user_id::text, actor_system, created_at
		FROM endpoints.deployment_ring_transitions WHERE deployment_id = $1::uuid ORDER BY id`, deploymentID)
	if err != nil {
		return out, fmt.Errorf("report ring transitions: %w", err)
	}
	for rows.Next() {
		var t application.ReportRingTransition
		if err := rows.Scan(&t.RingRunID, &t.From, &t.To, &t.Operation, &t.Reason, &t.ActorUserID, &t.ActorSystem, &t.At); err != nil {
			rows.Close()
			return out, fmt.Errorf("report ring transitions: scan: %w", err)
		}
		out.RingChanges = append(out.RingChanges, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("report ring transitions: %w", err)
	}

	rows, err = r.pool.Query(ctx, `SELECT reason, ring_id::text, task_id::text, created_at FROM endpoints.deployment_followups WHERE deployment_id = $1::uuid ORDER BY id`, deploymentID)
	if err != nil {
		return out, fmt.Errorf("report followups: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var f application.FollowUpInfo
		if err := rows.Scan(&f.Reason, &f.RingID, &f.TaskID, &f.CreatedAt); err != nil {
			return out, fmt.Errorf("report followups: scan: %w", err)
		}
		out.FollowUps = append(out.FollowUps, f)
	}
	return out, rows.Err()
}

func (r *Repository) ReportTargets(ctx context.Context, deploymentID, after string, limit int) ([]application.ReportRow, error) {
	if after != "" && !validUUID(after) {
		return nil, application.ErrInvalidCursor
	}
	rows, err := r.pool.Query(ctx, `SELECT t.id::text, rg.position, rg.name, t.device_id::text, dv.name, t.state, t.state_reason,
			CASE WHEN t.state IN ('failed', 'expired') THEN COALESCE(`+errorCodeSQL+`, '') ELSE '' END,
			t.resolved_at, t.assignment_requested_at, t.decided_at `+targetJoins+`
		WHERE t.deployment_id = $1::uuid AND ($2 = '' OR t.id > $2::uuid) ORDER BY t.id LIMIT $3`, deploymentID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("report targets: %w", err)
	}
	defer rows.Close()
	var out []application.ReportRow
	for rows.Next() {
		var x application.ReportRow
		if err := rows.Scan(&x.ID, &x.RingPosition, &x.RingName, &x.DeviceID, &x.DeviceName, &x.State, &x.StateReason, &x.ErrorCode,
			&x.ResolvedAt, &x.AssignmentRequestedAt, &x.DecidedAt); err != nil {
			return nil, fmt.Errorf("report targets: scan: %w", err)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (r *Repository) Rollouts(ctx context.Context, statuses []string, after string, limit int) ([]application.RolloutRow, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+deploymentColumns+deploymentFrom+` WHERE d.status = ANY($1::text[]) AND ($2 = '' OR d.id < $2::uuid)
		ORDER BY d.id DESC LIMIT $3`, statuses, after, limit)
	if err != nil {
		return nil, fmt.Errorf("rollouts: %w", err)
	}
	var out []application.RolloutRow
	var ids []string
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("rollouts: scan: %w", err)
		}
		out = append(out, application.RolloutRow{Deployment: d, Counts: map[string]int{}})
		ids = append(ids, d.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return out, err
	}
	idx := map[string]int{}
	for i, id := range ids {
		idx[id] = i
	}
	crows, err := r.pool.Query(ctx, `SELECT deployment_id::text, state, count(*) FROM endpoints.deployment_targets WHERE deployment_id = ANY($1::uuid[]) GROUP BY 1, 2`, ids)
	if err != nil {
		return nil, fmt.Errorf("rollout counts: %w", err)
	}
	for crows.Next() {
		var id, state string
		var n int64
		if err := crows.Scan(&id, &state, &n); err != nil {
			crows.Close()
			return nil, fmt.Errorf("rollout counts: scan: %w", err)
		}
		out[idx[id]].Counts[state] = int(n)
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return nil, fmt.Errorf("rollout counts: %w", err)
	}
	rrows, err := r.pool.Query(ctx, `SELECT rr.deployment_id::text, rr.position, rg.name, rr.status FROM endpoints.deployment_ring_runs rr
		JOIN endpoints.deployment_rings rg ON rg.id = rr.ring_id WHERE rr.deployment_id = ANY($1::uuid[]) ORDER BY rr.deployment_id, rr.position`, ids)
	if err != nil {
		return nil, fmt.Errorf("rollout rings: %w", err)
	}
	defer rrows.Close()
	for rrows.Next() {
		var id, name, status string
		var pos int
		if err := rrows.Scan(&id, &pos, &name, &status); err != nil {
			return nil, fmt.Errorf("rollout rings: scan: %w", err)
		}
		row := &out[idx[id]]
		row.RingCount++
		if status == application.RingHalted {
			row.HaltedRings++
		}
		if status == application.RingAwaitingPromotion {
			row.AwaitingPromo = true
		}
		if row.CurrentPosition == nil && status != application.RingPromoted {
			p, n, st := pos, name, status
			row.CurrentPosition, row.CurrentRingName, row.CurrentRingStat = &p, &n, &st
		}
	}
	return out, rrows.Err()
}

func (r *Repository) DeploymentDeviceIDs(ctx context.Context, deploymentID string, limit int) ([]string, bool, error) {
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT device_id::text FROM endpoints.deployment_targets WHERE deployment_id = $1::uuid AND state <> 'cancelled'
		ORDER BY 1 LIMIT $2`, deploymentID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("deployment devices: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, false, fmt.Errorf("deployment devices: scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(ids) > limit {
		return ids[:limit], true, nil
	}
	return ids, false, nil
}
