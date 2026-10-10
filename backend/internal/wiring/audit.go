package wiring

import (
	"context"
	"regexp"

	"github.com/jackc/pgx/v5/pgxpool"

	assetspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/public"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	secpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/security/public"
	sdpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
)

// AuditResolvers labels audit actors and targets with display names. Only names come back (Organization's public
// contract returns nothing else); target types without a resolver keep their ids. Tickets resolve to their
// number (never the title), queues and roles to their name, assets to their tag or reference, security advisories to their identifier (never the title). Module keys are
// already readable and are translated by the client (modules.<key>.name).
func AuditResolvers(pool *pgxpool.Pool) *audit.Resolvers {
	dir := orgpublic.NewWorkDirectory(orgrepository.New(pool))
	sd := sdpublic.NewNameLookup(pool)
	as := assetspublic.NewNameLookup(pool)
	sec := secpublic.NewNameLookup(pool)
	return audit.NewResolvers(
		nameResolver{types: []string{"user"}, names: dir.UserNames},
		nameResolver{types: []string{"team"}, names: dir.TeamNames},
		nameResolver{types: []string{"location"}, names: dir.LocationNames},
		nameResolver{types: []string{"ticket"}, names: uuidOnly(sd.TicketNumbers)},
		nameResolver{types: []string{"ticket_queue"}, names: uuidOnly(sd.QueueNames)},
		nameResolver{types: []string{"security_advisory", "advisory"}, names: uuidOnly(sec.AdvisoryLabels)},
		nameResolver{types: []string{"asset"}, names: uuidOnly(as.AssetLabels)},
		nameResolver{types: []string{"role"}, names: uuidOnly(func(ctx context.Context, ids []string) (map[string]string, error) {
			return roles.RoleNames(ctx, pool, ids)
		})},
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

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// uuidOnly drops ids that are not UUIDs before the lookup: audit target ids are free text for some events and a
// malformed id must not fail the whole batch.
func uuidOnly(f func(ctx context.Context, ids []string) (map[string]string, error)) func(ctx context.Context, ids []string) (map[string]string, error) {
	return func(ctx context.Context, ids []string) (map[string]string, error) {
		valid := make([]string, 0, len(ids))
		for _, id := range ids {
			if uuidPattern.MatchString(id) {
				valid = append(valid, id)
			}
		}
		if len(valid) == 0 {
			return map[string]string{}, nil
		}
		return f(ctx, valid)
	}
}
