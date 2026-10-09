package public

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/repository"
)

// Hostnames lets other modules search the assets of devices by hostname. It authorizes nothing: the caller must
// hold endpoints.view before it matches device names.
type Hostnames struct{ repo *repository.Repository }

func NewHostnames(pool *pgxpool.Pool) *Hostnames { return &Hostnames{repo: repository.New(pool)} }

// AssetIDsByHostname returns the ids of assets linked to a live device whose name matches the text.
func (h *Hostnames) AssetIDsByHostname(ctx context.Context, text string, limit int) ([]string, error) {
	return h.repo.AssetIDsByHostname(ctx, text, limit)
}
