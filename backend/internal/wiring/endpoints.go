package wiring

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	assetspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/public"
	endpointsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	endpointsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/repository"
)

// assetLookup adapts the Assets public contract to the questions Endpoints ask.
type assetLookup struct{ a *assetspublic.Assets }

func (l assetLookup) FindBySerial(ctx context.Context, serial string) (endpointsapp.AssetInfo, error) {
	a, err := l.a.FindBySerial(ctx, serial)
	switch {
	case errors.Is(err, assetspublic.ErrNotFound):
		return endpointsapp.AssetInfo{}, endpointsapp.ErrAssetNotFound
	case errors.Is(err, assetspublic.ErrConflict):
		return endpointsapp.AssetInfo{}, endpointsapp.ErrAssetAmbiguous
	case err != nil:
		return endpointsapp.AssetInfo{}, err
	}
	return endpointsapp.AssetInfo{ID: a.ID, SerialNumber: a.SerialNumber, Status: a.Status}, nil
}

func (l assetLookup) ByID(ctx context.Context, assetID string) (endpointsapp.AssetInfo, bool, error) {
	found, err := l.a.Assets(ctx, []string{assetID})
	if err != nil {
		return endpointsapp.AssetInfo{}, false, err
	}
	a, ok := found[assetID]
	if !ok {
		return endpointsapp.AssetInfo{}, false, nil
	}
	return endpointsapp.AssetInfo{ID: a.ID, SerialNumber: a.SerialNumber, Status: a.Status}, true, nil
}

// Endpoints builds the Endpoints service over the Assets public contract and the endpoint provider.
// syncEnabled switches POST /api/v1/endpoint-sync on.
func Endpoints(pool *pgxpool.Pool, provider intune.Provider, syncEnabled bool) *endpointsapp.Service {
	return endpointsapp.NewService(endpointsrepository.New(pool), assetLookup{assetspublic.New(Assets(pool))}, provider, syncEnabled, nil)
}
