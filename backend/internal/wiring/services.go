package wiring

import (
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	assetspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/public"
	infrapublic "github.com/MagicalWig34653/turaco/backend/internal/modules/infrastructure/public"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	servicesapp "github.com/MagicalWig34653/turaco/backend/internal/modules/services/application"
	servicespublic "github.com/MagicalWig34653/turaco/backend/internal/modules/services/public"
	servicesrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/services/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

var (
	graphOnce sync.Once
	graph     *relationships.Graph
)

// Relationships returns the process-wide relationship graph. Modules that own
// relationship triples register them here (Services today; Changes will add
// "Change AFFECTS ...").
func Relationships() *relationships.Graph {
	graphOnce.Do(func() {
		reg := relationships.NewRegistry()
		reg.Register(servicesapp.Triples...)
		graph = relationships.New(reg)
	})
	return graph
}

func servicesDeps(pool *pgxpool.Pool) (servicesapp.Directory, servicesapp.Infrastructure, servicesapp.Assets) {
	dir := orgpublic.NewWorkDirectory(orgrepository.New(pool))
	infra := servicespublic.NewInfrastructure(infrapublic.New(Infrastructure(pool)))
	assets := servicespublic.NewAssets(assetspublic.New(Assets(pool)))
	return dir, infra, assets
}

// Services builds the Services application over the other modules' public contracts.
func Services(pool *pgxpool.Pool) *servicesapp.App {
	dir, infra, assets := servicesDeps(pool)
	return servicesapp.NewApp(servicesrepository.New(pool), Relationships(), dir, infra, assets)
}

// ServiceVMLinks builds the outbox consumer that keeps "VM RUNS_ON Asset" in step with Infrastructure.
func ServiceVMLinks(pool *pgxpool.Pool) *servicesapp.VMLinks {
	_, infra, _ := servicesDeps(pool)
	return servicesapp.NewVMLinks(servicesrepository.New(pool), Relationships(), infra)
}
