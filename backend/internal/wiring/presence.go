package wiring

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	presenceapp "github.com/MagicalWig34653/turaco/backend/internal/modules/presence/application"
	presencerepository "github.com/MagicalWig34653/turaco/backend/internal/modules/presence/repository"
)

// Presence builds the Workforce Presence service over Organization's work directory (ADR-0028).
func Presence(pool *pgxpool.Pool, cfg presenceapp.Config) *presenceapp.Service {
	return presenceapp.NewService(presencerepository.New(pool), presenceDirectory{orgpublic.NewWorkDirectory(orgrepository.New(pool))}, cfg)
}

// presenceDirectory adapts Organization's work directory to Presence's Directory port.
type presenceDirectory struct{ *orgpublic.WorkDirectory }

func (d presenceDirectory) MembershipIntervals(ctx context.Context, teamID string, from, to time.Time) ([]presenceapp.MembershipInterval, error) {
	in, err := d.WorkDirectory.MembershipIntervals(ctx, teamID, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]presenceapp.MembershipInterval, len(in))
	for i, m := range in {
		out[i] = presenceapp.MembershipInterval{UserID: m.UserID, From: m.From, Until: m.Until}
	}
	return out, nil
}
