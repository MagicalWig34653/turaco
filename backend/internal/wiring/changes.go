package wiring

import (
	"github.com/jackc/pgx/v5/pgxpool"

	approvalsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/application"
	approvalspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/public"
	approvalsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/repository"
	assetspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/public"
	changesapp "github.com/MagicalWig34653/turaco/backend/internal/modules/changes/application"
	changespublic "github.com/MagicalWig34653/turaco/backend/internal/modules/changes/public"
	changesrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/changes/repository"
	infrapublic "github.com/MagicalWig34653/turaco/backend/internal/modules/infrastructure/public"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	servicespublic "github.com/MagicalWig34653/turaco/backend/internal/modules/services/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// Changes builds the Changes service over the other modules' public contracts.
func Changes(pool *pgxpool.Pool) *changesapp.Service {
	dir := orgpublic.NewWorkDirectory(orgrepository.New(pool))
	approvals := changespublic.NewApprovals(approvalspublic.New(approvalsapp.NewService(approvalsrepository.New(pool), dir, nil)))
	return changesapp.NewService(changesrepository.New(pool), Relationships(), dir,
		changespublic.NewServices(servicespublic.New(Services(pool))),
		changespublic.NewInfrastructure(infrapublic.New(Infrastructure(pool))),
		changespublic.NewAssets(assetspublic.New(Assets(pool))),
		approvals, changespublic.NewTasks(taskspublicCreator(pool, dir)))
}

// ChangeNotifications builds the notification consumers and the reminder job handler of Changes.
func ChangeNotifications(pool *pgxpool.Pool, notifier changesapp.Notifier) *changesapp.Notifications {
	dir := orgpublic.NewWorkDirectory(orgrepository.New(pool))
	return changesapp.NewNotifications(changesrepository.New(pool), Relationships(), dir,
		changespublic.NewServices(servicespublic.New(Services(pool))), notifier)
}

var _ changesapp.Notifier = (*notifications.Service)(nil)
