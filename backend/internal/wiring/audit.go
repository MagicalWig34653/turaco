package wiring

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// AuditResolvers labels audit actors and targets with display names. Only names come back (Organization's public
// contract returns nothing else); target types without a resolver keep their ids. Further types (tickets by
// reference, roles, assets) join when their modules offer a name contract.
func AuditResolvers(pool *pgxpool.Pool) *audit.Resolvers {
	dir := orgpublic.NewWorkDirectory(orgrepository.New(pool))
	return audit.NewResolvers(
		nameResolver{types: []string{"user"}, names: dir.UserNames},
		nameResolver{types: []string{"team"}, names: dir.TeamNames},
		nameResolver{types: []string{"location"}, names: dir.LocationNames},
	)
}

type nameResolver struct {
	types []string
	names func(ctx context.Context, ids []string) (map[string]string, error)
}

func (n nameResolver) Types() []string { return n.types }
func (n nameResolver) Names(ctx context.Context, ids []string) (map[string]string, error) {
	return n.names(ctx, ids)
}
