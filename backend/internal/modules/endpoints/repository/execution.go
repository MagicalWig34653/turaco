package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
)

// Deployment execution (F9 G3): ring runs, targets, attempts. All statements are explicit SQL; the set-based
// statements change the targets of one ring and append their history rows in the same statement.

const ringRunColumns = `r.id::text, r.deployment_id::text, r.ring_id::text, r.position, r.status, r.status_reason, r.group_external_id,
	r.activated_at, r.settled_at, r.awaiting_since, r.promoted_at, r.promoted_by::text, r.halted_at, r.assignment_requested_at,
	r.assignment_cleared_at, r.clear_requested_at, r.management_artifact_id::text, r.management_artifact_external_id, r.attempt_base, r.retry_count,
	r.promotion_approval_id::text, r.promotion_approval_status, r.promotion_approval_requested_by::text,
	r.version, r.created_at, r.updated_at`

func scanRingRun(row pgx.Row) (application.DeploymentRingRun, error) {
	var g application.DeploymentRingRun
	err := row.Scan(&g.ID, &g.DeploymentID, &g.RingID, &g.Position, &g.Status, &g.StatusReason, &g.GroupExternalID, &g.ActivatedAt,
		&g.SettledAt, &g.AwaitingSince, &g.PromotedAt, &g.PromotedBy, &g.HaltedAt, &g.AssignmentRequestedAt, &g.AssignmentClearedAt,
		&g.ClearRequestedAt, &g.ManagementArtifactID, &g.ManagementArtifactExternalID, &g.AttemptBase, &g.RetryCount, &g.PromotionApprovalID, &g.PromotionApprovalStatus, &g.PromotionApprovalRequestedBy, &g.Version, &g.CreatedAt, &g.UpdatedAt)
	return g, err
}

func (r *Repository) ringRuns(ctx context.Context, q querier, deploymentID string) ([]application.DeploymentRingRun, error) {
	rows, err := q.Query(ctx, `SELECT `+ringRunColumns+` FROM endpoints.deployment_ring_runs r WHERE r.deployment_id = $1::uuid ORDER BY r.position`, deploymentID)
	if err != nil {
		return nil, fmt.Errorf("ring runs: %w", err)
	}
	defer rows.Close()
	out := []application.DeploymentRingRun{}
	for rows.Next() {
		g, err := scanRingRun(rows)
		if err != nil {
			return nil, fmt.Errorf("ring runs: scan: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (r *Repository) RingRunsTx(ctx context.Context, tx pgx.Tx, deploymentID string) ([]application.DeploymentRingRun, error) {
	return r.ringRuns(ctx, tx, deploymentID)
}

func (r *Repository) RingRuns(ctx context.Context, deploymentID string) ([]application.DeploymentRingRun, error) {
	return r.ringRuns(ctx, r.q(ctx), deploymentID)
}

func (r *Repository) RingRunByIDTx(ctx context.Context, tx pgx.Tx, id string) (application.DeploymentRingRun, error) {
	g, err := scanRingRun(tx.QueryRow(ctx, `SELECT `+ringRunColumns+` FROM endpoints.deployment_ring_runs r WHERE r.id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.DeploymentRingRun{}, application.ErrNotFound
	}
	if err != nil {
		return application.DeploymentRingRun{}, fmt.Errorf("read ring run: %w", err)
	}
	return g, nil
}

func (r *Repository) InsertRingRunsTx(ctx context.Context, tx pgx.Tx, runs []application.DeploymentRingRun) ([]application.DeploymentRingRun, error) {
	for _, g := range runs {
		_, err := tx.Exec(ctx, `INSERT INTO endpoints.deployment_ring_runs (deployment_id, ring_id, position, status, group_external_id)
			VALUES ($1::uuid, $2::uuid, $3, 'pending', $4)`, g.DeploymentID, g.RingID, g.Position, g.GroupExternalID)
		if err != nil {
			return nil, fmt.Errorf("insert ring run: %w", err)
		}
	}
	if len(runs) == 0 {
		return nil, nil
	}
	return r.ringRuns(ctx, tx, runs[0].DeploymentID)
}

func (r *Repository) UpdateRingRunTx(ctx context.Context, tx pgx.Tx, g application.DeploymentRingRun) (application.DeploymentRingRun, error) {
	tag, err := tx.Exec(ctx, `UPDATE endpoints.deployment_ring_runs SET status = $2, status_reason = $3, activated_at = $4, settled_at = $5,
		awaiting_since = $6, promoted_at = $7, promoted_by = $8::uuid, halted_at = $9, assignment_requested_at = $10, assignment_cleared_at = $11,
		promotion_approval_id = $12::uuid, promotion_approval_status = $13, promotion_approval_requested_by = $14::uuid,
		clear_requested_at = $15, management_artifact_id = $16::uuid, management_artifact_external_id = $17, attempt_base = $18, retry_count = $19,
		version = version + 1, updated_at = now() WHERE id = $1::uuid`,
		g.ID, g.Status, g.StatusReason, g.ActivatedAt, g.SettledAt, g.AwaitingSince, g.PromotedAt, g.PromotedBy, g.HaltedAt,
		g.AssignmentRequestedAt, g.AssignmentClearedAt, g.PromotionApprovalID, g.PromotionApprovalStatus, g.PromotionApprovalRequestedBy,
		g.ClearRequestedAt, g.ManagementArtifactID, g.ManagementArtifactExternalID, g.AttemptBase, g.RetryCount)
	if err != nil {
		return application.DeploymentRingRun{}, fmt.Errorf("update ring run: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return application.DeploymentRingRun{}, application.ErrNotFound
	}
	out, err := scanRingRun(tx.QueryRow(ctx, `SELECT `+ringRunColumns+` FROM endpoints.deployment_ring_runs r WHERE r.id = $1::uuid`, g.ID))
	if err != nil {
		return application.DeploymentRingRun{}, fmt.Errorf("read ring run: %w", err)
	}
	return out, nil
}

func (r *Repository) AppendRingTransitionTx(ctx context.Context, tx pgx.Tx, t application.RingTransition) error {
	_, err := tx.Exec(ctx, `INSERT INTO endpoints.deployment_ring_transitions (deployment_id, ring_run_id, from_status, to_status, operation, reason,
		actor_user_id, actor_system, correlation_id) VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7::uuid, $8, $9)`,
		t.DeploymentID, t.RingRunID, t.From, t.To, t.Operation, t.Reason, t.ActorUserID, t.ActorSystem, t.CorrelationID)
	if err != nil {
		return fmt.Errorf("append ring transition: %w", err)
	}
	return nil
}

func (r *Repository) DeviceFactsTx(ctx context.Context, tx pgx.Tx, deviceIDs []string, productID, productVersion string, freshSince time.Time) (map[string]application.DeviceFact, error) {
	rows, err := tx.Query(ctx, `SELECT d.id::text, d.deleted_observed_at IS NULL, d.os_platform, d.external_id, d.provider,
		EXISTS (SELECT 1 FROM endpoints.software_installations si WHERE si.device_id = d.id AND si.software_product_id = $2::uuid
			AND si.raw_version = $3 AND si.deleted_observed_at IS NULL),
		COALESCE(d.last_synced_at >= $4, false)
		FROM endpoints.devices d WHERE d.id = ANY($1::uuid[])`, deviceIDs, productID, productVersion, freshSince)
	if err != nil {
		return nil, fmt.Errorf("device facts: %w", err)
	}
	defer rows.Close()
	out := map[string]application.DeviceFact{}
	for rows.Next() {
		var id string
		var f application.DeviceFact
		if err := rows.Scan(&id, &f.Live, &f.Platform, &f.External, &f.Provider, &f.HasVersion, &f.Fresh); err != nil {
			return nil, fmt.Errorf("device facts: scan: %w", err)
		}
		out[id] = f
	}
	return out, rows.Err()
}

func (r *Repository) RecheckSatisfiedTx(ctx context.Context, tx pgx.Tx, ringRunID, productID, productVersion string, uninstall bool, freshSince, now time.Time, correlationID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `WITH cand AS (
			SELECT t.id FROM endpoints.deployment_targets t JOIN endpoints.devices d ON d.id = t.device_id
			WHERE t.ring_run_id = $1::uuid AND t.state = 'pending' AND d.deleted_observed_at IS NULL AND d.last_synced_at >= $4
				AND EXISTS (SELECT 1 FROM endpoints.software_installations si WHERE si.device_id = d.id AND si.software_product_id = $2::uuid
					AND si.raw_version = $3 AND si.deleted_observed_at IS NULL) <> $6::bool
			FOR UPDATE OF t),
		upd AS (
			UPDATE endpoints.deployment_targets t SET state = 'already_satisfied',
				state_reason = CASE WHEN $6::bool THEN 'version_not_installed' ELSE 'version_present' END,
				decided_at = $5::timestamptz, version = t.version + 1, updated_at = now()
			FROM cand WHERE t.id = cand.id RETURNING t.id, t.deployment_id, t.state_reason),
		ins AS (INSERT INTO endpoints.deployment_target_transitions (deployment_id, target_id, from_state, to_state, reason, correlation_id)
			SELECT deployment_id, id, 'pending', 'already_satisfied', state_reason, $7 FROM upd RETURNING 1)
		SELECT count(*) FROM ins`, ringRunID, productID, productVersion, freshSince, now, uninstall, correlationID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("recheck satisfied: %w", err)
	}
	return n, nil
}

func (r *Repository) InsertTargetsTx(ctx context.Context, tx pgx.Tx, deploymentID string, targets []application.TargetInsert, now time.Time, correlationID string) error {
	if len(targets) == 0 {
		return nil
	}
	runs := make([]string, len(targets))
	devs := make([]string, len(targets))
	states := make([]string, len(targets))
	reasons := make([]string, len(targets))
	for i, t := range targets {
		runs[i], devs[i], states[i], reasons[i] = t.RingRunID, t.DeviceID, t.State, t.Reason
	}
	_, err := tx.Exec(ctx, `WITH ins AS (
			INSERT INTO endpoints.deployment_targets (deployment_id, ring_run_id, device_id, state, state_reason, resolved_at, decided_at)
			SELECT $1::uuid, u.run, u.dev, u.st, NULLIF(u.rs, ''), $5::timestamptz, CASE WHEN u.st = 'pending' THEN NULL ELSE $5::timestamptz END
			FROM unnest($2::uuid[], $3::uuid[], $4::text[], $6::text[]) AS u(run, dev, st, rs)
			RETURNING id, deployment_id, state, state_reason)
		INSERT INTO endpoints.deployment_target_transitions (deployment_id, target_id, from_state, to_state, reason, correlation_id)
		SELECT deployment_id, id, NULL, state, state_reason, $7 FROM ins`,
		deploymentID, runs, devs, states, now, reasons, correlationID)
	if err != nil {
		return fmt.Errorf("insert deployment targets: %w", err)
	}
	return nil
}

func (r *Repository) PublishedPackageTx(ctx context.Context, tx pgx.Tx, versionID string) (application.PublishedPackage, bool, error) {
	var p application.PublishedPackage
	err := tx.QueryRow(ctx, `SELECT pk.id::text, COALESCE(pk.management_artifact_external_id, ''), pk.management_artifact_id::text,
			COALESCE(ma.platform, 'other'), COALESCE(pk.installer_sha256 = sv.installer_sha256, false)
		FROM endpoints.software_packages pk JOIN endpoints.software_versions sv ON sv.id = pk.software_version_id
			LEFT JOIN endpoints.management_artifacts ma ON ma.id = pk.management_artifact_id
		WHERE pk.software_version_id = $1::uuid AND pk.status = 'published' AND pk.published_at IS NOT NULL
		ORDER BY pk.published_at DESC LIMIT 1 FOR SHARE OF pk`, versionID).
		Scan(&p.PackageID, &p.ArtifactExternalID, &p.ArtifactID, &p.ArtifactPlatform, &p.HashMatches)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.PublishedPackage{}, false, nil
	}
	if err != nil {
		return application.PublishedPackage{}, false, fmt.Errorf("published package: %w", err)
	}
	return p, true, nil
}

func (r *Repository) RingDeviceExternalIDsTx(ctx context.Context, tx pgx.Tx, ringRunID, provider string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT d.external_id FROM endpoints.deployment_targets t JOIN endpoints.devices d ON d.id = t.device_id
		WHERE t.ring_run_id = $1::uuid AND d.provider = $2 AND t.state NOT IN ('already_satisfied', 'not_applicable', 'cancelled') ORDER BY d.external_id`, ringRunID, provider)
	if err != nil {
		return nil, fmt.Errorf("ring device ids: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (r *Repository) RingFirstDeviceTx(ctx context.Context, tx pgx.Tx, ringRunID string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT device_id::text FROM endpoints.deployment_targets WHERE ring_run_id = $1::uuid ORDER BY id LIMIT 1`, ringRunID).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("ring first device: %w", err)
	}
	return id, nil
}

func (r *Repository) RequestAssignmentsTx(ctx context.Context, tx pgx.Tx, ringRunID string, now time.Time, correlationID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `WITH upd AS (
			UPDATE endpoints.deployment_targets SET state = 'assignment_requested', assignment_requested_at = $2, version = version + 1, updated_at = now()
			WHERE ring_run_id = $1::uuid AND state = 'pending' RETURNING id, deployment_id),
		ins AS (INSERT INTO endpoints.deployment_target_transitions (deployment_id, target_id, from_state, to_state, reason, correlation_id)
			SELECT deployment_id, id, 'pending', 'assignment_requested', NULL, $3 FROM upd RETURNING 1)
		SELECT count(*) FROM ins`, ringRunID, now, correlationID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("request assignments: %w", err)
	}
	return n, nil
}

func (r *Repository) DetectReadBackTx(ctx context.Context, tx pgx.Tx, ringRunID, artifactID, groupExternalID, intent string, now time.Time, expiry time.Duration, correlationID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `WITH cand AS (
			SELECT t.id FROM endpoints.deployment_targets t JOIN endpoints.devices d ON d.id = t.device_id
			WHERE t.ring_run_id = $1::uuid AND t.state = 'assignment_requested'
				AND EXISTS (SELECT 1 FROM endpoints.management_assignments a WHERE a.artifact_id = $2::uuid AND a.valid_until IS NULL
					AND a.target_kind = 'group' AND a.target_group_external_id = $3 AND a.mode = 'include' AND a.intent = $4)
				AND EXISTS (SELECT 1 FROM endpoints.device_group_memberships m WHERE m.device_id = t.device_id AND m.provider = d.provider
					AND m.group_external_id = $3 AND m.observed_until IS NULL)
			FOR UPDATE OF t),
		upd AS (
			UPDATE endpoints.deployment_targets t SET state = 'awaiting_observation', read_back_at = $5::timestamptz,
				expires_at = $5::timestamptz + make_interval(secs => $6::float8), version = t.version + 1, updated_at = now()
			FROM cand WHERE t.id = cand.id RETURNING t.id, t.deployment_id),
		ins AS (INSERT INTO endpoints.deployment_target_transitions (deployment_id, target_id, from_state, to_state, reason, correlation_id)
			SELECT deployment_id, id, 'assignment_requested', 'awaiting_observation', NULL, $7 FROM upd RETURNING 1)
		SELECT count(*) FROM ins`, ringRunID, artifactID, groupExternalID, intent, now, expiry.Seconds(), correlationID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("detect read-back: %w", err)
	}
	return n, nil
}

func (r *Repository) DecideTargetsTx(ctx context.Context, tx pgx.Tx, ringRunID, artifactID, productID, productVersion string, now time.Time, correlationID string) ([]application.TargetDecision, error) {
	rows, err := tx.Query(ctx, `WITH cand AS (
			SELECT t.id, o.normalized_state AS os, o.observed_at AS oat
			FROM endpoints.deployment_targets t
			JOIN endpoints.management_observations o ON o.artifact_id = $2::uuid AND o.device_id = t.device_id AND o.retired_at IS NULL
			WHERE t.ring_run_id = $1::uuid AND t.state = 'awaiting_observation' AND o.observed_at > t.read_back_at
				AND o.normalized_state IN ('applied', 'failed', 'not_applicable')
				AND (o.observed_at_provider OR EXISTS (SELECT 1 FROM endpoints.management_observation_history h
					WHERE h.artifact_id = o.artifact_id AND h.device_id = o.device_id AND h.normalized_state = o.normalized_state
						AND h.observed_at > t.read_back_at))
			FOR UPDATE OF t),
		upd AS (
			UPDATE endpoints.deployment_targets t SET
				state = CASE c.os WHEN 'applied' THEN 'successful' WHEN 'failed' THEN 'failed' ELSE 'not_applicable' END,
				state_reason = CASE c.os WHEN 'applied' THEN 'observed_applied' WHEN 'failed' THEN 'observed_failed' ELSE 'provider_not_applicable' END,
				decided_at = $5::timestamptz, evidence_observed_at = c.oat, version = t.version + 1, updated_at = now()
			FROM cand c WHERE t.id = c.id
			RETURNING t.id, t.deployment_id, t.device_id, t.state, t.state_reason, t.evidence_observed_at),
		ins AS (INSERT INTO endpoints.deployment_target_transitions (deployment_id, target_id, from_state, to_state, reason, evidence_observed_at, correlation_id)
			SELECT deployment_id, id, 'awaiting_observation', state, state_reason, evidence_observed_at, $6 FROM upd RETURNING 1)
		SELECT u.id::text, u.device_id::text, u.state, u.evidence_observed_at,
			EXISTS (SELECT 1 FROM endpoints.software_installations si WHERE si.device_id = u.device_id AND si.software_product_id = $3::uuid
				AND si.raw_version = $4 AND si.deleted_observed_at IS NULL),
			d.last_synced_at >= u.evidence_observed_at
		FROM upd u JOIN endpoints.devices d ON d.id = u.device_id ORDER BY u.id`, ringRunID, artifactID, productID, productVersion, now, correlationID)
	if err != nil {
		return nil, fmt.Errorf("decide targets: %w", err)
	}
	defer rows.Close()
	var out []application.TargetDecision
	for rows.Next() {
		var d application.TargetDecision
		if err := rows.Scan(&d.TargetID, &d.DeviceID, &d.State, &d.ObservedAt, &d.HasVersion, &d.DeviceFresh); err != nil {
			return nil, fmt.Errorf("decide targets: scan: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *Repository) ExpireTargetsTx(ctx context.Context, tx pgx.Tx, ringRunID string, now time.Time, expiry time.Duration, correlationID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `WITH old AS (
			SELECT id, deployment_id, state FROM endpoints.deployment_targets
			WHERE ring_run_id = $1::uuid AND ((state = 'awaiting_observation' AND expires_at <= $2)
				OR (state = 'assignment_requested' AND assignment_requested_at + make_interval(secs => $4::float8) <= $2)) FOR UPDATE),
		upd AS (
			UPDATE endpoints.deployment_targets t SET state = 'expired',
				state_reason = CASE old.state WHEN 'assignment_requested' THEN 'read_back_missing' ELSE 'observation_expired' END,
				decided_at = $2, version = t.version + 1, updated_at = now()
			FROM old WHERE t.id = old.id RETURNING t.id, t.state_reason),
		ins AS (INSERT INTO endpoints.deployment_target_transitions (deployment_id, target_id, from_state, to_state, reason, correlation_id)
			SELECT old.deployment_id, old.id, old.state, 'expired', upd.state_reason, $3 FROM old JOIN upd ON upd.id = old.id RETURNING 1)
		SELECT count(*) FROM ins`, ringRunID, now, correlationID, expiry.Seconds()).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("expire targets: %w", err)
	}
	return n, nil
}

func (r *Repository) CancelTargetsTx(ctx context.Context, tx pgx.Tx, deploymentID string, now time.Time, correlationID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `WITH old AS (
			SELECT id, deployment_id, state FROM endpoints.deployment_targets
			WHERE deployment_id = $1::uuid AND state IN ('pending', 'assignment_requested', 'awaiting_observation') FOR UPDATE),
		upd AS (
			UPDATE endpoints.deployment_targets t SET state = 'cancelled', state_reason = 'deployment_cancelled', decided_at = $2, version = t.version + 1, updated_at = now()
			FROM old WHERE t.id = old.id RETURNING t.id),
		ins AS (INSERT INTO endpoints.deployment_target_transitions (deployment_id, target_id, from_state, to_state, reason, correlation_id)
			SELECT old.deployment_id, old.id, old.state, 'cancelled', 'deployment_cancelled', $3 FROM old JOIN upd ON upd.id = old.id RETURNING 1)
		SELECT count(*) FROM ins`, deploymentID, now, correlationID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("cancel targets: %w", err)
	}
	return n, nil
}

func (r *Repository) ringCounts(ctx context.Context, q querier, ringRunID, artifactID string, freshSince time.Time) (application.RingCounts, error) {
	rows, err := q.Query(ctx, `SELECT state, count(*)::int, (count(*) FILTER (WHERE state_reason = 'provider_not_applicable'))::int
		FROM endpoints.deployment_targets WHERE ring_run_id = $1::uuid GROUP BY state`, ringRunID)
	if err != nil {
		return application.RingCounts{}, fmt.Errorf("ring counts: %w", err)
	}
	out := application.RingCounts{ByState: map[string]int{}}
	for rows.Next() {
		var st string
		var n, na int
		if err := rows.Scan(&st, &n, &na); err != nil {
			rows.Close()
			return application.RingCounts{}, fmt.Errorf("ring counts: scan: %w", err)
		}
		out.ByState[st] = n
		out.ProviderNA += na
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return application.RingCounts{}, fmt.Errorf("ring counts: %w", err)
	}
	var art *string
	if artifactID != "" {
		art = &artifactID
	}
	err = q.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE t.state = 'successful' AND o.normalized_state = 'applied' AND o.observed_at > t.read_back_at AND o.last_synced_at >= $3)::int,
			count(*) FILTER (WHERE t.state NOT IN ('already_satisfied', 'not_applicable', 'cancelled') AND o.observed_at > t.read_back_at AND o.last_synced_at >= $3)::int
		FROM endpoints.deployment_targets t
		LEFT JOIN endpoints.management_observations o ON o.artifact_id = $2::uuid AND o.device_id = t.device_id AND o.retired_at IS NULL
		WHERE t.ring_run_id = $1::uuid`, ringRunID, art, freshSince).Scan(&out.FreshSuccessful, &out.FreshObserved)
	if err != nil {
		return application.RingCounts{}, fmt.Errorf("ring evidence: %w", err)
	}
	return out, nil
}

func (r *Repository) RingCountsTx(ctx context.Context, tx pgx.Tx, ringRunID, artifactID string, freshSince time.Time) (application.RingCounts, error) {
	return r.ringCounts(ctx, tx, ringRunID, artifactID, freshSince)
}

func (r *Repository) RingCounts(ctx context.Context, ringRunID, artifactID string, freshSince time.Time) (application.RingCounts, error) {
	return r.ringCounts(ctx, r.q(ctx), ringRunID, artifactID, freshSince)
}

func (r *Repository) ListTargets(ctx context.Context, ringRunID string, f application.TargetFilter) (application.TargetResult, error) {
	page := f.Page.Normalize()
	args := []any{ringRunID}
	conds := "t.ring_run_id = $1::uuid"
	if f.State != "" {
		args = append(args, f.State)
		conds += fmt.Sprintf(" AND t.state = $%d", len(args))
	}
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.TargetResult{}, application.ErrInvalidCursor
		}
		args = append(args, page.Cursor)
		conds += fmt.Sprintf(" AND t.id > $%d::uuid", len(args))
	}
	args = append(args, page.Limit+1)
	rows, err := r.q(ctx).Query(ctx, `SELECT t.id::text, t.deployment_id::text, t.ring_run_id::text, t.device_id::text, d.name, t.state, t.state_reason,
			t.resolved_at, t.assignment_requested_at, t.read_back_at, t.expires_at, t.decided_at, t.evidence_observed_at, t.version, t.updated_at
		FROM endpoints.deployment_targets t JOIN endpoints.devices d ON d.id = t.device_id WHERE `+conds+fmt.Sprintf(` ORDER BY t.id LIMIT $%d`, len(args)), args...)
	if err != nil {
		return application.TargetResult{}, fmt.Errorf("list targets: %w", err)
	}
	defer rows.Close()
	res := application.TargetResult{Items: []application.DeploymentTarget{}}
	for rows.Next() {
		var t application.DeploymentTarget
		if err := rows.Scan(&t.ID, &t.DeploymentID, &t.RingRunID, &t.DeviceID, &t.DeviceName, &t.State, &t.StateReason, &t.ResolvedAt,
			&t.AssignmentRequestedAt, &t.ReadBackAt, &t.ExpiresAt, &t.DecidedAt, &t.EvidenceObservedAt, &t.Version, &t.UpdatedAt); err != nil {
			return application.TargetResult{}, fmt.Errorf("list targets: scan: %w", err)
		}
		res.Items = append(res.Items, t)
	}
	if err := rows.Err(); err != nil {
		return application.TargetResult{}, fmt.Errorf("list targets: %w", err)
	}
	if len(res.Items) > page.Limit {
		res.Items = res.Items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

func (r *Repository) AttemptStateTx(ctx context.Context, tx pgx.Tx, ringRunID, kind string) (int, bool, error) {
	var n int
	var accepted bool
	err := tx.QueryRow(ctx, `SELECT count(*)::int, COALESCE(bool_or(outcome_code = 'accepted'), false) FROM endpoints.deployment_attempts
		WHERE ring_run_id = $1::uuid AND kind = $2`, ringRunID, kind).Scan(&n, &accepted)
	if err != nil {
		return 0, false, fmt.Errorf("attempt state: %w", err)
	}
	return n, accepted, nil
}

func (r *Repository) BeginAttemptTx(ctx context.Context, tx pgx.Tx, a application.DeploymentAttempt, correlationID string) error {
	_, err := tx.Exec(ctx, `INSERT INTO endpoints.deployment_attempts (deployment_id, ring_run_id, kind, attempt, operation_id, requested_at, outcome_code, correlation_id)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, 'in_flight', $7)`,
		a.DeploymentID, a.RingRunID, a.Kind, a.Attempt, a.OperationID, a.RequestedAt, correlationID)
	if err != nil {
		return fmt.Errorf("begin attempt: %w", err)
	}
	return nil
}

func (r *Repository) FinishAttemptTx(ctx context.Context, tx pgx.Tx, ringRunID, kind string, attempt int, outcome string, now time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE endpoints.deployment_attempts SET outcome_code = $4, finished_at = $5
		WHERE ring_run_id = $1::uuid AND kind = $2 AND attempt = $3 AND outcome_code = 'in_flight'`, ringRunID, kind, attempt, outcome, now)
	if err != nil {
		return fmt.Errorf("finish attempt: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return application.ErrNotFound
	}
	return nil
}

func (r *Repository) InterruptAttemptsTx(ctx context.Context, tx pgx.Tx, deploymentID string, now time.Time) ([]application.DeploymentAttempt, error) {
	rows, err := tx.Query(ctx, `UPDATE endpoints.deployment_attempts SET outcome_code = 'interrupted', finished_at = $2
		WHERE deployment_id = $1::uuid AND outcome_code = 'in_flight' RETURNING id::text, ring_run_id::text, kind, attempt`, deploymentID, now)
	if err != nil {
		return nil, fmt.Errorf("interrupt attempts: %w", err)
	}
	defer rows.Close()
	var out []application.DeploymentAttempt
	for rows.Next() {
		a := application.DeploymentAttempt{DeploymentID: deploymentID, OutcomeCode: application.OutcomeInterrupted}
		if err := rows.Scan(&a.ID, &a.RingRunID, &a.Kind, &a.Attempt); err != nil {
			return nil, fmt.Errorf("interrupt attempts: scan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repository) ListAttempts(ctx context.Context, deploymentID string, page application.Page) (application.AttemptResult, error) {
	page = page.Normalize()
	args := []any{deploymentID}
	conds := "deployment_id = $1::uuid"
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.AttemptResult{}, application.ErrInvalidCursor
		}
		args = append(args, page.Cursor)
		conds += " AND id > $2::uuid"
	}
	args = append(args, page.Limit+1)
	rows, err := r.q(ctx).Query(ctx, `SELECT id::text, deployment_id::text, ring_run_id::text, kind, attempt, operation_id, requested_at, outcome_code, finished_at, created_at
		FROM endpoints.deployment_attempts WHERE `+conds+fmt.Sprintf(` ORDER BY id LIMIT $%d`, len(args)), args...)
	if err != nil {
		return application.AttemptResult{}, fmt.Errorf("list attempts: %w", err)
	}
	defer rows.Close()
	res := application.AttemptResult{Items: []application.DeploymentAttempt{}}
	for rows.Next() {
		var a application.DeploymentAttempt
		if err := rows.Scan(&a.ID, &a.DeploymentID, &a.RingRunID, &a.Kind, &a.Attempt, &a.OperationID, &a.RequestedAt, &a.OutcomeCode, &a.FinishedAt, &a.CreatedAt); err != nil {
			return application.AttemptResult{}, fmt.Errorf("list attempts: scan: %w", err)
		}
		res.Items = append(res.Items, a)
	}
	if err := rows.Err(); err != nil {
		return application.AttemptResult{}, fmt.Errorf("list attempts: %w", err)
	}
	if len(res.Items) > page.Limit {
		res.Items = res.Items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

// needsClearSQL selects the ring runs whose assignment is queued for clearing: queued, not cleared, a set attempt exists
// (the provider may hold the assignment) and the clear attempts are not exhausted.
const needsClearSQL = `r.clear_requested_at IS NOT NULL AND r.assignment_cleared_at IS NULL
	AND EXISTS (SELECT 1 FROM endpoints.deployment_attempts a WHERE a.ring_run_id = r.id AND a.kind = 'set_assignment')
	AND (SELECT count(*) FROM endpoints.deployment_attempts a WHERE a.ring_run_id = r.id AND a.kind = 'clear_assignment') < ` + "10"

func (r *Repository) EngineWork(ctx context.Context, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT d.id::text FROM endpoints.deployments d
		WHERE d.status IN ('resolving_targets', 'running')
			OR EXISTS (SELECT 1 FROM endpoints.deployment_ring_runs r WHERE r.deployment_id = d.id AND `+needsClearSQL+`)
		ORDER BY d.last_ticked_at NULLS FIRST, d.id LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("engine work: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (r *Repository) TouchTicked(ctx context.Context, id string) error {
	if _, err := r.pool.Exec(ctx, `UPDATE endpoints.deployments SET last_ticked_at = now() WHERE id = $1::uuid`, id); err != nil {
		return fmt.Errorf("touch ticked: %w", err)
	}
	return nil
}

func (r *Repository) GateSweep(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT d.id::text FROM endpoints.deployments d
		JOIN endpoints.software_versions v ON v.id = d.software_version_id
		JOIN endpoints.software_products p ON p.id = v.software_product_id
		LEFT JOIN LATERAL (SELECT pk.management_artifact_id, COALESCE(pk.management_artifact_external_id, '') AS ext, pk.installer_sha256 = v.installer_sha256 AS hash_ok
			FROM endpoints.software_packages pk WHERE pk.software_version_id = d.software_version_id AND pk.status = 'published' AND pk.published_at IS NOT NULL
			ORDER BY pk.published_at DESC LIMIT 1) pk ON true
		WHERE d.status IN ('running', 'paused')
			AND (d.status = 'running' OR EXISTS (SELECT 1 FROM endpoints.deployment_ring_runs r WHERE r.deployment_id = d.id
				AND (r.status IN ('active', 'awaiting_promotion')
					OR (r.clear_requested_at IS NULL AND r.assignment_cleared_at IS NULL
						AND EXISTS (SELECT 1 FROM endpoints.deployment_attempts a WHERE a.ring_run_id = r.id AND a.kind = 'set_assignment')))))
			AND (v.approval_status <> 'approved' OR p.approval_status <> 'approved'
				OR pk.management_artifact_id IS NULL OR pk.ext = '' OR NOT COALESCE(pk.hash_ok, false)
				OR EXISTS (SELECT 1 FROM endpoints.software_packages k JOIN endpoints.findings f ON f.software_package_id = k.id AND f.status = 'open'
					AND f.kind IN ('package_hash_mismatch', 'package_published_after_revoke') WHERE k.software_version_id = d.software_version_id)
				OR EXISTS (SELECT 1 FROM endpoints.deployment_ring_runs r WHERE r.deployment_id = d.id AND r.management_artifact_id IS NOT NULL
					AND (r.management_artifact_id <> pk.management_artifact_id OR r.management_artifact_external_id <> pk.ext)))
		ORDER BY d.id`)
	if err != nil {
		return nil, fmt.Errorf("gate sweep: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
