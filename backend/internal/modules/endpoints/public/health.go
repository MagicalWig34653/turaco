package public

import (
	"context"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/repository"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SyncStatus = repository.ProviderSyncStatus
type Health struct{ repo *repository.Repository }

func NewHealth(pool *pgxpool.Pool) *Health { return &Health{repo: repository.New(pool)} }

// SyncHealth returns at most 21 provider completion records. The schema has no last-error field.
func (h *Health) SyncHealth(ctx context.Context) ([]SyncStatus, error) { return h.repo.SyncHealth(ctx) }

// OpenProviderErrors returns a count without device details.
func (h *Health) OpenProviderErrors(ctx context.Context) (int, error) {
	return h.repo.OpenProviderErrors(ctx)
}
