package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
)

var _ application.ManagementStore = (*Repository)(nil)

// ---- filters ----

const filterColumns = `id::text, provider, external_id, name, platform, rule, revision, source, observed_at, last_synced_at,
	deleted_observed_at, version, created_at, updated_at`

func scanFilter(row pgx.Row) (application.Filter, error) {
	var f application.Filter
	err := row.Scan(&f.ID, &f.Provider, &f.ExternalID, &f.Name, &f.Platform, &f.Rule, &f.Revision, &f.Source, &f.ObservedAt,
		&f.LastSyncedAt, &f.DeletedObservedAt, &f.Version, &f.CreatedAt, &f.UpdatedAt)
	return f, err
}

func (r *Repository) LockFilterByExternalTx(ctx context.Context, tx pgx.Tx, provider, externalID string) (*application.Filter, error) {
	f, err := scanFilter(tx.QueryRow(ctx, `SELECT `+filterColumns+` FROM endpoints.management_filters WHERE provider = $1 AND external_id = $2 FOR UPDATE`, provider, externalID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lock filter: %w", err)
	}
	return &f, nil
}

func (r *Repository) InsertFilterTx(ctx context.Context, tx pgx.Tx, n application.NewFilter) (application.Filter, bool, error) {
	f, err := scanFilter(tx.QueryRow(ctx, `
		INSERT INTO endpoints.management_filters (provider, external_id, name, platform, rule, revision, source, observed_at, last_synced_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (provider, external_id) DO NOTHING
		RETURNING `+filterColumns, n.Provider, n.ExternalID, n.Name, n.Platform, n.Rule, n.Revision, n.Source, n.ObservedAt, n.SyncedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Filter{}, false, nil
	}
	if err != nil {
		return application.Filter{}, false, fmt.Errorf("insert filter: %w", err)
	}
	return f, true, nil
}

func (r *Repository) UpdateFilterTx(ctx context.Context, tx pgx.Tx, f application.Filter) (application.Filter, error) {
	out, err := scanFilter(tx.QueryRow(ctx, `
		UPDATE endpoints.management_filters SET name = $2, platform = $3, rule = $4, revision = $5, source = $6, observed_at = $7,
			last_synced_at = GREATEST(last_synced_at, $8), deleted_observed_at = $9, version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+filterColumns, f.ID, f.Name, f.Platform, f.Rule, f.Revision, f.Source, f.ObservedAt, f.LastSyncedAt, f.DeletedObservedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Filter{}, application.ErrNotFound
	}
	if err != nil {
		return application.Filter{}, fmt.Errorf("update filter: %w", err)
	}
	return out, nil
}

func (r *Repository) TouchFilterTx(ctx context.Context, tx pgx.Tx, id string, observedAt, syncedAt time.Time, source string) error {
	_, err := tx.Exec(ctx, `UPDATE endpoints.management_filters SET observed_at = $2, last_synced_at = GREATEST(last_synced_at, $3), source = $4 WHERE id = $1::uuid`, id, observedAt, syncedAt, source)
	if err != nil {
		return fmt.Errorf("touch filter: %w", err)
	}
	return nil
}

func (r *Repository) ResolveFiltersTx(ctx context.Context, tx pgx.Tx, provider string, externalIDs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(externalIDs) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT external_id, id::text FROM endpoints.management_filters WHERE provider = $1 AND external_id = ANY($2) AND deleted_observed_at IS NULL`, provider, externalIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve filters: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ext, id string
		if err := rows.Scan(&ext, &id); err != nil {
			return nil, fmt.Errorf("resolve filters: scan: %w", err)
		}
		out[ext] = id
	}
	return out, rows.Err()
}

func (r *Repository) FilterTombstoneCandidatesTx(ctx context.Context, tx pgx.Tx, provider string, before time.Time, keep []string) ([]string, int, error) {
	var live int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM endpoints.management_filters WHERE provider = $1 AND deleted_observed_at IS NULL`, provider).Scan(&live); err != nil {
		return nil, 0, fmt.Errorf("count live filters: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text FROM endpoints.management_filters
		WHERE provider = $1 AND deleted_observed_at IS NULL AND last_synced_at < $2 AND external_id <> ALL($3::text[])
		ORDER BY id FOR UPDATE`, provider, before, keep)
	if err != nil {
		return nil, 0, fmt.Errorf("filter tombstone candidates: %w", err)
	}
	ids, err := collectStrings(rows, "filter tombstone candidates")
	return ids, live, err
}

func (r *Repository) TombstoneFiltersTx(ctx context.Context, tx pgx.Tx, ids []string, at time.Time) (int, error) {
	tag, err := tx.Exec(ctx, `UPDATE endpoints.management_filters SET deleted_observed_at = $2, version = version + 1, updated_at = now()
		WHERE id = ANY($1::uuid[]) AND deleted_observed_at IS NULL`, ids, at)
	if err != nil {
		return 0, fmt.Errorf("tombstone filters: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ---- artifacts ----

const artifactColumns = `id::text, provider, external_id, kind, name, platform, software_product_id::text, revision, source, observed_at,
	last_synced_at, deleted_observed_at, version, created_at, updated_at`

func scanArtifact(row pgx.Row) (application.Artifact, error) {
	var a application.Artifact
	err := row.Scan(&a.ID, &a.Provider, &a.ExternalID, &a.Kind, &a.Name, &a.Platform, &a.SoftwareProductID, &a.Revision, &a.Source,
		&a.ObservedAt, &a.LastSyncedAt, &a.DeletedObservedAt, &a.Version, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

func (r *Repository) LockArtifactByExternalTx(ctx context.Context, tx pgx.Tx, provider, externalID string) (*application.Artifact, error) {
	a, err := scanArtifact(tx.QueryRow(ctx, `SELECT `+artifactColumns+` FROM endpoints.management_artifacts WHERE provider = $1 AND external_id = $2 FOR UPDATE`, provider, externalID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lock artifact: %w", err)
	}
	return &a, nil
}

func (r *Repository) InsertArtifactTx(ctx context.Context, tx pgx.Tx, n application.NewArtifact) (application.Artifact, bool, error) {
	a, err := scanArtifact(tx.QueryRow(ctx, `
		INSERT INTO endpoints.management_artifacts (provider, external_id, kind, name, platform, software_product_id, revision, source, observed_at, last_synced_at)
		VALUES ($1, $2, $3, $4, $5, $6::uuid, $7, $8, $9, $10)
		ON CONFLICT (provider, external_id) DO NOTHING
		RETURNING `+artifactColumns, n.Provider, n.ExternalID, n.Kind, n.Name, n.Platform, n.SoftwareProductID, n.Revision, n.Source, n.ObservedAt, n.SyncedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Artifact{}, false, nil
	}
	if err != nil {
		return application.Artifact{}, false, fmt.Errorf("insert artifact: %w", err)
	}
	return a, true, nil
}

func (r *Repository) UpdateArtifactTx(ctx context.Context, tx pgx.Tx, a application.Artifact) (application.Artifact, error) {
	out, err := scanArtifact(tx.QueryRow(ctx, `
		UPDATE endpoints.management_artifacts SET kind = $2, name = $3, platform = $4, software_product_id = $5::uuid, revision = $6, source = $7,
			observed_at = $8, last_synced_at = GREATEST(last_synced_at, $9), deleted_observed_at = $10, version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+artifactColumns, a.ID, a.Kind, a.Name, a.Platform, a.SoftwareProductID, a.Revision, a.Source, a.ObservedAt, a.LastSyncedAt, a.DeletedObservedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Artifact{}, application.ErrNotFound
	}
	if err != nil {
		return application.Artifact{}, fmt.Errorf("update artifact: %w", err)
	}
	return out, nil
}

func (r *Repository) TouchArtifactTx(ctx context.Context, tx pgx.Tx, id string, observedAt, syncedAt time.Time, source string) error {
	_, err := tx.Exec(ctx, `UPDATE endpoints.management_artifacts SET observed_at = $2, last_synced_at = GREATEST(last_synced_at, $3), source = $4 WHERE id = $1::uuid`, id, observedAt, syncedAt, source)
	if err != nil {
		return fmt.Errorf("touch artifact: %w", err)
	}
	return nil
}

func (r *Repository) ArtifactTombstoneCandidatesTx(ctx context.Context, tx pgx.Tx, provider string, before time.Time, keep []string) ([]string, int, error) {
	var live int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM endpoints.management_artifacts WHERE provider = $1 AND deleted_observed_at IS NULL`, provider).Scan(&live); err != nil {
		return nil, 0, fmt.Errorf("count live artifacts: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text FROM endpoints.management_artifacts
		WHERE provider = $1 AND deleted_observed_at IS NULL AND last_synced_at < $2 AND external_id <> ALL($3::text[])
		ORDER BY id FOR UPDATE`, provider, before, keep)
	if err != nil {
		return nil, 0, fmt.Errorf("artifact tombstone candidates: %w", err)
	}
	ids, err := collectStrings(rows, "artifact tombstone candidates")
	return ids, live, err
}

func (r *Repository) TombstoneArtifactsTx(ctx context.Context, tx pgx.Tx, ids []string, at time.Time) (map[string]int, []string, error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE endpoints.management_artifacts SET deleted_observed_at = $2, version = version + 1, updated_at = now()
		WHERE id = ANY($1::uuid[]) AND deleted_observed_at IS NULL`, ids, at); err != nil {
		return nil, nil, fmt.Errorf("tombstone artifacts: %w", err)
	}
	rows, err := tx.Query(ctx, `
		WITH closed AS (
			UPDATE endpoints.management_assignments SET valid_until = GREATEST(valid_from, $2)
			WHERE artifact_id = ANY($1::uuid[]) AND valid_until IS NULL RETURNING artifact_id)
		SELECT artifact_id::text, count(*) FROM closed GROUP BY artifact_id`, ids, at)
	if err != nil {
		return nil, nil, fmt.Errorf("close assignments of artifacts: %w", err)
	}
	closed := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("close assignments of artifacts: scan: %w", err)
		}
		closed[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("close assignments of artifacts: %w", err)
	}
	drows, err := tx.Query(ctx, `
		SELECT DISTINCT o.device_id::text FROM endpoints.management_observations o
		JOIN endpoints.devices d ON d.id = o.device_id
		WHERE o.artifact_id = ANY($1::uuid[]) AND d.deleted_observed_at IS NULL ORDER BY 1`, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("devices of artifacts: %w", err)
	}
	devices, err := collectStrings(drows, "devices of artifacts")
	return closed, devices, err
}

// ---- assignments ----

const assignmentColumns = `a.id::text, a.artifact_id::text, a.provider_assignment_id, a.target_kind, a.target_group_external_id, a.mode, a.intent,
	a.filter_id::text, a.filter_mode, a.source, a.observed_at, a.last_synced_at, a.valid_from, a.valid_until`

func scanAssignment(row pgx.Row) (application.Assignment, error) {
	var a application.Assignment
	err := row.Scan(&a.ID, &a.ArtifactID, &a.ProviderAssignmentID, &a.TargetKind, &a.TargetGroupExternalID, &a.Mode, &a.Intent,
		&a.FilterID, &a.FilterMode, &a.Source, &a.ObservedAt, &a.LastSyncedAt, &a.ValidFrom, &a.ValidUntil)
	return a, err
}

func (r *Repository) CurrentAssignmentsTx(ctx context.Context, tx pgx.Tx, artifactID string) ([]application.Assignment, error) {
	rows, err := tx.Query(ctx, `SELECT `+assignmentColumns+` FROM endpoints.management_assignments a
		WHERE a.artifact_id = $1::uuid AND a.valid_until IS NULL ORDER BY a.id FOR UPDATE`, artifactID)
	if err != nil {
		return nil, fmt.Errorf("current assignments: %w", err)
	}
	defer rows.Close()
	var out []application.Assignment
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, fmt.Errorf("current assignments: scan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repository) InsertAssignmentTx(ctx context.Context, tx pgx.Tx, artifactID string, a application.AssignmentInput, source string, validFrom, observedAt time.Time) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO endpoints.management_assignments (artifact_id, provider_assignment_id, target_kind, target_group_external_id, mode, intent,
			filter_id, filter_mode, source, observed_at, last_synced_at, valid_from)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7::uuid, $8, $9, $10, $10, $11)`,
		artifactID, a.ProviderAssignmentID, a.TargetKind, a.TargetGroupExternalID, a.Mode, a.Intent, a.FilterID, a.FilterMode, source, observedAt, validFrom)
	if err != nil {
		return fmt.Errorf("insert assignment: %w", err)
	}
	return nil
}

func (r *Repository) CloseAssignmentsTx(ctx context.Context, tx pgx.Tx, ids []string, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE endpoints.management_assignments SET valid_until = GREATEST(valid_from, $2) WHERE id = ANY($1::uuid[]) AND valid_until IS NULL`, ids, at)
	if err != nil {
		return fmt.Errorf("close assignments: %w", err)
	}
	return nil
}

func (r *Repository) TouchAssignmentsTx(ctx context.Context, tx pgx.Tx, ids []string, observedAt, syncedAt time.Time, source string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE endpoints.management_assignments SET observed_at = $2, last_synced_at = GREATEST(last_synced_at, $3), source = $4
		WHERE id = ANY($1::uuid[])`, ids, observedAt, syncedAt, source)
	if err != nil {
		return fmt.Errorf("touch assignments: %w", err)
	}
	return nil
}

// ---- observations ----

func (r *Repository) ResolveArtifactsTx(ctx context.Context, tx pgx.Tx, provider string, externalIDs []string) (map[string]application.ObjectRef, error) {
	return r.resolveObjects(ctx, tx, `SELECT external_id, id::text, deleted_observed_at IS NOT NULL FROM endpoints.management_artifacts WHERE provider = $1 AND external_id = ANY($2)`, provider, externalIDs)
}

func (r *Repository) ResolveDevicesTx(ctx context.Context, tx pgx.Tx, provider string, externalIDs []string) (map[string]application.ObjectRef, error) {
	return r.resolveObjects(ctx, tx, `SELECT external_id, id::text, deleted_observed_at IS NOT NULL FROM endpoints.devices WHERE provider = $1 AND external_id = ANY($2)`, provider, externalIDs)
}

func (r *Repository) resolveObjects(ctx context.Context, tx pgx.Tx, sql, provider string, externalIDs []string) (map[string]application.ObjectRef, error) {
	out := map[string]application.ObjectRef{}
	if len(externalIDs) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, sql, provider, externalIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve objects: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ext string
		var ref application.ObjectRef
		if err := rows.Scan(&ext, &ref.ID, &ref.Deleted); err != nil {
			return nil, fmt.Errorf("resolve objects: scan: %w", err)
		}
		out[ext] = ref
	}
	return out, rows.Err()
}

func (r *Repository) UpsertObservationsTx(ctx context.Context, tx pgx.Tx, in []application.ObservationInput, source string, syncedAt time.Time) ([]application.ObservationOutcome, error) {
	if len(in) == 0 {
		return nil, nil
	}
	arts, devs, states, raws, ats := make([]string, len(in)), make([]string, len(in)), make([]string, len(in)), make([]string, len(in)), make([]time.Time, len(in))
	for i, o := range in {
		arts[i], devs[i], states[i], raws[i], ats[i] = o.ArtifactID, o.DeviceID, o.State, o.RawStatus, o.ObservedAt
	}
	type key struct{ a, d string }
	prev := map[key][2]string{}
	rows, err := tx.Query(ctx, `
		SELECT o.artifact_id::text, o.device_id::text, o.normalized_state, o.raw_status
		FROM endpoints.management_observations o
		JOIN unnest($1::uuid[], $2::uuid[]) AS t(artifact_id, device_id) ON t.artifact_id = o.artifact_id AND t.device_id = o.device_id
		ORDER BY o.id FOR UPDATE OF o`, arts, devs)
	if err != nil {
		return nil, fmt.Errorf("lock observations: %w", err)
	}
	for rows.Next() {
		var a, d, s, raw string
		if err := rows.Scan(&a, &d, &s, &raw); err != nil {
			rows.Close()
			return nil, fmt.Errorf("lock observations: scan: %w", err)
		}
		prev[key{a, d}] = [2]string{s, raw}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("lock observations: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO endpoints.management_observations (artifact_id, device_id, normalized_state, raw_status, source, observed_at, last_synced_at)
		SELECT t.artifact_id, t.device_id, t.state, t.raw, $5, t.observed_at, $6
		FROM unnest($1::uuid[], $2::uuid[], $3::text[], $4::text[], $7::timestamptz[]) AS t(artifact_id, device_id, state, raw, observed_at)
		ON CONFLICT (artifact_id, device_id) DO UPDATE SET
			normalized_state = EXCLUDED.normalized_state, raw_status = EXCLUDED.raw_status, source = EXCLUDED.source,
			observed_at = GREATEST(endpoints.management_observations.observed_at, EXCLUDED.observed_at),
			last_synced_at = GREATEST(endpoints.management_observations.last_synced_at, EXCLUDED.last_synced_at)`,
		arts, devs, states, raws, source, syncedAt, ats)
	if err != nil {
		return nil, fmt.Errorf("upsert observations: %w", err)
	}
	out := make([]application.ObservationOutcome, len(in))
	for i, o := range in {
		p, existed := prev[key{o.ArtifactID, o.DeviceID}]
		out[i] = application.ObservationOutcome{ObservationInput: o, Created: !existed, Changed: !existed || p[0] != o.State || p[1] != o.RawStatus}
	}
	return out, nil
}

func (r *Repository) AppendObservationHistoryTx(ctx context.Context, tx pgx.Tx, in []application.ObservationInput, source string) error {
	if len(in) == 0 {
		return nil
	}
	arts, devs, states, raws, ats := make([]string, len(in)), make([]string, len(in)), make([]string, len(in)), make([]string, len(in)), make([]time.Time, len(in))
	for i, o := range in {
		arts[i], devs[i], states[i], raws[i], ats[i] = o.ArtifactID, o.DeviceID, o.State, o.RawStatus, o.ObservedAt
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO endpoints.management_observation_history (artifact_id, device_id, normalized_state, raw_status, source, observed_at)
		SELECT t.artifact_id, t.device_id, t.state, t.raw, $5, t.observed_at
		FROM unnest($1::uuid[], $2::uuid[], $3::text[], $4::text[], $6::timestamptz[]) AS t(artifact_id, device_id, state, raw, observed_at)`,
		arts, devs, states, raws, source, ats)
	if err != nil {
		return fmt.Errorf("append observation history: %w", err)
	}
	return nil
}

func (r *Repository) ProviderErrorsTx(ctx context.Context, tx pgx.Tx, deviceIDs []string) (map[string]application.ProviderErrors, error) {
	out := map[string]application.ProviderErrors{}
	if len(deviceIDs) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT d.id::text,
			count(*) FILTER (WHERE a.id IS NOT NULL AND o.normalized_state = 'failed'),
			count(*) FILTER (WHERE a.id IS NOT NULL AND o.normalized_state = 'conflict')
		FROM endpoints.devices d
		LEFT JOIN endpoints.management_observations o ON o.device_id = d.id AND o.normalized_state IN ('failed', 'conflict')
		LEFT JOIN endpoints.management_artifacts a ON a.id = o.artifact_id AND a.deleted_observed_at IS NULL
		WHERE d.id = ANY($1::uuid[]) AND d.deleted_observed_at IS NULL
		GROUP BY d.id`, deviceIDs)
	if err != nil {
		return nil, fmt.Errorf("provider errors: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var pe application.ProviderErrors
		if err := rows.Scan(&id, &pe.Failed, &pe.Conflict); err != nil {
			return nil, fmt.Errorf("provider errors: scan: %w", err)
		}
		out[id] = pe
	}
	return out, rows.Err()
}

// ---- memberships ----

func (r *Repository) UpsertMembershipsTx(ctx context.Context, tx pgx.Tx, provider, source string, in []application.MembershipInput, at time.Time) (int, error) {
	if len(in) == 0 {
		return 0, nil
	}
	devs, groups := make([]string, len(in)), make([]string, len(in))
	for i, m := range in {
		devs[i], groups[i] = m.DeviceID, m.GroupExternalID
	}
	tag, err := tx.Exec(ctx, `
		WITH input AS (SELECT * FROM unnest($3::uuid[], $4::text[]) AS t(device_id, group_id)),
		touched AS (
			UPDATE endpoints.device_group_memberships m SET last_synced_at = GREATEST(m.last_synced_at, $5), source = $2
			FROM input i WHERE m.device_id = i.device_id AND m.provider = $1 AND m.group_external_id = i.group_id AND m.observed_until IS NULL
			RETURNING m.device_id, m.group_external_id)
		INSERT INTO endpoints.device_group_memberships (device_id, provider, group_external_id, source, observed_from, last_synced_at)
		SELECT i.device_id, $1, i.group_id, $2, $5, $5 FROM input i
		WHERE NOT EXISTS (SELECT 1 FROM touched t WHERE t.device_id = i.device_id AND t.group_external_id = i.group_id)
		ON CONFLICT DO NOTHING`, provider, source, devs, groups, at)
	if err != nil {
		return 0, fmt.Errorf("upsert memberships: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *Repository) StaleMembershipsTx(ctx context.Context, tx pgx.Tx, provider string, before time.Time) (int, int, error) {
	var stale, live int
	err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE last_synced_at < $2), count(*) FROM endpoints.device_group_memberships
		WHERE provider = $1 AND observed_until IS NULL`, provider, before).Scan(&stale, &live)
	if err != nil {
		return 0, 0, fmt.Errorf("stale memberships: %w", err)
	}
	return stale, live, nil
}

func (r *Repository) CloseStaleMembershipsTx(ctx context.Context, tx pgx.Tx, provider string, before, at time.Time) (int, error) {
	tag, err := tx.Exec(ctx, `UPDATE endpoints.device_group_memberships SET observed_until = GREATEST(observed_from, $3)
		WHERE provider = $1 AND observed_until IS NULL AND last_synced_at < $2`, provider, before, at)
	if err != nil {
		return 0, fmt.Errorf("close stale memberships: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ---- reads ----

type listQuery struct {
	conds []string
	args  []any
}

func (q *listQuery) add(cond string, v any) {
	q.args = append(q.args, v)
	q.conds = append(q.conds, fmt.Sprintf(cond, len(q.args)))
}

func (q *listQuery) where() string {
	if len(q.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(q.conds, " AND ")
}

func (r *Repository) ListArtifacts(ctx context.Context, f application.ArtifactFilter) (application.ArtifactResult, error) {
	page := f.Page.Normalize()
	q := &listQuery{}
	if !f.IncludeDeleted {
		q.conds = append(q.conds, "deleted_observed_at IS NULL")
	}
	if f.Kind != "" {
		q.add("kind = $%d", f.Kind)
	}
	if f.Platform != "" {
		q.add("platform = $%d", f.Platform)
	}
	if f.Query != "" {
		q.add("lower(name) LIKE $%d", prefixPattern(strings.ToLower(f.Query)))
	}
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.ArtifactResult{}, application.ErrInvalidCursor
		}
		q.add("id > $%d::uuid", page.Cursor)
	}
	q.args = append(q.args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM endpoints.management_artifacts%s ORDER BY id LIMIT $%d`, artifactColumns, q.where(), len(q.args)), q.args...)
	if err != nil {
		return application.ArtifactResult{}, fmt.Errorf("list artifacts: %w", err)
	}
	defer rows.Close()
	items := make([]application.Artifact, 0, page.Limit+1)
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return application.ArtifactResult{}, fmt.Errorf("list artifacts: scan: %w", err)
		}
		items = append(items, a)
	}
	if err := rows.Err(); err != nil {
		return application.ArtifactResult{}, fmt.Errorf("list artifacts: %w", err)
	}
	res := application.ArtifactResult{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

func (r *Repository) GetArtifact(ctx context.Context, id string) (application.Artifact, error) {
	a, err := scanArtifact(r.pool.QueryRow(ctx, `SELECT `+artifactColumns+` FROM endpoints.management_artifacts WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Artifact{}, application.ErrNotFound
	}
	if err != nil {
		return application.Artifact{}, fmt.Errorf("get artifact: %w", err)
	}
	return a, nil
}

// maxAssignmentRows bounds the assignment rows (current and closed) one artifact read returns.
const maxAssignmentRows = 2000

func (r *Repository) ArtifactAssignments(ctx context.Context, artifactID string) ([]application.Assignment, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+assignmentColumns+`, f.id::text, f.name, f.platform, f.rule, f.deleted_observed_at IS NOT NULL
		FROM endpoints.management_assignments a
		LEFT JOIN endpoints.management_filters f ON f.id = a.filter_id
		WHERE a.artifact_id = $1::uuid
		ORDER BY (a.valid_until IS NOT NULL), a.id LIMIT `+fmt.Sprint(maxAssignmentRows), artifactID)
	if err != nil {
		return nil, fmt.Errorf("artifact assignments: %w", err)
	}
	defer rows.Close()
	out := []application.Assignment{}
	for rows.Next() {
		var a application.Assignment
		var fid, fname, fplatform, frule *string
		var fdeleted *bool
		if err := rows.Scan(&a.ID, &a.ArtifactID, &a.ProviderAssignmentID, &a.TargetKind, &a.TargetGroupExternalID, &a.Mode, &a.Intent,
			&a.FilterID, &a.FilterMode, &a.Source, &a.ObservedAt, &a.LastSyncedAt, &a.ValidFrom, &a.ValidUntil,
			&fid, &fname, &fplatform, &frule, &fdeleted); err != nil {
			return nil, fmt.Errorf("artifact assignments: scan: %w", err)
		}
		if fid != nil {
			a.Filter = &application.FilterSummary{ID: *fid, Name: *fname, Platform: *fplatform, Rule: *frule, Deleted: fdeleted != nil && *fdeleted}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repository) ArtifactObservationCounts(ctx context.Context, artifactID string) (map[string]int, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT o.normalized_state, count(*) FROM endpoints.management_observations o
		JOIN endpoints.devices d ON d.id = o.device_id AND d.deleted_observed_at IS NULL
		WHERE o.artifact_id = $1::uuid GROUP BY o.normalized_state`, artifactID)
	if err != nil {
		return nil, fmt.Errorf("observation counts: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, fmt.Errorf("observation counts: scan: %w", err)
		}
		out[s] = n
	}
	return out, rows.Err()
}

func (r *Repository) ListManagementFilters(ctx context.Context, f application.FilterListFilter) (application.ManagementFilterResult, error) {
	page := f.Page.Normalize()
	q := &listQuery{}
	if !f.IncludeDeleted {
		q.conds = append(q.conds, "deleted_observed_at IS NULL")
	}
	if f.Platform != "" {
		q.add("platform = $%d", f.Platform)
	}
	if f.Query != "" {
		q.add("lower(name) LIKE $%d", prefixPattern(strings.ToLower(f.Query)))
	}
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.ManagementFilterResult{}, application.ErrInvalidCursor
		}
		q.add("id > $%d::uuid", page.Cursor)
	}
	q.args = append(q.args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM endpoints.management_filters%s ORDER BY id LIMIT $%d`, filterColumns, q.where(), len(q.args)), q.args...)
	if err != nil {
		return application.ManagementFilterResult{}, fmt.Errorf("list filters: %w", err)
	}
	defer rows.Close()
	items := make([]application.Filter, 0, page.Limit+1)
	for rows.Next() {
		fi, err := scanFilter(rows)
		if err != nil {
			return application.ManagementFilterResult{}, fmt.Errorf("list filters: scan: %w", err)
		}
		items = append(items, fi)
	}
	if err := rows.Err(); err != nil {
		return application.ManagementFilterResult{}, fmt.Errorf("list filters: %w", err)
	}
	res := application.ManagementFilterResult{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

func (r *Repository) ListDeviceObservations(ctx context.Context, deviceID string, page application.Page) (application.ObservationResult, error) {
	page = page.Normalize()
	args := []any{deviceID}
	cursor := ""
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.ObservationResult{}, application.ErrInvalidCursor
		}
		args = append(args, page.Cursor)
		cursor = " AND o.id > $2::uuid"
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`
		SELECT o.id::text, o.artifact_id::text, a.name, a.kind, a.deleted_observed_at IS NOT NULL, o.device_id::text, o.normalized_state, o.raw_status,
			o.source, o.observed_at, o.last_synced_at
		FROM endpoints.management_observations o JOIN endpoints.management_artifacts a ON a.id = o.artifact_id
		WHERE o.device_id = $1::uuid%s ORDER BY o.id LIMIT $%d`, cursor, len(args)), args...)
	if err != nil {
		return application.ObservationResult{}, fmt.Errorf("list observations: %w", err)
	}
	defer rows.Close()
	items := make([]application.Observation, 0, page.Limit+1)
	for rows.Next() {
		var o application.Observation
		if err := rows.Scan(&o.ID, &o.ArtifactID, &o.ArtifactName, &o.ArtifactKind, &o.ArtifactDeleted, &o.DeviceID, &o.NormalizedState, &o.RawStatus,
			&o.Source, &o.ObservedAt, &o.LastSyncedAt); err != nil {
			return application.ObservationResult{}, fmt.Errorf("list observations: scan: %w", err)
		}
		items = append(items, o)
	}
	if err := rows.Err(); err != nil {
		return application.ObservationResult{}, fmt.Errorf("list observations: %w", err)
	}
	res := application.ObservationResult{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}
