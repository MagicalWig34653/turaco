package wiring

import (
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
	"github.com/jackc/pgx/v5/pgxpool"
)

func BriefingFeed(pool *pgxpool.Pool, manual *briefingapp.Service) *briefingapp.FeedService {
	dir := orgpublic.NewWorkDirectory(orgrepo.New(pool))
	approvals := approvalspublic.New(approvalsapp.NewService(approvalsrepo.New(pool), dir, nil))
	return briefingapp.NewFeedService(manual, briefingapp.FeedSources{
		Security: securitypublic.NewAdvisories(Security(pool)), Planning: planningpublic.New(Planning(pool)),
		Desk: deskpublic.NewBriefing(pool), Endpoints: endpointspublic.NewHealth(pool), Directory: orgpublic.NewSyncHealth(orgrepo.New(pool)), Approvals: approvals,
	})
}
