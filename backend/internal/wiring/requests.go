// Package wiring assembles module services that depend on several modules'
// public contracts, so turaco-api and turaco-worker build them identically.
// It contains no business logic.
package wiring

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"

	approvalsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/application"
	approvalspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/public"
	approvalsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/repository"
	assetspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/public"
	catalogpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/public"
	catalogrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/repository"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	productspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/products/public"
	productsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/products/repository"
	requestsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/requests/application"
	requestsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/requests/repository"
	taskspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/public"
	tasksrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/repository"
)

// Requests builds the Service Request service over the other modules' public contracts.
func Requests(pool *pgxpool.Pool) *requestsapp.Service {
	dir := orgpublic.NewWorkDirectory(orgrepository.New(pool))
	products := catalogpublic.NewProducts(productspublic.NewDirectory(productsrepository.New(pool)))
	approvals := approvalspublic.New(approvalsapp.NewService(approvalsrepository.New(pool), dir, nil))
	tasks := taskspublicCreator(pool, dir, "service_request")
	return requestsapp.NewService(requestsrepository.New(pool), catalogpublic.New(catalogrepository.New(pool)),
		approvals, tasks, dir, dir, products, nil).WithAssets(requestAssets{assetspublic.New(Assets(pool))})
}

type requestAssets struct{ assets *assetspublic.Assets }

func (a requestAssets) RequestAsset(ctx context.Context, id, requesterID string, viewAll bool) (catalogpublic.AssetChoice, bool, error) {
	if viewAll {
		requesterID = ""
	}
	snap, err := a.assets.Snapshot(ctx, id, requesterID)
	if errors.Is(err, assetspublic.ErrNotFound) {
		return catalogpublic.AssetChoice{}, false, nil
	}
	if err != nil {
		return catalogpublic.AssetChoice{}, false, err
	}
	return catalogpublic.AssetChoice{Name: snap.Product, Reference: snap.Reference}, true, nil
}

func taskspublicCreator(pool *pgxpool.Pool, dir *orgpublic.WorkDirectory, owned ...string) *taskspublic.Creator {
	return taskspublic.NewCreator(tasksrepository.New(pool), dir, owned...)
}
