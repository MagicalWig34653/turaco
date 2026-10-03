package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
)

// Read port of the management views (F6 slice 3). Everything is bounded by the caller's limit.

func (r *Repository) scanDevices(rows pgx.Rows, what string) ([]application.Device, error) {
	defer rows.Close()
	out := []application.Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("%s: scan: %w", what, err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *Repository) DevicesByIDs(ctx context.Context, ids []string) ([]application.Device, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+deviceColumns+` FROM endpoints.devices WHERE id = ANY($1::uuid[]) ORDER BY id`, ids)
	if err != nil {
		return nil, fmt.Errorf("devices by ids: %w", err)
	}
	return r.scanDevices(rows, "devices by ids")
}

func (r *Repository) LiveDevicesByAssetIDs(ctx context.Context, assetIDs []string, limit int) ([]application.Device, error) {
	if len(assetIDs) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+deviceColumns+` FROM endpoints.devices
		WHERE asset_id = ANY($1::uuid[]) AND deleted_observed_at IS NULL ORDER BY id LIMIT $2`, assetIDs, limit)
	if err != nil {
		return nil, fmt.Errorf("devices by assets: %w", err)
	}
	return r.scanDevices(rows, "devices by assets")
}

func (r *Repository) LiveDevicesInGroups(ctx context.Context, groupExternalIDs []string, limit int) ([]application.Device, error) {
	if len(groupExternalIDs) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+deviceColumns+` FROM endpoints.devices
		WHERE deleted_observed_at IS NULL AND id IN (
			SELECT device_id FROM endpoints.device_group_memberships WHERE group_external_id = ANY($1) AND observed_until IS NULL)
		ORDER BY id LIMIT $2`, groupExternalIDs, limit)
	if err != nil {
		return nil, fmt.Errorf("devices in groups: %w", err)
	}
	return r.scanDevices(rows, "devices in groups")
}

func (r *Repository) LiveDevicesFirst(ctx context.Context, limit int) ([]application.Device, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+deviceColumns+` FROM endpoints.devices WHERE deleted_observed_at IS NULL ORDER BY id LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("first devices: %w", err)
	}
	return r.scanDevices(rows, "first devices")
}

func (r *Repository) DeviceMemberships(ctx context.Context, deviceIDs []string) ([]application.DeviceMembership, error) {
	if len(deviceIDs) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT device_id::text, group_external_id, last_synced_at FROM endpoints.device_group_memberships
		WHERE device_id = ANY($1::uuid[]) AND observed_until IS NULL ORDER BY device_id, group_external_id`, deviceIDs)
	if err != nil {
		return nil, fmt.Errorf("device memberships: %w", err)
	}
	defer rows.Close()
	var out []application.DeviceMembership
	for rows.Next() {
		var m application.DeviceMembership
		if err := rows.Scan(&m.DeviceID, &m.GroupExternalID, &m.LastSyncedAt); err != nil {
			return nil, fmt.Errorf("device memberships: scan: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *Repository) ReachableArtifacts(ctx context.Context, q application.ReachQuery) ([]application.Artifact, error) {
	if q.Limit <= 0 {
		q.Limit = application.DefaultLimit
	}
	after := q.AfterID
	if after == "" {
		after = "00000000-0000-0000-0000-000000000000"
	}
	rows, err := r.pool.Query(ctx, `SELECT `+artifactColumns+` FROM endpoints.management_artifacts
		WHERE deleted_observed_at IS NULL AND id > $1::uuid AND ($2 = '' OR kind = $2) AND (
			EXISTS (SELECT 1 FROM endpoints.management_assignments a WHERE a.artifact_id = management_artifacts.id AND a.valid_until IS NULL AND (
				(a.target_kind = 'group' AND ($8 OR a.target_group_external_id = ANY($3)))
				OR (a.target_kind = 'all_devices' AND $4) OR (a.target_kind = 'all_users' AND $5)))
			OR ($6 <> '' AND EXISTS (SELECT 1 FROM endpoints.management_observations o WHERE o.artifact_id = management_artifacts.id
				AND o.device_id = NULLIF($6, '')::uuid AND o.retired_at IS NULL)))
		ORDER BY id LIMIT $7`, after, q.Kind, q.GroupExternalIDs, q.AllDevices, q.AllUsers, q.DeviceID, q.Limit, q.AnyGroup)
	if err != nil {
		return nil, fmt.Errorf("reachable artifacts: %w", err)
	}
	return scanArtifacts(rows)
}

func scanArtifacts(rows pgx.Rows) ([]application.Artifact, error) {
	defer rows.Close()
	out := []application.Artifact{}
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, fmt.Errorf("artifacts: scan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repository) ArtifactsTargetingGroups(ctx context.Context, groupExternalIDs []string, afterID string, limit int) ([]application.Artifact, error) {
	if len(groupExternalIDs) == 0 {
		return nil, nil
	}
	if afterID == "" {
		afterID = "00000000-0000-0000-0000-000000000000"
	}
	rows, err := r.pool.Query(ctx, `SELECT `+artifactColumns+` FROM endpoints.management_artifacts
		WHERE deleted_observed_at IS NULL AND id > $1::uuid AND id IN (
			SELECT artifact_id FROM endpoints.management_assignments
			WHERE valid_until IS NULL AND target_kind = 'group' AND target_group_external_id = ANY($2))
		ORDER BY id LIMIT $3`, afterID, groupExternalIDs, limit)
	if err != nil {
		return nil, fmt.Errorf("artifacts targeting groups: %w", err)
	}
	return scanArtifacts(rows)
}

func (r *Repository) CurrentAssignmentsOf(ctx context.Context, artifactIDs []string) (map[string][]application.Assignment, error) {
	out := map[string][]application.Assignment{}
	if len(artifactIDs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+assignmentColumns+`, f.id::text, f.name, f.platform, f.rule, f.deleted_observed_at IS NOT NULL
		FROM endpoints.management_assignments a
		LEFT JOIN endpoints.management_filters f ON f.id = a.filter_id
		WHERE a.artifact_id = ANY($1::uuid[]) AND a.valid_until IS NULL
		ORDER BY a.artifact_id, a.id`, artifactIDs)
	if err != nil {
		return nil, fmt.Errorf("current assignments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var a application.Assignment
		var fid, fname, fplatform, frule *string
		var fdeleted *bool
		if err := rows.Scan(&a.ID, &a.ArtifactID, &a.ProviderAssignmentID, &a.TargetKind, &a.TargetGroupExternalID, &a.Mode, &a.Intent,
			&a.FilterID, &a.FilterMode, &a.Source, &a.ObservedAt, &a.LastSyncedAt, &a.ValidFrom, &a.ValidUntil,
			&fid, &fname, &fplatform, &frule, &fdeleted); err != nil {
			return nil, fmt.Errorf("current assignments: scan: %w", err)
		}
		if fid != nil {
			a.Filter = &application.FilterSummary{ID: *fid, Name: *fname, Platform: *fplatform, Rule: *frule, Deleted: fdeleted != nil && *fdeleted}
		}
		out[a.ArtifactID] = append(out[a.ArtifactID], a)
	}
	return out, rows.Err()
}

func (r *Repository) ObservationsOf(ctx context.Context, artifactIDs, deviceIDs []string) ([]application.Observation, error) {
	if len(artifactIDs) == 0 || len(deviceIDs) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT o.id::text, o.artifact_id::text, a.name, a.kind, a.deleted_observed_at IS NOT NULL, o.device_id::text, o.normalized_state, o.raw_status,
			o.source, o.observed_at, o.last_synced_at
		FROM endpoints.management_observations o JOIN endpoints.management_artifacts a ON a.id = o.artifact_id
		WHERE o.artifact_id = ANY($1::uuid[]) AND o.device_id = ANY($2::uuid[]) AND o.retired_at IS NULL
		ORDER BY o.artifact_id, o.device_id`, artifactIDs, deviceIDs)
	if err != nil {
		return nil, fmt.Errorf("observations of: %w", err)
	}
	defer rows.Close()
	var out []application.Observation
	for rows.Next() {
		var o application.Observation
		if err := rows.Scan(&o.ID, &o.ArtifactID, &o.ArtifactName, &o.ArtifactKind, &o.ArtifactDeleted, &o.DeviceID, &o.NormalizedState, &o.RawStatus,
			&o.Source, &o.ObservedAt, &o.LastSyncedAt); err != nil {
			return nil, fmt.Errorf("observations of: scan: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
