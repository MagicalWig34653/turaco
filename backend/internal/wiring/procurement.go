package wiring

import (
	"github.com/jackc/pgx/v5/pgxpool"

	approvalsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/application"
	approvalspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/public"
	approvalsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/repository"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	procurementapp "github.com/MagicalWig34653/turaco/backend/internal/modules/procurement/application"
	procurementpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/procurement/public"
	procurementrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/procurement/repository"
	productspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/products/public"
	productsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/products/repository"
)

// Procurement builds the Procurement service over the other modules' public contracts.
func Procurement(pool *pgxpool.Pool) *procurementapp.Service {
	dir := orgpublic.NewWorkDirectory(orgrepository.New(pool))
	products := procurementpublic.NewProducts(productspublic.NewDirectory(productsrepository.New(pool)))
	approvals := procurementpublic.NewApprovals(approvalspublic.New(approvalsapp.NewService(approvalsrepository.New(pool), dir, nil)))
	return procurementapp.NewService(procurementrepository.New(pool), dir, products, approvals)
}
