package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
)

// DeviceIdentity returns the identity and last observation of one Device (nil when unknown or the id is no UUID).
func (r *Repository) DeviceIdentity(ctx context.Context, id string) (*application.DeviceIdentityRow, error) {
	if !validUUID(id) {
		return nil, nil
	}
	var d application.DeviceIdentityRow
	err := r.pool.QueryRow(ctx, `SELECT id::text, name, asset_id::text, ownership, observed_at, last_checkin_at, deleted_observed_at
		FROM endpoints.devices WHERE id = $1::uuid`, id).Scan(&d.ID, &d.Name, &d.AssetID, &d.Ownership, &d.ObservedAt, &d.LastCheckinAt, &d.RetiredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("device identity: %w", err)
	}
	return &d, nil
}
