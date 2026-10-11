package wiring

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	presenceapp "github.com/MagicalWig34653/turaco/backend/internal/modules/presence/application"
	presencepublic "github.com/MagicalWig34653/turaco/backend/internal/modules/presence/public"
	presencerepository "github.com/MagicalWig34653/turaco/backend/internal/modules/presence/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/modules"
)

// ChangePresence applies the module switch before reading the public Presence contract.
type ChangePresence struct {
	Presence *presencepublic.Presence
	Modules  *modules.Service
}

func (p ChangePresence) AvailabilityWindow(ctx context.Context, viewer presencepublic.Viewer, userID string, from, to time.Time, need presencepublic.Need) (presencepublic.WindowResult, error) {
	if p.Modules != nil {
		on, err := p.Modules.Enabled(ctx, "presence")
		if err != nil || !on {
			return presencepublic.WindowResult{Disabled: true}, nil
		}
	}
	return p.Presence.AvailabilityWindow(ctx, viewer, userID, from, to, need)
}

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
