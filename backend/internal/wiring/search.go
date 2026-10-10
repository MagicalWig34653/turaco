package wiring

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/softwaremgmt"
	assetspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/public"
	changespublic "github.com/MagicalWig34653/turaco/backend/internal/modules/changes/public"
	endpointspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/public"
	knowledgepublic "github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/public"
	orgapp "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	requestspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/requests/public"
	servicedeskpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/modules"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/search"
)

// Search builds the platform search (F14 section 5) with the Searchers the modules contribute through their public
// contracts. Order is the order of the result groups. A disabled module is skipped by the module switch.
func Search(pool *pgxpool.Pool, mods *modules.Service) *search.Service {
	svc := search.New()
	if mods != nil {
		svc.Enabled = func(ctx context.Context, module string) bool {
			on, err := mods.Enabled(ctx, module)
			return err == nil && on
		}
	}
	svc.Register(
		servicedeskpublic.NewTicketSearcher(ServiceDesk(pool)),
		servicedeskpublic.NewProblemSearcher(Problems(pool)),
		servicedeskpublic.NewMajorIncidentSearcher(MajorIncidents(pool)),
		changespublic.NewChangeSearcher(Changes(pool)),
		requestspublic.NewRequestSearcher(Requests(pool)),
		knowledgepublic.NewArticleSearcher(Knowledge(pool)),
		assetspublic.NewAssetSearcher(Assets(pool)),
		endpointspublic.NewDeviceSearcher(Endpoints(pool, intune.NotConfigured{}, false, softwaremgmt.NotConfigured{}, false)),
		orgpublic.NewUserSearcher(orgapp.NewQueries(orgrepository.New(pool), QueryEngine(pool))),
	)
	return svc
}
