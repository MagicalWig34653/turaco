package wiring

import (
	"github.com/jackc/pgx/v5/pgxpool"

	knowledgeapp "github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/application"
	knowledgerepository "github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/repository"
)

// Knowledge builds the Knowledge service.
func Knowledge(pool *pgxpool.Pool) *knowledgeapp.Service {
	return knowledgeapp.NewService(knowledgerepository.New(pool))
}
