package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
)

// Inventory reads for endpoints/public. Callers validate ids and bound the limits.

const installationCols = `i.id::text, i.device_id::text, d.name, d.os_platform, i.software_product_id::text, i.raw_version,
	i.observed_at, i.deleted_observed_at, d.deleted_observed_at`

func scanInstallations(rows pgx.Rows) ([]application.InstallationRow, error) {
	defer rows.Close()
	var out []application.InstallationRow
	for rows.Next() {
		var r application.InstallationRow
		if err := rows.Scan(&r.ID, &r.DeviceID, &r.DeviceName, &r.DevicePlatform, &r.SoftwareProductID, &r.RawVersion,
			&r.ObservedAt, &r.RetiredAt, &r.DeviceRetiredAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InstallationsByProducts lists installations of the Software Products in id order after the cursor (an
// installation id; empty starts at the beginning). Without includeRetired only installations that are
// still reported on a Device that is not tombstoned are returned.
func (r *Repository) InstallationsByProducts(ctx context.Context, productIDs []string, includeRetired bool, after string, limit int) ([]application.InstallationRow, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+installationCols+`
		FROM endpoints.software_installations i JOIN endpoints.devices d ON d.id = i.device_id
		WHERE i.software_product_id = ANY($1::uuid[])
		  AND ($2 OR (i.deleted_observed_at IS NULL AND d.deleted_observed_at IS NULL))
		  AND ($3 = '' OR i.id > $3::uuid)
		ORDER BY i.id LIMIT $4`, productIDs, includeRetired, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list installations by product: %w", err)
	}
	out, err := scanInstallations(rows)
	if err != nil {
		return nil, fmt.Errorf("list installations by product: %w", err)
	}
	return out, nil
}

// InstallationsOnDevices lists the installations (retired ones included) of the Software Products on the
// Devices, by device and id, at most limit.
func (r *Repository) InstallationsOnDevices(ctx context.Context, deviceIDs, productIDs []string, limit int) ([]application.InstallationRow, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+installationCols+`
		FROM endpoints.software_installations i JOIN endpoints.devices d ON d.id = i.device_id
		WHERE i.device_id = ANY($1::uuid[]) AND i.software_product_id = ANY($2::uuid[])
		ORDER BY i.device_id, i.id LIMIT $3`, deviceIDs, productIDs, limit)
	if err != nil {
		return nil, fmt.Errorf("list installations on devices: %w", err)
	}
	out, err := scanInstallations(rows)
	if err != nil {
		return nil, fmt.Errorf("list installations on devices: %w", err)
	}
	return out, nil
}

// DeviceStates returns id -> tombstone time (nil while live) for the known Devices among ids.
func (r *Repository) DeviceStates(ctx context.Context, ids []string) (map[string]*time.Time, error) {
	rows, err := r.pool.Query(ctx, `SELECT id::text, deleted_observed_at FROM endpoints.devices WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("device states: %w", err)
	}
	defer rows.Close()
	out := map[string]*time.Time{}
	for rows.Next() {
		var id string
		var at *time.Time
		if err := rows.Scan(&id, &at); err != nil {
			return nil, fmt.Errorf("device states: %w", err)
		}
		out[id] = at
	}
	return out, rows.Err()
}

// DeviceNames returns id -> provider-reported name for the known Devices among ids.
func (r *Repository) DeviceNames(ctx context.Context, ids []string) (map[string]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT id::text, name FROM endpoints.devices WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("device names: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("device names: %w", err)
		}
		out[id] = name
	}
	return out, rows.Err()
}

// LatestIngestionAt returns the newest completion of a provider synchronization or, for imports, the
// newest device synchronization time; nil when nothing was ever ingested.
func (r *Repository) LatestIngestionAt(ctx context.Context) (*time.Time, error) {
	var at *time.Time
	err := r.pool.QueryRow(ctx, `SELECT GREATEST(
		(SELECT max(last_completed_at) FROM endpoints.provider_sync_state),
		(SELECT max(last_synced_at) FROM endpoints.devices))`).Scan(&at)
	if err != nil {
		return nil, fmt.Errorf("latest ingestion: %w", err)
	}
	return at, nil
}

// SoftwareProductsByIDs returns the known Software Products among ids.
func (r *Repository) SoftwareProductsByIDs(ctx context.Context, ids []string) ([]application.SoftwareProductRow, error) {
	rows, err := r.pool.Query(ctx, `SELECT id::text, name, publisher FROM endpoints.software_products WHERE id = ANY($1::uuid[]) ORDER BY id`, ids)
	if err != nil {
		return nil, fmt.Errorf("software products: %w", err)
	}
	return scanProducts(rows)
}

func scanProducts(rows pgx.Rows) ([]application.SoftwareProductRow, error) {
	defer rows.Close()
	var out []application.SoftwareProductRow
	for rows.Next() {
		var p application.SoftwareProductRow
		if err := rows.Scan(&p.ID, &p.Name, &p.Publisher); err != nil {
			return nil, fmt.Errorf("software products: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SoftwareProductsByName returns the Software Products whose name equals name (case-insensitively),
// restricted to the publisher when one is given; at most limit.
func (r *Repository) SoftwareProductsByName(ctx context.Context, name, publisher string, limit int) ([]application.SoftwareProductRow, error) {
	rows, err := r.pool.Query(ctx, `SELECT id::text, name, publisher FROM endpoints.software_products
		WHERE lower(name) = lower($1) AND ($2 = '' OR lower(coalesce(publisher, '')) = lower($2))
		ORDER BY id LIMIT $3`, strings.TrimSpace(name), strings.TrimSpace(publisher), limit)
	if err != nil {
		return nil, fmt.Errorf("software products by name: %w", err)
	}
	return scanProducts(rows)
}

// SoftwareProductByAlias returns the Software Product an alias key maps to.
func (r *Repository) SoftwareProductByAlias(ctx context.Context, alias string) (*application.SoftwareProductRow, error) {
	rows, err := r.pool.Query(ctx, `SELECT p.id::text, p.name, p.publisher FROM endpoints.software_aliases a
		JOIN endpoints.software_products p ON p.id = a.software_product_id WHERE a.alias = $1`, alias)
	if err != nil {
		return nil, fmt.Errorf("software product by alias: %w", err)
	}
	out, err := scanProducts(rows)
	if err != nil || len(out) == 0 {
		return nil, err
	}
	return &out[0], nil
}
