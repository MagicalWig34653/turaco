// Package repository implements the Endpoints store on PostgreSQL.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
			last_checkin_at = $13, source = $14, observed_at = $15, last_synced_at = $16, deleted_observed_at = $17,
			version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+deviceColumns,
		d.ID, d.Name, d.SerialNumber, d.AssetID, d.AssetLinkSource, d.AutoLinkBlocked,
		d.OSPlatform, d.OSVersion, d.Manufacturer, d.Model, d.Ownership, d.ComplianceState,
		d.LastCheckinAt, d.Source, d.ObservedAt, d.LastSyncedAt, d.DeletedObservedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Device{}, application.ErrNotFound
	}
	if err != nil {
		return application.Device{}, fmt.Errorf("update device: %w", err)
	}
	return out, nil
}

func (r *Repository) TouchDeviceTx(ctx context.Context, tx pgx.Tx, id string, observedAt, syncedAt time.Time, lastCheckin *time.Time) error {
	_, err := tx.Exec(ctx, `
		UPDATE endpoints.devices SET observed_at = $2, last_synced_at = $3, last_checkin_at = $4, deleted_observed_at = NULL
		WHERE id = $1::uuid`, id, observedAt, syncedAt, lastCheckin)
	if err != nil {
		return fmt.Errorf("touch device: %w", err)
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

func (r *Repository) TombstoneMissingTx(ctx context.Context, tx pgx.Tx, provider string, before, at time.Time) ([]string, error) {
	rows, err := tx.Query(ctx, `
		UPDATE endpoints.devices SET deleted_observed_at = $3, version = version + 1, updated_at = now()
		WHERE provider = $1 AND deleted_observed_at IS NULL AND last_synced_at < $2
		RETURNING id::text`, provider, before, at)
	if err != nil {
		return nil, fmt.Errorf("tombstone devices: %w", err)
	}
	return collectStrings(rows, "tombstone devices")
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

func (r *Repository) OtherLiveDevicesBySerialTx(ctx context.Context, tx pgx.Tx, serial, excludeID string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text FROM endpoints.devices
		WHERE lower(serial_number) = lower($1) AND id <> $2::uuid AND deleted_observed_at IS NULL ORDER BY id LIMIT 10`, serial, excludeID)
	if err != nil {
		return nil, fmt.Errorf("devices by serial: %w", err)
	}
	return collectStrings(rows, "devices by serial")
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

func (r *Repository) UpsertInstallationTx(ctx context.Context, tx pgx.Tx, deviceID string, productID *string, name, version string, publisher *string, observedAt, syncedAt time.Time) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO endpoints.software_installations (device_id, software_product_id, raw_name, raw_version, raw_publisher, observed_at, last_synced_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7)
		ON CONFLICT (device_id, lower(raw_name), raw_version) DO UPDATE SET
			software_product_id = EXCLUDED.software_product_id, raw_publisher = EXCLUDED.raw_publisher,
			observed_at = EXCLUDED.observed_at, last_synced_at = EXCLUDED.last_synced_at, deleted_observed_at = NULL`,
		deviceID, productID, name, version, publisher, observedAt, syncedAt)
	if err != nil {
		return fmt.Errorf("upsert installation: %w", err)
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

func (r *Repository) RelinkInstallationsTx(ctx context.Context, tx pgx.Tx) (int, error) {
	// Matching key = application.NormalizeSoftwareName: lower case, whitespace runs collapsed to one space.
	tag, err := tx.Exec(ctx, `
		UPDATE endpoints.software_installations i SET software_product_id = a.software_product_id
		FROM endpoints.software_aliases a
		WHERE i.software_product_id IS NULL AND i.deleted_observed_at IS NULL
		  AND a.alias = lower(btrim(regexp_replace(i.raw_name, '\s+', ' ', 'g')))`)
	if err != nil {
		return 0, fmt.Errorf("relink installations: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ---- findings ----

func (r *Repository) OpenFindingTx(ctx context.Context, tx pgx.Tx, kind, deviceID string, detail json.RawMessage) (string, bool, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO endpoints.findings (kind, device_id, detail) VALUES ($1, $2::uuid, $3::jsonb)
		ON CONFLICT (kind, device_id) WHERE status = 'open' DO NOTHING
		RETURNING id::text`, kind, deviceID, []byte(detail)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("open finding: %w", err)
	}
	return id, true, nil
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
	d, err := scanDevice(r.pool.QueryRow(ctx, `SELECT `+deviceColumns+` FROM endpoints.devices WHERE id = $1::uuid`, id))
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
