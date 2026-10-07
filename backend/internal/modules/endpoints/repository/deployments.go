package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
)

// Target Sets and Deployment planning (F9 G2).

const targetSetColumns = `id::text, reference, name, description, owner_user_id::text, definition, all_devices, high_impact_reason, archived_at,
	archived_by::text, created_by::text, updated_by::text, version, created_at, updated_at`

func scanTargetSet(row pgx.Row) (application.TargetSet, error) {
	var t application.TargetSet
	var def []byte
	if err := row.Scan(&t.ID, &t.Reference, &t.Name, &t.Description, &t.OwnerUserID, &def, &t.AllDevices, &t.HighImpactReason, &t.ArchivedAt,
		&t.ArchivedBy, &t.CreatedBy, &t.UpdatedBy, &t.Version, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return application.TargetSet{}, err
	}
	d, err := application.ParseTargetDefinition(def)
	if err != nil {
		return application.TargetSet{}, fmt.Errorf("stored target set definition %s: %w", t.ID, err)
	}
	t.Definition = d
	return t, nil
}

func (r *Repository) InsertTargetSetTx(ctx context.Context, tx pgx.Tx, t application.TargetSet) (application.TargetSet, error) {
	def, err := t.Definition.CanonicalJSON()
	if err != nil {
		return application.TargetSet{}, err
	}
	out, err := scanTargetSet(tx.QueryRow(ctx, `INSERT INTO endpoints.target_sets (name, description, owner_user_id, definition, all_devices, high_impact_reason, created_by, updated_by)
		VALUES ($1, $2, $3::uuid, $4::jsonb, $5, $6, $7::uuid, $7::uuid) RETURNING `+targetSetColumns,
		t.Name, t.Description, t.OwnerUserID, string(def), t.AllDevices, t.HighImpactReason, t.CreatedBy))
	if pgCode(err) == "23505" {
		return application.TargetSet{}, application.ErrTargetSetNameTaken
	}
	if err != nil {
		return application.TargetSet{}, fmt.Errorf("insert target set: %w", err)
	}
	return out, nil
}

func (r *Repository) LockTargetSetTx(ctx context.Context, tx pgx.Tx, id string) (application.TargetSet, error) {
	t, err := scanTargetSet(tx.QueryRow(ctx, `SELECT `+targetSetColumns+` FROM endpoints.target_sets WHERE id = $1::uuid FOR NO KEY UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.TargetSet{}, application.ErrNotFound
	}
	if err != nil {
		return application.TargetSet{}, fmt.Errorf("lock target set: %w", err)
	}
	return t, nil
}

func (r *Repository) ShareTargetSetsTx(ctx context.Context, tx pgx.Tx, ids []string) (map[string]application.TargetSet, error) {
	out := map[string]application.TargetSet{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT `+targetSetColumns+` FROM endpoints.target_sets WHERE id = ANY($1::uuid[]) ORDER BY id FOR SHARE`, ids)
	if err != nil {
		return nil, fmt.Errorf("share target sets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		t, err := scanTargetSet(rows)
		if err != nil {
			return nil, fmt.Errorf("share target sets: scan: %w", err)
		}
		out[t.ID] = t
	}
	return out, rows.Err()
}

func (r *Repository) UpdateTargetSetTx(ctx context.Context, tx pgx.Tx, t application.TargetSet) (application.TargetSet, error) {
	def, err := t.Definition.CanonicalJSON()
	if err != nil {
		return application.TargetSet{}, err
	}
	out, err := scanTargetSet(tx.QueryRow(ctx, `UPDATE endpoints.target_sets SET name = $2, description = $3, owner_user_id = $4::uuid,
		definition = $5::jsonb, all_devices = $6, high_impact_reason = $7, archived_at = $8, archived_by = $9::uuid, updated_by = $10::uuid,
		version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+targetSetColumns,
		t.ID, t.Name, t.Description, t.OwnerUserID, string(def), t.AllDevices, t.HighImpactReason, t.ArchivedAt, t.ArchivedBy, t.UpdatedBy))
	if pgCode(err) == "23505" {
		return application.TargetSet{}, application.ErrTargetSetNameTaken
	}
	if err != nil {
		return application.TargetSet{}, fmt.Errorf("update target set: %w", err)
	}
	return out, nil
}

func (r *Repository) TargetSetInUseTx(ctx context.Context, tx pgx.Tx, id string, statuses []string) (bool, error) {
	var used bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM endpoints.deployment_rings rg JOIN endpoints.deployments d ON d.id = rg.deployment_id
		WHERE rg.target_set_id = $1::uuid AND d.status = ANY($2))`, id, statuses).Scan(&used)
	if err != nil {
		return false, fmt.Errorf("target set in use: %w", err)
	}
	return used, nil
}

func (r *Repository) DeploymentsUsingTargetSet(ctx context.Context, id string, statuses []string, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT d.id::text FROM endpoints.deployment_rings rg JOIN endpoints.deployments d ON d.id = rg.deployment_id
		WHERE rg.target_set_id = $1::uuid AND d.status = ANY($2) ORDER BY 1 DESC LIMIT $3`, id, statuses, limit)
	if err != nil {
		return nil, fmt.Errorf("deployments using target set: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (r *Repository) GetTargetSet(ctx context.Context, id string) (application.TargetSet, error) {
	t, err := scanTargetSet(r.q(ctx).QueryRow(ctx, `SELECT `+targetSetColumns+` FROM endpoints.target_sets WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.TargetSet{}, application.ErrNotFound
	}
	if err != nil {
		return application.TargetSet{}, fmt.Errorf("get target set: %w", err)
	}
	return t, nil
}

func (r *Repository) ListTargetSets(ctx context.Context, f application.TargetSetFilter) (application.TargetSetResult, error) {
	page := f.Page.Normalize()
	var conds []string
	var args []any
	if !f.IncludeArchived {
		conds = append(conds, "archived_at IS NULL")
	}
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.TargetSetResult{}, application.ErrInvalidCursor
		}
		args = append(args, page.Cursor)
		conds = append(conds, fmt.Sprintf("id > $%d::uuid", len(args)))
	}
	items, err := listPage(ctx, r, `SELECT `+targetSetColumns+` FROM endpoints.target_sets`, conds, args, page.Limit, scanTargetSet)
	if err != nil {
		return application.TargetSetResult{}, fmt.Errorf("list target sets: %w", err)
	}
	res := application.TargetSetResult{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

// ---- deployments ----

const deploymentColumns = `d.id::text, d.reference, d.name, d.software_version_id::text, v.software_product_id::text, p.name, v.product_version,
	d.intent, d.supersede, d.status, d.status_reason, d.owner_user_id::text, d.created_by::text, d.editors::text[], d.high_impact,
	d.approval_id::text, d.submitted_by::text, d.submitted_at, d.plan_sha256, d.approved_at, d.scheduled_by::text, d.scheduled_at, d.scheduled_targets,
	d.started_by::text, d.started_at, d.finished_at, d.cancelled_by::text, d.cancelled_at, d.version, d.created_at, d.updated_at`

const deploymentFrom = ` FROM endpoints.deployments d JOIN endpoints.software_versions v ON v.id = d.software_version_id
	JOIN endpoints.software_products p ON p.id = v.software_product_id`

func scanDeployment(row pgx.Row) (application.Deployment, error) {
	var d application.Deployment
	err := row.Scan(&d.ID, &d.Reference, &d.Name, &d.SoftwareVersionID, &d.ProductID, &d.ProductName, &d.ProductVersion,
		&d.Intent, &d.Supersede, &d.Status, &d.StatusReason, &d.OwnerUserID, &d.CreatedBy, &d.Editors, &d.HighImpact,
		&d.ApprovalID, &d.SubmittedBy, &d.SubmittedAt, &d.PlanSHA256, &d.ApprovedAt, &d.ScheduledBy, &d.ScheduledAt, &d.ScheduledTargets,
		&d.StartedBy, &d.StartedAt, &d.FinishedAt, &d.CancelledBy, &d.CancelledAt, &d.Version, &d.CreatedAt, &d.UpdatedAt)
	if d.Editors == nil {
		d.Editors = []string{}
	}
	return d, err
}

func (r *Repository) deploymentTx(ctx context.Context, tx pgx.Tx, id, lock string) (application.Deployment, error) {
	d, err := scanDeployment(tx.QueryRow(ctx, `SELECT `+deploymentColumns+deploymentFrom+` WHERE d.id = $1::uuid `+lock, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Deployment{}, application.ErrNotFound
	}
	if err != nil {
		return application.Deployment{}, fmt.Errorf("read deployment: %w", err)
	}
	return d, nil
}

func (r *Repository) InsertDeploymentTx(ctx context.Context, tx pgx.Tx, d application.Deployment) (application.Deployment, error) {
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO endpoints.deployments (name, software_version_id, intent, supersede, status, owner_user_id, created_by, editors, high_impact)
		VALUES ($1, $2::uuid, $3, $4, $5, $6::uuid, $7::uuid, $8::uuid[], $9) RETURNING id::text`,
		d.Name, d.SoftwareVersionID, d.Intent, d.Supersede, d.Status, d.OwnerUserID, d.CreatedBy, d.Editors, d.HighImpact).Scan(&id)
	if err != nil {
		return application.Deployment{}, fmt.Errorf("insert deployment: %w", err)
	}
	return r.deploymentTx(ctx, tx, id, "")
}

func (r *Repository) DeploymentTx(ctx context.Context, tx pgx.Tx, id string) (application.Deployment, error) {
	return r.deploymentTx(ctx, tx, id, "")
}

func (r *Repository) LockDeploymentTx(ctx context.Context, tx pgx.Tx, id string) (application.Deployment, error) {
	return r.deploymentTx(ctx, tx, id, "FOR NO KEY UPDATE OF d")
}

func (r *Repository) UpdateDeploymentTx(ctx context.Context, tx pgx.Tx, d application.Deployment) (application.Deployment, error) {
	tag, err := tx.Exec(ctx, `UPDATE endpoints.deployments SET name = $2, software_version_id = $3::uuid, intent = $4, supersede = $5, status = $6,
		status_reason = $7, owner_user_id = $8::uuid, editors = $9::uuid[], high_impact = $10, approval_id = $11::uuid, submitted_by = $12::uuid,
		submitted_at = $13, plan_sha256 = $14, approved_at = $15, scheduled_by = $16::uuid, scheduled_at = $17, cancelled_by = $18::uuid,
		cancelled_at = $19, started_by = $20::uuid, started_at = $21, finished_at = $22, scheduled_targets = $23, version = version + 1, updated_at = now() WHERE id = $1::uuid`,
		d.ID, d.Name, d.SoftwareVersionID, d.Intent, d.Supersede, d.Status, d.StatusReason, d.OwnerUserID, d.Editors, d.HighImpact,
		d.ApprovalID, d.SubmittedBy, d.SubmittedAt, d.PlanSHA256, d.ApprovedAt, d.ScheduledBy, d.ScheduledAt, d.CancelledBy, d.CancelledAt,
		d.StartedBy, d.StartedAt, d.FinishedAt, d.ScheduledTargets)
	if err != nil {
		return application.Deployment{}, fmt.Errorf("update deployment: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return application.Deployment{}, application.ErrNotFound
	}
	return r.deploymentTx(ctx, tx, d.ID, "")
}

func (r *Repository) AppendDeploymentTransitionTx(ctx context.Context, tx pgx.Tx, t application.DeploymentTransition) error {
	_, err := tx.Exec(ctx, `INSERT INTO endpoints.deployment_transitions (deployment_id, from_status, to_status, operation, reason, plan_sha256,
		actor_user_id, actor_system, correlation_id) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7::uuid, $8, $9)`,
		t.DeploymentID, t.FromStatus, t.ToStatus, t.Operation, t.Reason, t.PlanSHA256, t.ActorUserID, t.ActorSystem, t.CorrelationID)
	if err != nil {
		return fmt.Errorf("append deployment transition: %w", err)
	}
	return nil
}

const ringColumns = `rg.id::text, rg.deployment_id::text, rg.position, rg.name, rg.target_set_id::text, t.reference, t.name, rg.approval_required,
	rg.success_threshold_percent, rg.min_fresh_evidence_percent, rg.soak_minutes, rg.change_id::text, rg.no_window_required, rg.max_targets,
	rg.created_at, rg.updated_at`

const ringFrom = ` FROM endpoints.deployment_rings rg JOIN endpoints.target_sets t ON t.id = rg.target_set_id`

func scanRing(row pgx.Row) (application.DeploymentRing, error) {
	var g application.DeploymentRing
	err := row.Scan(&g.ID, &g.DeploymentID, &g.Position, &g.Name, &g.TargetSetID, &g.TargetSetReference, &g.TargetSetName, &g.ApprovalRequired,
		&g.SuccessThresholdPercent, &g.MinFreshEvidencePercent, &g.SoakMinutes, &g.ChangeID, &g.NoWindowRequired, &g.MaxTargets,
		&g.CreatedAt, &g.UpdatedAt)
	return g, err
}

func (r *Repository) rings(ctx context.Context, q querier, deploymentID string) ([]application.DeploymentRing, error) {
	rows, err := q.Query(ctx, `SELECT `+ringColumns+ringFrom+` WHERE rg.deployment_id = $1::uuid ORDER BY rg.position`, deploymentID)
	if err != nil {
		return nil, fmt.Errorf("deployment rings: %w", err)
	}
	defer rows.Close()
	out := []application.DeploymentRing{}
	for rows.Next() {
		g, err := scanRing(rows)
		if err != nil {
			return nil, fmt.Errorf("deployment rings: scan: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (r *Repository) RingsTx(ctx context.Context, tx pgx.Tx, deploymentID string) ([]application.DeploymentRing, error) {
	return r.rings(ctx, tx, deploymentID)
}

func (r *Repository) Rings(ctx context.Context, deploymentID string) ([]application.DeploymentRing, error) {
	return r.rings(ctx, r.q(ctx), deploymentID)
}

func (r *Repository) ringTx(ctx context.Context, tx pgx.Tx, id string) (application.DeploymentRing, error) {
	g, err := scanRing(tx.QueryRow(ctx, `SELECT `+ringColumns+ringFrom+` WHERE rg.id = $1::uuid`, id))
	if err != nil {
		return application.DeploymentRing{}, fmt.Errorf("read deployment ring: %w", err)
	}
	return g, nil
}

func (r *Repository) InsertRingTx(ctx context.Context, tx pgx.Tx, g application.DeploymentRing) (application.DeploymentRing, error) {
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO endpoints.deployment_rings (deployment_id, position, name, target_set_id, approval_required,
		success_threshold_percent, min_fresh_evidence_percent, soak_minutes, change_id, no_window_required, max_targets)
		VALUES ($1::uuid, $2, $3, $4::uuid, $5, $6, $7, $8, $9::uuid, $10, $11) RETURNING id::text`,
		g.DeploymentID, g.Position, g.Name, g.TargetSetID, g.ApprovalRequired, g.SuccessThresholdPercent, g.MinFreshEvidencePercent,
		g.SoakMinutes, g.ChangeID, g.NoWindowRequired, g.MaxTargets).Scan(&id)
	if err != nil {
		return application.DeploymentRing{}, fmt.Errorf("insert deployment ring: %w", err)
	}
	return r.ringTx(ctx, tx, id)
}

func (r *Repository) UpdateRingTx(ctx context.Context, tx pgx.Tx, g application.DeploymentRing) (application.DeploymentRing, error) {
	tag, err := tx.Exec(ctx, `UPDATE endpoints.deployment_rings SET position = $2, name = $3, target_set_id = $4::uuid, approval_required = $5,
		success_threshold_percent = $6, min_fresh_evidence_percent = $7, soak_minutes = $8, change_id = $9::uuid, no_window_required = $10,
		max_targets = $11, updated_at = now() WHERE id = $1::uuid`,
		g.ID, g.Position, g.Name, g.TargetSetID, g.ApprovalRequired, g.SuccessThresholdPercent, g.MinFreshEvidencePercent,
		g.SoakMinutes, g.ChangeID, g.NoWindowRequired, g.MaxTargets)
	if err != nil {
		return application.DeploymentRing{}, fmt.Errorf("update deployment ring: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return application.DeploymentRing{}, application.ErrNotFound
	}
	return r.ringTx(ctx, tx, g.ID)
}

func (r *Repository) DeleteRingTx(ctx context.Context, tx pgx.Tx, ringID string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM endpoints.deployment_rings WHERE id = $1::uuid`, ringID); err != nil {
		return fmt.Errorf("delete deployment ring: %w", err)
	}
	return nil
}

// CheckRingPositionsTx makes the deferred position uniqueness check run now, so a collision surfaces as a conflict
// of the edit instead of a commit failure.
func (r *Repository) CheckRingPositionsTx(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SET CONSTRAINTS endpoints.deployment_rings_position_unique IMMEDIATE`)
	if pgCode(err) == "23505" {
		return application.ErrVersionConflict
	}
	if err != nil {
		return fmt.Errorf("check ring positions: %w", err)
	}
	return nil
}

func (r *Repository) PackageGateTx(ctx context.Context, tx pgx.Tx, versionID string) (bool, bool, error) {
	var closed, published bool
	err := tx.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM endpoints.software_packages pk WHERE pk.software_version_id = $1::uuid AND (
			EXISTS (SELECT 1 FROM endpoints.findings f WHERE f.software_package_id = pk.id AND f.status = 'open'
				AND f.kind IN ('package_hash_mismatch', 'package_published_after_revoke'))
			OR EXISTS (SELECT 1 FROM endpoints.software_versions sv JOIN endpoints.software_products sp ON sp.id = sv.software_product_id
				WHERE sv.id = pk.software_version_id AND (sv.approval_status = 'revoked' OR sp.approval_status = 'blocked')))),
		EXISTS (SELECT 1 FROM endpoints.software_packages pk WHERE pk.software_version_id = $1::uuid AND pk.published_at IS NOT NULL)`,
		versionID).Scan(&closed, &published)
	if err != nil {
		return false, false, fmt.Errorf("package gate: %w", err)
	}
	return closed, published, nil
}

func (r *Repository) GetDeployment(ctx context.Context, id string) (application.Deployment, error) {
	d, err := scanDeployment(r.q(ctx).QueryRow(ctx, `SELECT `+deploymentColumns+deploymentFrom+` WHERE d.id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Deployment{}, application.ErrNotFound
	}
	if err != nil {
		return application.Deployment{}, fmt.Errorf("get deployment: %w", err)
	}
	return d, nil
}

func (r *Repository) DeploymentTransitions(ctx context.Context, deploymentID string) ([]application.DeploymentTransition, error) {
	rows, err := r.pool.Query(ctx, `SELECT id::text, deployment_id::text, from_status, to_status, operation, reason, plan_sha256,
		actor_user_id::text, actor_system, correlation_id, created_at FROM endpoints.deployment_transitions WHERE deployment_id = $1::uuid ORDER BY id`, deploymentID)
	if err != nil {
		return nil, fmt.Errorf("deployment transitions: %w", err)
	}
	defer rows.Close()
	out := []application.DeploymentTransition{}
	for rows.Next() {
		var t application.DeploymentTransition
		if err := rows.Scan(&t.ID, &t.DeploymentID, &t.FromStatus, &t.ToStatus, &t.Operation, &t.Reason, &t.PlanSHA256,
			&t.ActorUserID, &t.ActorSystem, &t.CorrelationID, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("deployment transitions: scan: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *Repository) ListDeployments(ctx context.Context, f application.DeploymentFilter) (application.DeploymentResult, error) {
	page := f.Page.Normalize()
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, strings.ReplaceAll(cond, "$?", fmt.Sprintf("$%d", len(args))))
	}
	if f.Status != "" {
		add("d.status = $?", f.Status)
	}
	if f.VersionID != "" {
		add("d.software_version_id = $?::uuid", f.VersionID)
	}
	if f.ProductID != "" {
		add("v.software_product_id = $?::uuid", f.ProductID)
	}
	if f.MineOf != "" {
		add("(d.owner_user_id = $?::uuid OR d.created_by = $?::uuid)", f.MineOf)
	}
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.DeploymentResult{}, application.ErrInvalidCursor
		}
		add("d.id < $?::uuid", page.Cursor)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, `SELECT `+deploymentColumns+deploymentFrom+where+fmt.Sprintf(` ORDER BY d.id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return application.DeploymentResult{}, fmt.Errorf("list deployments: %w", err)
	}
	defer rows.Close()
	items := []application.Deployment{}
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return application.DeploymentResult{}, fmt.Errorf("list deployments: scan: %w", err)
		}
		items = append(items, d)
	}
	if err := rows.Err(); err != nil {
		return application.DeploymentResult{}, err
	}
	res := application.DeploymentResult{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

func (r *Repository) ActiveDeploymentsOfProduct(ctx context.Context, productID, excludeID string, statuses []string, limit int) ([]application.Deployment, error) {
	rows, err := r.q(ctx).Query(ctx, `SELECT `+deploymentColumns+deploymentFrom+` WHERE v.software_product_id = $1::uuid AND d.id <> $2::uuid
		AND d.status = ANY($3) ORDER BY d.id DESC LIMIT $4`, productID, excludeID, statuses, limit)
	if err != nil {
		return nil, fmt.Errorf("active deployments of product: %w", err)
	}
	defer rows.Close()
	var out []application.Deployment
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, fmt.Errorf("active deployments of product: scan: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
