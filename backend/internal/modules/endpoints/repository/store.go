// Package repository implements the Endpoints store on PostgreSQL.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
)

// Repository stores devices, software and findings.
type Repository struct{ pool *pgxpool.Pool }

var _ application.Store = (*Repository)(nil)

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func pgCode(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code
	}
	return ""
}

func (r *Repository) InTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, r.pool, fn)
}

const deviceColumns = `id::text, provider, external_id, name, serial_number, asset_id::text, asset_link_source, auto_link_blocked,
	os_platform, os_version, manufacturer, model, ownership, compliance_state, last_checkin_at, source, observed_at,
	last_synced_at, deleted_observed_at, version, created_at, updated_at`

func scanDevice(row pgx.Row) (application.Device, error) {
	var d application.Device
	err := row.Scan(&d.ID, &d.Provider, &d.ExternalID, &d.Name, &d.SerialNumber, &d.AssetID, &d.AssetLinkSource, &d.AutoLinkBlocked,
		&d.OSPlatform, &d.OSVersion, &d.Manufacturer, &d.Model, &d.Ownership, &d.ComplianceState, &d.LastCheckinAt, &d.Source, &d.ObservedAt,
		&d.LastSyncedAt, &d.DeletedObservedAt, &d.Version, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}

func (r *Repository) LockDeviceByExternalTx(ctx context.Context, tx pgx.Tx, provider, externalID string) (*application.Device, error) {
	d, err := scanDevice(tx.QueryRow(ctx, `SELECT `+deviceColumns+` FROM endpoints.devices WHERE provider = $1 AND external_id = $2 FOR UPDATE`, provider, externalID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lock device: %w", err)
	}
	return &d, nil
}

func (r *Repository) LockDeviceTx(ctx context.Context, tx pgx.Tx, id string) (application.Device, error) {
	d, err := scanDevice(tx.QueryRow(ctx, `SELECT `+deviceColumns+` FROM endpoints.devices WHERE id = $1::uuid FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Device{}, application.ErrNotFound
	}
	if err != nil {
		return application.Device{}, fmt.Errorf("lock device: %w", err)
	}
	return d, nil
}

func (r *Repository) InsertDeviceTx(ctx context.Context, tx pgx.Tx, n application.NewDevice) (application.Device, bool, error) {
	d, err := scanDevice(tx.QueryRow(ctx, `
		INSERT INTO endpoints.devices (provider, external_id, name, serial_number, os_platform, os_version, manufacturer, model,
			ownership, compliance_state, last_checkin_at, source, observed_at, last_synced_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (provider, external_id) DO NOTHING
		RETURNING `+deviceColumns,
		n.Provider, n.ExternalID, n.Name, n.SerialNumber, n.OSPlatform, n.OSVersion, n.Manufacturer, n.Model,
		n.Ownership, n.ComplianceState, n.LastCheckinAt, n.Source, n.ObservedAt, n.SyncedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Device{}, false, nil
	}
	if err != nil {
		return application.Device{}, false, fmt.Errorf("insert device: %w", err)
	}
	return d, true, nil
}

func (r *Repository) UpdateDeviceTx(ctx context.Context, tx pgx.Tx, d application.Device) (application.Device, error) {
	out, err := scanDevice(tx.QueryRow(ctx, `
		UPDATE endpoints.devices SET name = $2, serial_number = $3, asset_id = $4::uuid, asset_link_source = $5, auto_link_blocked = $6,
			os_platform = $7, os_version = $8, manufacturer = $9, model = $10, ownership = $11, compliance_state = $12,
			last_checkin_at = $13, source = $14, observed_at = $15, last_synced_at = GREATEST(last_synced_at, $16), deleted_observed_at = $17,
			version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+deviceColumns,
		d.ID, d.Name, d.SerialNumber, d.AssetID, d.AssetLinkSource, d.AutoLinkBlocked,
		d.OSPlatform, d.OSVersion, d.Manufacturer, d.Model, d.Ownership, d.ComplianceState,
		d.LastCheckinAt, d.Source, d.ObservedAt, d.LastSyncedAt, d.DeletedObservedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Device{}, application.ErrNotFound
	}
	if pgCode(err) == "23505" {
		// devices_asset_live_unique: the Asset already belongs to another live device.
		return application.Device{}, application.ErrConflict
	}
	if err != nil {
		return application.Device{}, fmt.Errorf("update device: %w", err)
	}
	return out, nil
}

func (r *Repository) TouchDeviceTx(ctx context.Context, tx pgx.Tx, id string, observedAt, syncedAt time.Time, lastCheckin *time.Time, source string) error {
	_, err := tx.Exec(ctx, `
		UPDATE endpoints.devices SET observed_at = $2, last_synced_at = GREATEST(last_synced_at, $3), last_checkin_at = $4, source = $5
		WHERE id = $1::uuid`, id, observedAt, syncedAt, lastCheckin, source)
	if err != nil {
		return fmt.Errorf("touch device: %w", err)
	}
	return nil
}

// TryLockProvider holds a session-level advisory lock on a connection of its own for the whole run,
// so it survives the many short transactions of an ingestion.
func (r *Repository) TryLockProvider(ctx context.Context, provider string) (func(), bool, error) {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquire connection: %w", err)
	}
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('endpoints.ingest:' || $1, 0))`, provider).Scan(&got); err != nil {
		conn.Release()
		return nil, false, fmt.Errorf("lock provider: %w", err)
	}
	if !got {
		conn.Release()
		return nil, false, nil
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var released bool
		if err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock(hashtextextended('endpoints.ingest:' || $1, 0))`, provider).Scan(&released); err != nil || !released {
			// Never hand a connection that may still hold the lock back to the pool.
			_ = conn.Conn().Close(ctx)
		}
		conn.Release()
	}, true, nil
}

func (r *Repository) LastSyncCompleted(ctx context.Context, provider string) (*time.Time, error) {
	var at *time.Time // NULL while only the finding cursor has been stored
	err := r.pool.QueryRow(ctx, `SELECT last_completed_at FROM endpoints.provider_sync_state WHERE provider = $1`, provider).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("last sync completed: %w", err)
	}
	return at, nil
}

func (r *Repository) MarkSyncCompleted(ctx context.Context, provider string, at time.Time) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO endpoints.provider_sync_state (provider, last_completed_at) VALUES ($1, $2)
		ON CONFLICT (provider) DO UPDATE SET last_completed_at = EXCLUDED.last_completed_at`, provider, at)
	if err != nil {
		return fmt.Errorf("mark sync completed: %w", err)
	}
	return nil
}

func (r *Repository) AppendHistoryTx(ctx context.Context, tx pgx.Tx, h application.History) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO endpoints.device_observation_history (device_id, name, serial_number, os_platform, os_version, manufacturer, model,
			ownership, compliance_state, source, observed_at)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		h.DeviceID, h.Name, h.SerialNumber, h.OSPlatform, h.OSVersion, h.Manufacturer, h.Model, h.Ownership, h.ComplianceState, h.Source, h.ObservedAt)
	if err != nil {
		return fmt.Errorf("append device history: %w", err)
	}
	return nil
}

func (r *Repository) TombstoneCandidatesTx(ctx context.Context, tx pgx.Tx, provider string, before time.Time, keep []string) ([]string, int, error) {
	var live int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM endpoints.devices WHERE provider = $1 AND deleted_observed_at IS NULL`, provider).Scan(&live); err != nil {
		return nil, 0, fmt.Errorf("count live devices: %w", err)
	}
	// Rows are locked in id order so concurrent writers cannot deadlock with the run.
	rows, err := tx.Query(ctx, `
		SELECT id::text FROM endpoints.devices
		WHERE provider = $1 AND deleted_observed_at IS NULL AND last_synced_at < $2 AND external_id <> ALL($3::text[])
		ORDER BY id FOR UPDATE`, provider, before, keep)
	if err != nil {
		return nil, 0, fmt.Errorf("tombstone candidates: %w", err)
	}
	ids, err := collectStrings(rows, "tombstone candidates")
	return ids, live, err
}

func (r *Repository) TombstoneDevicesTx(ctx context.Context, tx pgx.Tx, ids []string, at time.Time) ([]application.Device, []application.Device, error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}
	// Serial-source links are only as good as the device being live; manual links stay (and are re-checked on revival).
	prev, err := r.devicesByIDs(ctx, tx, ids)
	if err != nil {
		return nil, nil, err
	}
	rows, err := tx.Query(ctx, `
		UPDATE endpoints.devices SET deleted_observed_at = $2, version = version + 1, updated_at = now(),
			asset_id = CASE WHEN asset_link_source = 'serial' THEN NULL ELSE asset_id END,
			asset_link_source = CASE WHEN asset_link_source = 'serial' THEN NULL ELSE asset_link_source END
		WHERE id = ANY($1::uuid[]) AND deleted_observed_at IS NULL
		RETURNING `+deviceColumns, ids, at)
	if err != nil {
		return nil, nil, fmt.Errorf("tombstone devices: %w", err)
	}
	defer rows.Close()
	var out []application.Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, nil, fmt.Errorf("tombstone devices: scan: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("tombstone devices: %w", err)
	}
	rows.Close()
	if _, err := tx.Exec(ctx, `UPDATE endpoints.software_installations SET deleted_observed_at = $2 WHERE device_id = ANY($1::uuid[]) AND deleted_observed_at IS NULL`, ids, at); err != nil {
		return nil, nil, fmt.Errorf("tombstone installations of devices: %w", err)
	}
	var unlinked []application.Device
	for _, p := range prev {
		if p.AssetLinkSource != nil && *p.AssetLinkSource == application.LinkSerial {
			unlinked = append(unlinked, p)
		}
	}
	return out, unlinked, nil
}

func (r *Repository) devicesByIDs(ctx context.Context, tx pgx.Tx, ids []string) ([]application.Device, error) {
	rows, err := tx.Query(ctx, `SELECT `+deviceColumns+` FROM endpoints.devices WHERE id = ANY($1::uuid[]) ORDER BY id`, ids)
	if err != nil {
		return nil, fmt.Errorf("devices by ids: %w", err)
	}
	defer rows.Close()
	var out []application.Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("devices by ids: scan: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func collectStrings(rows pgx.Rows, what string) ([]string, error) {
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("%s: scan: %w", what, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return out, nil
}

func (r *Repository) LiveDevicesBySerialTx(ctx context.Context, tx pgx.Tx, serial, excludeID string) ([]application.Device, error) {
	if excludeID == "" {
		excludeID = "00000000-0000-0000-0000-000000000000"
	}
	rows, err := tx.Query(ctx, `
		SELECT `+deviceColumns+` FROM endpoints.devices
		WHERE lower(serial_number) = lower($1) AND id <> $2::uuid AND deleted_observed_at IS NULL ORDER BY id LIMIT 10 FOR UPDATE`, serial, excludeID)
	if err != nil {
		return nil, fmt.Errorf("devices by serial: %w", err)
	}
	defer rows.Close()
	var out []application.Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("devices by serial: scan: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *Repository) OtherLiveDevicesByAssetTx(ctx context.Context, tx pgx.Tx, assetID, excludeID string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text FROM endpoints.devices
		WHERE asset_id = $1::uuid AND id <> $2::uuid AND deleted_observed_at IS NULL ORDER BY id LIMIT 10`, assetID, excludeID)
	if err != nil {
		return nil, fmt.Errorf("devices by asset: %w", err)
	}
	return collectStrings(rows, "devices by asset")
}

// ---- software ----

func (r *Repository) ResolveAliasesTx(ctx context.Context, tx pgx.Tx, aliases []string) (map[string]string, error) {
	out := map[string]string{}
	if len(aliases) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT alias, software_product_id::text FROM endpoints.software_aliases WHERE alias = ANY($1)`, aliases)
	if err != nil {
		return nil, fmt.Errorf("resolve aliases: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var alias, id string
		if err := rows.Scan(&alias, &id); err != nil {
			return nil, fmt.Errorf("resolve aliases: scan: %w", err)
		}
		out[alias] = id
	}
	return out, rows.Err()
}

func (r *Repository) UpsertInstallationsTx(ctx context.Context, tx pgx.Tx, deviceID string, items []application.InstallationInput, observedAt, syncedAt time.Time) error {
	if len(items) == 0 {
		return nil
	}
	names, keys, versions := make([]string, len(items)), make([]string, len(items)), make([]string, len(items))
	publishers, products := make([]*string, len(items)), make([]*string, len(items))
	for i, it := range items {
		names[i], keys[i], versions[i], publishers[i], products[i] = it.Name, it.Key, it.Version, it.Publisher, it.ProductID
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO endpoints.software_installations (device_id, software_product_id, raw_name, normalized_name, raw_version, raw_publisher, observed_at, last_synced_at)
		SELECT $1::uuid, t.product::uuid, t.name, t.key, t.version, t.publisher, $7, $8
		FROM unnest($2::text[], $3::text[], $4::text[], $5::text[], $6::text[]) AS t(name, key, version, publisher, product)
		ON CONFLICT (device_id, lower(raw_name), raw_version) DO UPDATE SET
			software_product_id = EXCLUDED.software_product_id, raw_publisher = EXCLUDED.raw_publisher,
			observed_at = EXCLUDED.observed_at, last_synced_at = EXCLUDED.last_synced_at, deleted_observed_at = NULL`,
		deviceID, names, keys, versions, publishers, products, observedAt, syncedAt)
	if err != nil {
		return fmt.Errorf("upsert installations: %w", err)
	}
	return nil
}

func (r *Repository) TombstoneInstallationsTx(ctx context.Context, tx pgx.Tx, deviceID string, before, at time.Time) error {
	_, err := tx.Exec(ctx, `
		UPDATE endpoints.software_installations SET deleted_observed_at = $3
		WHERE device_id = $1::uuid AND deleted_observed_at IS NULL AND last_synced_at < $2`, deviceID, before, at)
	if err != nil {
		return fmt.Errorf("tombstone installations: %w", err)
	}
	return nil
}

func (r *Repository) CountUnmatchedTx(ctx context.Context, tx pgx.Tx, deviceID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM endpoints.software_installations
		WHERE device_id = $1::uuid AND software_product_id IS NULL AND deleted_observed_at IS NULL`, deviceID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count unmatched software: %w", err)
	}
	return n, nil
}

func (r *Repository) InsertProductTx(ctx context.Context, tx pgx.Tx, name string, publisher *string, aliases []string) (application.ProductInfo, error) {
	var p application.ProductInfo
	err := tx.QueryRow(ctx, `INSERT INTO endpoints.software_products (name, publisher) VALUES ($1, $2) RETURNING id::text, name, publisher`, name, publisher).
		Scan(&p.ID, &p.Name, &p.Publisher)
	if pgCode(err) == "23505" {
		return application.ProductInfo{}, application.ErrConflict
	}
	if err != nil {
		return application.ProductInfo{}, fmt.Errorf("insert software product: %w", err)
	}
	for _, a := range aliases {
		if _, err := tx.Exec(ctx, `INSERT INTO endpoints.software_aliases (alias, software_product_id) VALUES ($1, $2::uuid)`, a, p.ID); err != nil {
			if pgCode(err) == "23505" {
				return application.ProductInfo{}, application.ErrConflict
			}
			return application.ProductInfo{}, fmt.Errorf("insert software alias: %w", err)
		}
	}
	return p, nil
}

func (r *Repository) RelinkInstallationsTx(ctx context.Context, tx pgx.Tx) (int, []string, error) {
	rows, err := tx.Query(ctx, `
		UPDATE endpoints.software_installations i SET software_product_id = a.software_product_id
		FROM endpoints.software_aliases a
		WHERE i.software_product_id IS NULL AND i.deleted_observed_at IS NULL AND a.alias = i.normalized_name
		RETURNING i.device_id::text`)
	if err != nil {
		return 0, nil, fmt.Errorf("relink installations: %w", err)
	}
	all, err := collectStrings(rows, "relink installations")
	if err != nil {
		return 0, nil, err
	}
	seen := map[string]bool{}
	var devices []string
	for _, id := range all {
		if !seen[id] {
			seen[id] = true
			devices = append(devices, id)
		}
	}
	slices.Sort(devices)
	return len(all), devices, nil
}

// ---- findings ----

func (r *Repository) OpenFindingTx(ctx context.Context, tx pgx.Tx, kind, deviceID string, detail json.RawMessage) (string, bool, error) {
	var id string
	var inserted bool
	err := tx.QueryRow(ctx, `
		INSERT INTO endpoints.findings (kind, device_id, detail) VALUES ($1, $2::uuid, $3::jsonb)
		ON CONFLICT (kind, device_id) WHERE status = 'open' DO UPDATE SET detail = EXCLUDED.detail
			WHERE endpoints.findings.detail IS DISTINCT FROM EXCLUDED.detail
		RETURNING id::text, (xmax = 0)`, kind, deviceID, []byte(detail)).Scan(&id, &inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("open finding: %w", err)
	}
	return id, inserted, nil
}

func (r *Repository) ResolveFindingTx(ctx context.Context, tx pgx.Tx, kind, deviceID string) (bool, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE endpoints.findings SET status = 'resolved', resolved_at = now()
		WHERE kind = $1 AND device_id = $2::uuid AND status = 'open'`, kind, deviceID)
	if err != nil {
		return false, fmt.Errorf("resolve finding: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ---- reads ----

func (r *Repository) GetDevice(ctx context.Context, id string) (application.Device, error) {
	d, err := scanDevice(r.q(ctx).QueryRow(ctx, `SELECT `+deviceColumns+` FROM endpoints.devices WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Device{}, application.ErrNotFound
	}
	if err != nil {
		return application.Device{}, fmt.Errorf("get device: %w", err)
	}
	return d, nil
}

func prefixPattern(q string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
}

func (r *Repository) ListDevices(ctx context.Context, f application.DeviceFilter) (application.DeviceResult, error) {
	page := f.Page.Normalize()
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if !f.IncludeDeleted {
		conds = append(conds, "deleted_observed_at IS NULL")
	}
	if f.Platform != "" {
		add("os_platform = $%d", f.Platform)
	}
	if f.Compliance != "" {
		add("compliance_state = $%d", f.Compliance)
	}
	if f.Linked != nil {
		if *f.Linked {
			conds = append(conds, "asset_id IS NOT NULL")
		} else {
			conds = append(conds, "asset_id IS NULL")
		}
	}
	if f.ManagementState != "" {
		add(`EXISTS (SELECT 1 FROM endpoints.management_observations o JOIN endpoints.management_artifacts a ON a.id = o.artifact_id AND a.deleted_observed_at IS NULL
			WHERE o.device_id = endpoints.devices.id AND o.retired_at IS NULL AND o.normalized_state = $%d)`, f.ManagementState)
	}
	if f.HasFinding != "" {
		add(`EXISTS (SELECT 1 FROM endpoints.findings f WHERE f.device_id = endpoints.devices.id AND f.status = 'open' AND f.kind = $%d)`, f.HasFinding)
	}
	if f.OSVersionPrefix != "" {
		add("os_version LIKE $%d", prefixPattern(f.OSVersionPrefix))
	}
	if f.LastCheckinBefore != nil {
		add("last_checkin_at < $%d", *f.LastCheckinBefore)
	}
	if f.Query != "" {
		args = append(args, prefixPattern(strings.ToLower(f.Query)))
		conds = append(conds, fmt.Sprintf(`(lower(name) LIKE $%[1]d OR lower(serial_number) LIKE $%[1]d)`, len(args)))
	}
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.DeviceResult{}, application.ErrInvalidCursor
		}
		add("id > $%d::uuid", page.Cursor)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM endpoints.devices%s ORDER BY id LIMIT $%d`, deviceColumns, where, len(args)), args...)
	if err != nil {
		return application.DeviceResult{}, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()
	items := make([]application.Device, 0, page.Limit+1)
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return application.DeviceResult{}, fmt.Errorf("list devices: scan: %w", err)
		}
		items = append(items, d)
	}
	if err := rows.Err(); err != nil {
		return application.DeviceResult{}, fmt.Errorf("list devices: %w", err)
	}
	res := application.DeviceResult{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

func (r *Repository) Installations(ctx context.Context, deviceID string) ([]application.Installation, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT i.id::text, i.device_id::text, i.software_product_id::text, p.name, i.raw_name, i.raw_version, i.raw_publisher, i.observed_at, i.last_synced_at
		FROM endpoints.software_installations i
		LEFT JOIN endpoints.software_products p ON p.id = i.software_product_id
		WHERE i.device_id = $1::uuid AND i.deleted_observed_at IS NULL
		ORDER BY lower(i.raw_name), i.raw_version LIMIT 5000`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("list installations: %w", err)
	}
	defer rows.Close()
	out := []application.Installation{}
	for rows.Next() {
		var i application.Installation
		if err := rows.Scan(&i.ID, &i.DeviceID, &i.SoftwareProduct, &i.ProductName, &i.RawName, &i.RawVersion, &i.RawPublisher, &i.ObservedAt, &i.LastSyncedAt); err != nil {
			return nil, fmt.Errorf("list installations: scan: %w", err)
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

const findingColumns = `f.id::text, f.kind, f.device_id::text, d.name, f.status, f.detail, f.raised_at, f.resolved_at`

func scanFinding(row pgx.Row) (application.Finding, error) {
	var f application.Finding
	var detail []byte
	err := row.Scan(&f.ID, &f.Kind, &f.DeviceID, &f.DeviceName, &f.Status, &detail, &f.RaisedAt, &f.ResolvedAt)
	f.Detail = json.RawMessage(detail)
	return f, err
}

func (r *Repository) OpenFindings(ctx context.Context, deviceID string) ([]application.Finding, error) {
	res, err := r.ListFindings(ctx, application.FindingFilter{Status: application.FindingOpen, DeviceID: deviceID, Page: application.Page{Limit: application.MaxLimit}})
	return res.Items, err
}

func (r *Repository) ListFindings(ctx context.Context, f application.FindingFilter) (application.FindingResult, error) {
	page := f.Page.Normalize()
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.Status != "" {
		add("f.status = $%d", f.Status)
	}
	if f.Kind != "" {
		add("f.kind = $%d", f.Kind)
	}
	if len(f.ExcludeKinds) > 0 {
		add("NOT (f.kind = ANY($%d::text[]))", f.ExcludeKinds)
	}
	if f.DeviceID != "" {
		add("f.device_id = $%d::uuid", f.DeviceID)
	}
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.FindingResult{}, application.ErrInvalidCursor
		}
		add("f.id > $%d::uuid", page.Cursor)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM endpoints.findings f JOIN endpoints.devices d ON d.id = f.device_id%s ORDER BY f.id LIMIT $%d`,
		findingColumns, where, len(args)), args...)
	if err != nil {
		return application.FindingResult{}, fmt.Errorf("list findings: %w", err)
	}
	defer rows.Close()
	items := make([]application.Finding, 0, page.Limit+1)
	for rows.Next() {
		fi, err := scanFinding(rows)
		if err != nil {
			return application.FindingResult{}, fmt.Errorf("list findings: scan: %w", err)
		}
		items = append(items, fi)
	}
	if err := rows.Err(); err != nil {
		return application.FindingResult{}, fmt.Errorf("list findings: %w", err)
	}
	res := application.FindingResult{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if r != '-' {
				return false
			}
		case !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F'):
			return false
		}
	}
	return true
}
