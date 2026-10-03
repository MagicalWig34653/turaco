package wiring

import (
	"github.com/jackc/pgx/v5/pgxpool"

	assetspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/public"
	infraapp "github.com/MagicalWig34653/turaco/backend/internal/modules/infrastructure/application"
	infrapublic "github.com/MagicalWig34653/turaco/backend/internal/modules/infrastructure/public"
	infrarepository "github.com/MagicalWig34653/turaco/backend/internal/modules/infrastructure/repository"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
)

// Infrastructure builds the Infrastructure service over the other modules' public contracts.
func Infrastructure(pool *pgxpool.Pool) *infraapp.Service {
	dir := orgpublic.NewWorkDirectory(orgrepository.New(pool))
	assets := infrapublic.NewAssets(assetspublic.New(Assets(pool)))
	return infraapp.NewService(infrarepository.New(pool), dir, assets)
}
