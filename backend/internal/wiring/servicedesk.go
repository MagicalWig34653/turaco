package wiring

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	assetsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/application"
	assetspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/public"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	servicedeskrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
)

type deviceAdapter struct{ a *assetspublic.Assets }

func (d deviceAdapter) Snapshot(ctx context.Context, assetID, holder string) (map[string]any, error) {
	s, err := d.a.Snapshot(ctx, assetID, holder)
	if errors.Is(err, assetsapp.ErrNotFound) {
		return nil, servicedeskapp.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"reference": s.Reference, "product": s.Product, "serialNumber": s.SerialNumber, "assetTag": s.AssetTag, "status": s.Status}, nil
}

// MajorIncidents builds the Major Incident service.
func MajorIncidents(pool *pgxpool.Pool) *servicedeskapp.MajorService {
	return servicedeskapp.NewMajorService(servicedeskrepository.New(pool))
}

// Problems builds the Problem service.
func Problems(pool *pgxpool.Pool) *servicedeskapp.ProblemService {
	return servicedeskapp.NewProblemService(servicedeskrepository.New(pool), orgpublic.NewWorkDirectory(orgrepository.New(pool)))
}

// ServiceDesk builds the ticket service over the other modules' public contracts.
func ServiceDesk(pool *pgxpool.Pool) *servicedeskapp.Service {
	dir := orgpublic.NewWorkDirectory(orgrepository.New(pool))
	return servicedeskapp.NewService(servicedeskrepository.New(pool), dir, deviceAdapter{assetspublic.New(Assets(pool))})
}
