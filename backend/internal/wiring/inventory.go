package wiring

import (
	"github.com/jackc/pgx/v5/pgxpool"

	assetspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/public"
	inventoryapp "github.com/MagicalWig34653/turaco/backend/internal/modules/inventory/application"
	inventorypublic "github.com/MagicalWig34653/turaco/backend/internal/modules/inventory/public"
	inventoryrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/inventory/repository"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	productspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/products/public"
	productsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/products/repository"
)

// Inventory builds the Inventory service over the other modules' public contracts.
func Inventory(pool *pgxpool.Pool) *inventoryapp.Service {
	dir := orgpublic.NewWorkDirectory(orgrepository.New(pool))
	products := inventorypublic.NewProducts(productspublic.NewDirectory(productsrepository.New(pool)))
	assets := inventorypublic.NewAssets(assetspublic.New(Assets(pool)))
	return inventoryapp.NewService(inventoryrepository.New(pool), dir, products, assets)
}
