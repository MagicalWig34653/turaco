package wiring

import (
	"context"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	endpointsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	tasksapp "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

// ValidateQueryCatalogs fails API startup when a declared field does not
// match the migrated schema. Catalog expressions are never accepted from HTTP.
func ValidateQueryCatalogs(ctx context.Context, pool *pgxpool.Pool) error {
	return query.ValidateSchema(ctx, pool, servicedeskapp.TicketCatalog(), endpointsapp.DeviceCatalog(), tasksapp.TaskCatalog())
}

var (
	queryEngineOnce sync.Once
	queryEngine     *query.Engine
)

// QueryEngine is the process-wide query engine of ADR-0033: one rate limiter
// for all list queries and one cursor signing key. A configured database
// password supplies shared secret material across API instances. Installations
// without a database password use a random per-process key.
func QueryEngine(pool *pgxpool.Pool) *query.Engine {
	if pool == nil { // doc generation constructs wiring without a database.
		return query.NewEphemeralEngine()
	}
	queryEngineOnce.Do(func() {
		if pool.Config().ConnConfig.Password == "" {
			queryEngine = query.NewEphemeralEngine()
		} else {
			queryEngine = query.NewEngine([]byte(pool.Config().ConnString()))
		}
	})
	return queryEngine
}
