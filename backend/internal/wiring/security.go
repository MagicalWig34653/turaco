package wiring

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	changespublic "github.com/MagicalWig34653/turaco/backend/internal/modules/changes/public"
	endpointspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/public"
	endpointsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/repository"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	securityapp "github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
	securityrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/security/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

type securityInventory struct{ source *endpointspublic.Inventory }

func securityInstallations(in []endpointspublic.Installation) []securityapp.Installation {
	out := make([]securityapp.Installation, 0, len(in))
	for _, v := range in {
		out = append(out, securityapp.Installation{ID: v.ID, DeviceID: v.DeviceID, DevicePlatform: v.DevicePlatform,
			SoftwareProductID: v.SoftwareProductID, RawVersion: v.RawVersion, ObservedAt: v.ObservedAt,
			Retired: v.Retired, RetiredAt: v.RetiredAt, DeviceRetired: v.DeviceRetired})
	}
	return out
}

func (v securityInventory) InstallationsByProducts(ctx context.Context, ids []string, cursor string, limit int) ([]securityapp.Installation, string, error) {
	p, err := v.source.InstallationsByProducts(ctx, ids, false, endpointspublic.Scope{}, cursor, limit)
	return securityInstallations(p.Items), p.NextCursor, err
}

func (v securityInventory) InstallationsOnDevices(ctx context.Context, devices, products []string) ([]securityapp.Installation, bool, error) {
	p, truncated, err := v.source.InstallationsOnDevices(ctx, devices, products, endpointspublic.Scope{}, 10000)
	return securityInstallations(p), truncated, err
}

func (v securityInventory) DeviceRetired(ctx context.Context, ids []string) (map[string]*time.Time, error) {
	return v.source.DeviceRetired(ctx, ids)
}

func (v securityInventory) DeviceNames(ctx context.Context, ids []string) (map[string]string, error) {
	return v.source.DeviceNames(ctx, ids)
}

func (v securityInventory) LatestIngestionAt(ctx context.Context) (*time.Time, error) {
	return v.source.LatestIngestionAt(ctx)
}

func (v securityInventory) LatestObservedByProducts(ctx context.Context, ids []string) (map[string]time.Time, error) {
	return v.source.LatestObservedByProducts(ctx, ids)
}

func (v securityInventory) SoftwareProducts(ctx context.Context, ids []string) (map[string]securityapp.SoftwareProduct, error) {
	products, err := v.source.SoftwareProducts(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]securityapp.SoftwareProduct, len(products))
	for id, p := range products {
		out[id] = securityapp.SoftwareProduct{ID: p.ID, Name: p.Name, Publisher: p.Publisher}
	}
	return out, nil
}

func (v securityInventory) FindSoftwareProduct(ctx context.Context, name, publisher string) (securityapp.SoftwareProduct, string, bool, error) {
	p, method, found, err := v.source.FindSoftwareProduct(ctx, name, publisher)
	return securityapp.SoftwareProduct{ID: p.ID, Name: p.Name, Publisher: p.Publisher}, method, found, err
}

// Security builds the Security service using only Endpoints' public inventory contract.
func Security(pool *pgxpool.Pool) *securityapp.Service {
	dir := orgpublic.NewWorkDirectory(orgrepository.New(pool))
	return securityapp.NewService(securityrepository.New(pool), securityInventory{endpointspublic.NewInventory(endpointsrepository.New(pool))}).WithRemediation(
		taskspublicCreator(pool, dir), changespublic.NewChanges(Changes(pool)), Relationships(), dir, pool)
}

// SecurityNotifications builds the advisory notification consumer over Organization and roles contracts.
func SecurityNotifications(pool *pgxpool.Pool, notifier *notifications.Service) *securityapp.Notifications {
	org := orgrepository.New(pool)
	return securityapp.NewNotifications(securityrepository.New(pool), orgpublic.NewWorkDirectory(org),
		orgpublic.NewNotificationRecipients(org), roles.NewEvaluator(pool, orgpublic.NewAuthorizationSubjects(org)), notifier)
}
