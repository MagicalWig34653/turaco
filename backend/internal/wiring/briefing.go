package wiring

import (
	"context"

	approvalsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/application"
	approvalspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/public"
	approvalsrepo "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/repository"
	briefingapp "github.com/MagicalWig34653/turaco/backend/internal/modules/briefing/application"
	endpointspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/public"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepo "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	planningpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/planning/public"
	securitypublic "github.com/MagicalWig34653/turaco/backend/internal/modules/security/public"
	deskpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/modules"
	"github.com/jackc/pgx/v5/pgxpool"
)

// incidentAnnouncements reads the public Major Incident status unless Service Desk is switched off (ADR-0032); a module
// whose state cannot be read counts as off.
type incidentAnnouncements struct {
	desk *deskpublic.Briefing
	mods *modules.Service
}

func (i incidentAnnouncements) PublicOpenIncidents(ctx context.Context) ([]deskpublic.PublicIncident, error) {
	if i.mods != nil {
		if ok, err := i.mods.Enabled(ctx, "servicedesk"); err != nil || !ok {
			return nil, err
		}
	}
	return i.desk.PublicOpenIncidents(ctx)
}

// BriefingAnnouncements builds the employee-facing announcements service.
func BriefingAnnouncements(pool *pgxpool.Pool, manual *briefingapp.Service, mods *modules.Service) *briefingapp.Service {
	return manual.WithIncidents(incidentAnnouncements{desk: deskpublic.NewBriefing(pool), mods: mods})
}

func BriefingFeed(pool *pgxpool.Pool, manual *briefingapp.Service, mods *modules.Service) *briefingapp.FeedService {
	dir := orgpublic.NewWorkDirectory(orgrepo.New(pool))
	approvals := approvalspublic.New(approvalsapp.NewService(approvalsrepo.New(pool), dir, nil))
	feed := briefingapp.NewFeedService(manual, briefingapp.FeedSources{
		Security: securitypublic.NewAdvisories(Security(pool)), Planning: planningpublic.New(Planning(pool)),
		Desk: deskpublic.NewBriefing(pool), Endpoints: endpointspublic.NewHealth(pool), Deployments: endpointspublic.NewHealth(pool), Directory: orgpublic.NewSyncHealth(orgrepo.New(pool)), Approvals: approvals,
	})
	if mods != nil {
		feed.WithSourceFilter(briefingSourceFilter(mods))
	}
	return feed
}

// briefingSourceFilter drops the sources of switched-off modules (ADR-0032). A module whose state cannot be read is
// treated as off: the briefing then shows less, never data of a module an administrator switched off.
func briefingSourceFilter(mods *modules.Service) func(context.Context, briefingapp.FeedSources) briefingapp.FeedSources {
	return func(ctx context.Context, s briefingapp.FeedSources) briefingapp.FeedSources {
		on := func(key string) bool {
			ok, err := mods.Enabled(ctx, key)
			return err == nil && ok
		}
		if !on("security") {
			s.Security = nil
		}
		if !on("planning") {
			s.Planning = nil
		}
		if !on("servicedesk") {
			s.Desk = nil
		}
		if !on("endpoints") {
			s.Endpoints, s.Deployments = nil, nil
		}
		return s
	}
}
