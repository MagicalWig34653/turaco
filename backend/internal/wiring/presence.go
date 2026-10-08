package wiring

import (
	"github.com/jackc/pgx/v5/pgxpool"

	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	presenceapp "github.com/MagicalWig34653/turaco/backend/internal/modules/presence/application"
	presencerepository "github.com/MagicalWig34653/turaco/backend/internal/modules/presence/repository"
)

// Presence builds the Workforce Presence service over Organization's work directory (ADR-0028).
func Presence(pool *pgxpool.Pool, cfg presenceapp.Config) *presenceapp.Service {
	return presenceapp.NewService(presencerepository.New(pool), orgpublic.NewWorkDirectory(orgrepository.New(pool)), cfg)
}
