package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
)

// ---- software products ----

const productColumns = `id::text, name, publisher, approval_status, approval_reason, version, created_at, updated_at`

func scanProduct(row pgx.Row) (application.SoftwareProduct, error) {
	var p application.SoftwareProduct
	err := row.Scan(&p.ID, &p.Name, &p.Publisher, &p.ApprovalStatus, &p.ApprovalReason, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func (r *Repository) LockProductTx(ctx context.Context, tx pgx.Tx, id string) (application.SoftwareProduct, error) {
	p, err := scanProduct(tx.QueryRow(ctx, `SELECT `+productColumns+` FROM endpoints.software_products WHERE id = $1::uuid FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.SoftwareProduct{}, application.ErrNotFound
	}
	if err != nil {
		return application.SoftwareProduct{}, fmt.Errorf("lock software product: %w", err)
	}
	return p, nil
}

func (r *Repository) UpdateProductApprovalTx(ctx context.Context, tx pgx.Tx, id, status string, reason *string) (application.SoftwareProduct, error) {
	p, err := scanProduct(tx.QueryRow(ctx, `
		UPDATE endpoints.software_products SET approval_status = $2, approval_reason = $3, version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+productColumns, id, status, reason))
	if err != nil {
		return application.SoftwareProduct{}, fmt.Errorf("update software product approval: %w", err)
	}
	return p, nil
}

func (r *Repository) AppendProductTransitionTx(ctx context.Context, tx pgx.Tx, t application.SoftwareTransition) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO endpoints.software_product_transitions (software_product_id, from_status, to_status, operation, reason, actor_user_id, actor_system, correlation_id)
		VALUES ($1::uuid, $2, $3, $4, $5, $6::uuid, $7, $8)`,
		t.SubjectID, t.FromStatus, t.ToStatus, t.Operation, t.Reason, t.ActorUserID, t.ActorSystem, t.CorrelationID)
	if err != nil {
		return fmt.Errorf("append software product transition: %w", err)
	}
	return nil
}

func (r *Repository) ListSoftwareProducts(ctx context.Context, f application.SoftwareProductFilter) (application.SoftwareProductResult, error) {
	page := f.Page.Normalize()
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.Status != "" {
		add("approval_status = $%d", f.Status)
	}
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.SoftwareProductResult{}, application.ErrInvalidCursor
		}
		add("id > $%d::uuid", page.Cursor)
	}
	items, err := listPage(ctx, r, `SELECT `+productColumns+` FROM endpoints.software_products`, conds, args, page.Limit, scanProduct)
	if err != nil {
		return application.SoftwareProductResult{}, fmt.Errorf("list software products: %w", err)
	}
	res := application.SoftwareProductResult{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

// listPage runs base with the conditions, ordered by id and limited to limit+1 rows.
func listPage[T any](ctx context.Context, r *Repository, base string, conds []string, args []any, limit int, scan func(pgx.Row) (T, error)) ([]T, error) {
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`%s%s ORDER BY id LIMIT $%d`, base, where, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]T, 0, limit+1)
	for rows.Next() {
		it, err := scan(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// ---- software versions ----

const versionColumns = `v.id::text, v.software_product_id::text, p.name, v.product_version, v.installer_sha256, v.installer_url, v.publisher,
	v.install_command, v.install_command_sha256, v.detection_rule, v.detection_rule_sha256, v.binding_sha256, v.registered_by::text,
	v.approval_status, v.approval_reason, v.approval_requested_by::text, v.approval_requested_at, v.approval_decided_by::text,
	v.approval_decided_at, v.version, v.created_at, v.updated_at`

const versionFrom = ` FROM endpoints.software_versions v JOIN endpoints.software_products p ON p.id = v.software_product_id`

func scanVersion(row pgx.Row) (application.SoftwareVersion, error) {
	var v application.SoftwareVersion
	err := row.Scan(&v.ID, &v.ProductID, &v.ProductName, &v.ProductVersion, &v.InstallerSHA256, &v.InstallerURL, &v.Publisher,
		&v.InstallCommand, &v.InstallCommandSHA256, &v.DetectionRule, &v.DetectionRuleSHA256, &v.BindingSHA256, &v.RegisteredBy,
		&v.ApprovalStatus, &v.ApprovalReason, &v.RequestedBy, &v.RequestedAt, &v.DecidedBy,
		&v.DecidedAt, &v.Version, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

func (r *Repository) InsertVersionTx(ctx context.Context, tx pgx.Tx, v application.SoftwareVersion) (application.SoftwareVersion, bool, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO endpoints.software_versions (software_product_id, product_version, installer_sha256, installer_url, publisher,
			install_command, install_command_sha256, detection_rule, detection_rule_sha256, binding_sha256, registered_by)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::uuid)
		ON CONFLICT (software_product_id, binding_sha256) DO NOTHING
		RETURNING id::text`,
		v.ProductID, v.ProductVersion, v.InstallerSHA256, v.InstallerURL, v.Publisher, v.InstallCommand, v.InstallCommandSHA256,
		v.DetectionRule, v.DetectionRuleSHA256, v.BindingSHA256, v.RegisteredBy).Scan(&id)
	created := true
	if errors.Is(err, pgx.ErrNoRows) {
		created = false
		err = tx.QueryRow(ctx, `SELECT id::text FROM endpoints.software_versions WHERE software_product_id = $1::uuid AND binding_sha256 = $2`,
			v.ProductID, v.BindingSHA256).Scan(&id)
	}
	if err != nil {
		return application.SoftwareVersion{}, false, fmt.Errorf("insert software version: %w", err)
	}
	out, err := scanVersion(tx.QueryRow(ctx, `SELECT `+versionColumns+versionFrom+` WHERE v.id = $1::uuid`, id))
	if err != nil {
		return application.SoftwareVersion{}, false, fmt.Errorf("read software version: %w", err)
	}
	return out, created, nil
}

func (r *Repository) LockVersionTx(ctx context.Context, tx pgx.Tx, id string) (application.SoftwareVersion, error) {
	v, err := scanVersion(tx.QueryRow(ctx, `SELECT `+versionColumns+versionFrom+` WHERE v.id = $1::uuid FOR UPDATE OF v`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.SoftwareVersion{}, application.ErrNotFound
	}
	if err != nil {
		return application.SoftwareVersion{}, fmt.Errorf("lock software version: %w", err)
	}
	return v, nil
}

func (r *Repository) UpdateVersionApprovalTx(ctx context.Context, tx pgx.Tx, v application.SoftwareVersion) (application.SoftwareVersion, error) {
	_, err := tx.Exec(ctx, `
		UPDATE endpoints.software_versions SET approval_status = $2, approval_reason = $3, approval_requested_by = $4::uuid,
			approval_requested_at = $5, approval_decided_by = $6::uuid, approval_decided_at = $7, version = version + 1, updated_at = now()
		WHERE id = $1::uuid`,
		v.ID, v.ApprovalStatus, v.ApprovalReason, v.RequestedBy, v.RequestedAt, v.DecidedBy, v.DecidedAt)
	if err != nil {
		return application.SoftwareVersion{}, fmt.Errorf("update software version approval: %w", err)
	}
	out, err := scanVersion(tx.QueryRow(ctx, `SELECT `+versionColumns+versionFrom+` WHERE v.id = $1::uuid`, v.ID))
	if err != nil {
		return application.SoftwareVersion{}, fmt.Errorf("read software version: %w", err)
	}
	return out, nil
}

func (r *Repository) AppendVersionApprovalTx(ctx context.Context, tx pgx.Tx, t application.SoftwareTransition) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO endpoints.software_version_approvals (software_version_id, from_status, to_status, operation, reason,
			installer_sha256, binding_sha256, actor_user_id, actor_system, correlation_id)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8::uuid, $9, $10)`,
		t.SubjectID, t.FromStatus, t.ToStatus, t.Operation, t.Reason, t.InstallerSHA256, t.BindingSHA256, t.ActorUserID, t.ActorSystem, t.CorrelationID)
	if err != nil {
		return fmt.Errorf("append software version approval: %w", err)
	}
	return nil
}

func (r *Repository) GetSoftwareVersion(ctx context.Context, id string) (application.SoftwareVersion, error) {
	v, err := scanVersion(r.pool.QueryRow(ctx, `SELECT `+versionColumns+versionFrom+` WHERE v.id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.SoftwareVersion{}, application.ErrNotFound
	}
	if err != nil {
		return application.SoftwareVersion{}, fmt.Errorf("get software version: %w", err)
	}
	return v, nil
}

func (r *Repository) VersionApprovals(ctx context.Context, versionID string) ([]application.SoftwareTransition, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, software_version_id::text, from_status, to_status, operation, reason, installer_sha256, binding_sha256,
			actor_user_id::text, actor_system, correlation_id, created_at
		FROM endpoints.software_version_approvals WHERE software_version_id = $1::uuid ORDER BY id LIMIT 500`, versionID)
	if err != nil {
		return nil, fmt.Errorf("list software version approvals: %w", err)
	}
	defer rows.Close()
	out := []application.SoftwareTransition{}
	for rows.Next() {
		var t application.SoftwareTransition
		if err := rows.Scan(&t.ID, &t.SubjectID, &t.FromStatus, &t.ToStatus, &t.Operation, &t.Reason, &t.InstallerSHA256, &t.BindingSHA256,
			&t.ActorUserID, &t.ActorSystem, &t.CorrelationID, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("list software version approvals: scan: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *Repository) ListSoftwareVersions(ctx context.Context, f application.SoftwareVersionFilter) (application.SoftwareVersionResult, error) {
	page := f.Page.Normalize()
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.Status != "" {
		add("v.approval_status = $%d", f.Status)
	}
	if f.ProductID != "" {
		add("v.software_product_id = $%d::uuid", f.ProductID)
	}
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.SoftwareVersionResult{}, application.ErrInvalidCursor
		}
		add("v.id > $%d::uuid", page.Cursor)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s%s%s ORDER BY v.id LIMIT $%d`, versionColumns, versionFrom, where, len(args)), args...)
	if err != nil {
		return application.SoftwareVersionResult{}, fmt.Errorf("list software versions: %w", err)
	}
	defer rows.Close()
	items := make([]application.SoftwareVersion, 0, page.Limit+1)
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return application.SoftwareVersionResult{}, fmt.Errorf("list software versions: scan: %w", err)
		}
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		return application.SoftwareVersionResult{}, fmt.Errorf("list software versions: %w", err)
	}
	res := application.SoftwareVersionResult{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

// ---- software packages ----

const packageColumns = `id::text, provider, provider_package_id, software_version_id::text, status, installer_sha256, management_provider,
	management_artifact_external_id, management_artifact_id::text, source, observed_at, last_synced_at, requested_by::text,
	publish_requested_by::text, publish_requested_at, version, created_at, updated_at,
	EXISTS (SELECT 1 FROM endpoints.findings f WHERE f.software_package_id = endpoints.software_packages.id AND f.status = 'open' AND f.kind = 'package_hash_mismatch')`

func scanPackage(row pgx.Row) (application.SoftwarePackage, error) {
	var p application.SoftwarePackage
	err := row.Scan(&p.ID, &p.Provider, &p.ProviderPackageID, &p.VersionID, &p.Status, &p.InstallerSHA256, &p.ManagementProvider,
		&p.ManagementArtifactExternalID, &p.ManagementArtifactID, &p.Source, &p.ObservedAt, &p.LastSyncedAt, &p.RequestedBy,
		&p.PublishRequestedBy, &p.PublishRequestedAt, &p.Version, &p.CreatedAt, &p.UpdatedAt, &p.HashMismatch)
	return p, err
}

func (r *Repository) InsertPackageTx(ctx context.Context, tx pgx.Tx, provider, versionID, requestedBy string) (application.SoftwarePackage, bool, error) {
	p, err := scanPackage(tx.QueryRow(ctx, `
		INSERT INTO endpoints.software_packages (provider, software_version_id, status, source, requested_by)
		VALUES ($1, $2::uuid, 'requested', 'operation', $3::uuid)
		ON CONFLICT (provider, software_version_id) DO NOTHING
		RETURNING `+packageColumns, provider, versionID, requestedBy))
	if err == nil {
		return p, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return application.SoftwarePackage{}, false, fmt.Errorf("insert software package: %w", err)
	}
	p, err = scanPackage(tx.QueryRow(ctx, `SELECT `+packageColumns+` FROM endpoints.software_packages WHERE provider = $1 AND software_version_id = $2::uuid FOR UPDATE`,
		provider, versionID))
	if err != nil {
		return application.SoftwarePackage{}, false, fmt.Errorf("read software package: %w", err)
	}
	return p, false, nil
}

func (r *Repository) LockPackageTx(ctx context.Context, tx pgx.Tx, id string) (application.SoftwarePackage, error) {
	p, err := scanPackage(tx.QueryRow(ctx, `SELECT `+packageColumns+` FROM endpoints.software_packages WHERE id = $1::uuid FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.SoftwarePackage{}, application.ErrNotFound
	}
	if err != nil {
		return application.SoftwarePackage{}, fmt.Errorf("lock software package: %w", err)
	}
	return p, nil
}

func (r *Repository) UpdatePackageTx(ctx context.Context, tx pgx.Tx, p application.SoftwarePackage) (application.SoftwarePackage, error) {
	out, err := scanPackage(tx.QueryRow(ctx, `
		UPDATE endpoints.software_packages SET provider_package_id = $2, status = $3, installer_sha256 = $4, management_provider = $5,
			management_artifact_external_id = $6, management_artifact_id = $7::uuid, source = $8, observed_at = $9, last_synced_at = $10,
			publish_requested_by = $11::uuid, publish_requested_at = $12, version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+packageColumns,
		p.ID, p.ProviderPackageID, p.Status, p.InstallerSHA256, p.ManagementProvider, p.ManagementArtifactExternalID, p.ManagementArtifactID,
		p.Source, p.ObservedAt, p.LastSyncedAt, p.PublishRequestedBy, p.PublishRequestedAt))
	if err != nil {
		return application.SoftwarePackage{}, fmt.Errorf("update software package: %w", err)
	}
	return out, nil
}

func (r *Repository) AppendPackageObservationTx(ctx context.Context, tx pgx.Tx, o application.PackageObservation) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO endpoints.software_package_observations (software_package_id, status, provider_package_id, installer_sha256,
			management_artifact_external_id, source, observed_at)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)`,
		o.PackageID, o.Status, o.ProviderPackageID, o.InstallerSHA256, o.ManagementArtifactExternalID, o.Source, o.ObservedAt)
	if err != nil {
		return fmt.Errorf("append software package observation: %w", err)
	}
	return nil
}

func (r *Repository) SyncablePackages(ctx context.Context, provider, after string, limit int) ([]application.SoftwarePackage, error) {
	conds := []string{"provider = $1", "provider_package_id IS NOT NULL"}
	args := []any{provider}
	if after != "" {
		args = append(args, after)
		conds = append(conds, fmt.Sprintf("id > $%d::uuid", len(args)))
	}
	items, err := listPage(ctx, r, `SELECT `+packageColumns+` FROM endpoints.software_packages`, conds, args, limit-1, scanPackage)
	if err != nil {
		return nil, fmt.Errorf("list syncable software packages: %w", err)
	}
	return items, nil
}

func (r *Repository) ListSoftwarePackages(ctx context.Context, f application.SoftwarePackageFilter) (application.SoftwarePackageResult, error) {
	page := f.Page.Normalize()
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.VersionID != "" {
		add("software_version_id = $%d::uuid", f.VersionID)
	}
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.SoftwarePackageResult{}, application.ErrInvalidCursor
		}
		add("id > $%d::uuid", page.Cursor)
	}
	items, err := listPage(ctx, r, `SELECT `+packageColumns+` FROM endpoints.software_packages`, conds, args, page.Limit, scanPackage)
	if err != nil {
		return application.SoftwarePackageResult{}, fmt.Errorf("list software packages: %w", err)
	}
	res := application.SoftwarePackageResult{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

func (r *Repository) ArtifactIDByExternalTx(ctx context.Context, tx pgx.Tx, provider, externalID string) (*string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM endpoints.management_artifacts WHERE provider = $1 AND external_id = $2 AND deleted_observed_at IS NULL`,
		provider, externalID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find management artifact: %w", err)
	}
	return &id, nil
}

func (r *Repository) OpenPackageFindingTx(ctx context.Context, tx pgx.Tx, kind, packageID string, detail []byte) (string, bool, error) {
	var id string
	var inserted bool
	err := tx.QueryRow(ctx, `
		INSERT INTO endpoints.findings (kind, software_package_id, detail) VALUES ($1, $2::uuid, $3::jsonb)
		ON CONFLICT (kind, software_package_id) WHERE status = 'open' AND software_package_id IS NOT NULL DO UPDATE SET detail = EXCLUDED.detail
			WHERE endpoints.findings.detail IS DISTINCT FROM EXCLUDED.detail
		RETURNING id::text, (xmax = 0)`, kind, packageID, detail).Scan(&id, &inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("open package finding: %w", err)
	}
	return id, inserted, nil
}

func (r *Repository) ResolvePackageFindingTx(ctx context.Context, tx pgx.Tx, kind, packageID string) (bool, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE endpoints.findings SET status = 'resolved', resolved_at = now()
		WHERE kind = $1 AND software_package_id = $2::uuid AND status = 'open'`, kind, packageID)
	if err != nil {
		return false, fmt.Errorf("resolve package finding: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
