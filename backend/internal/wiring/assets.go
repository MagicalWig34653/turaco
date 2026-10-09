package wiring

import (
	"github.com/jackc/pgx/v5/pgxpool"

	assetsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/application"
	assetspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/public"
	assetsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/repository"
	endpointspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/public"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	productspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/products/public"
	productsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/products/repository"
)

// Assets builds the Asset service over the other modules' public contracts.
func Assets(pool *pgxpool.Pool) *assetsapp.Service {
	dir := orgpublic.NewWorkDirectory(orgrepository.New(pool))
	products := assetspublic.NewProducts(productspublic.NewDirectory(productsrepository.New(pool)))
	return assetsapp.NewService(assetsrepository.New(pool), dir, products, nil).WithDeviceSearch(endpointspublic.NewHostnames(pool))
}
