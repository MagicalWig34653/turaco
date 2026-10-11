package repository

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/assets/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

var _ application.QueryStore = (*Repository)(nil)

func (r *Repository) QueryAssets(ctx context.Context, plan *query.Plan) (query.Page[application.Asset], error) {
	return query.Run(ctx, r.pool, plan, query.Select{Columns: prefixColumns("a.")}, func(rows pgx.Rows, extra []any) (application.Asset, error) {
		var a application.Asset
		err := rows.Scan(append([]any{&a.ID, &a.Reference, &a.ProductID, &a.SerialNumber, &a.AssetTag, &a.Status, &a.StatusReason, &a.ProvisioningStatus,
			&a.OwnershipType, &a.SupplierID, &a.SourceType, &a.SourceID, &a.PurchasedAt, &a.WarrantyUntil, &a.FirmwareVersion, &a.FirmwareSource,
			&a.FirmwareObservedAt, &a.LastCalibratedAt, &a.NextCalibrationDue, &a.NextMaintenanceDue, &a.LocationID, &a.Notes,
			&a.Version, &a.CreatedAt, &a.UpdatedAt}, extra...)...)
		return a, err
	})
}
