package wiring

import (
	"github.com/jackc/pgx/v5/pgxpool"

	approvalsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/application"
	approvalspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/public"
	approvalsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/repository"
	changespublic "github.com/MagicalWig34653/turaco/backend/internal/modules/changes/public"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	planningapp "github.com/MagicalWig34653/turaco/backend/internal/modules/planning/application"
	planningpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/planning/public"
	planningrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/planning/repository"
	procurementpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/procurement/public"
	servicespublic "github.com/MagicalWig34653/turaco/backend/internal/modules/services/public"
)

// Planning builds the Planning service over the other modules' public contracts.
func Planning(pool *pgxpool.Pool) *planningapp.Service {
	dir := orgpublic.NewWorkDirectory(orgrepository.New(pool))
	approvals := planningpublic.NewApprovals(approvalspublic.New(approvalsapp.NewService(approvalsrepository.New(pool), dir, nil)))
	return planningapp.NewService(planningrepository.New(pool), Relationships(), dir,
		planningpublic.NewChangesAdapter(changespublic.NewChanges(Changes(pool))),
		planningpublic.NewTasks(taskspublicCreator(pool, dir, "initiative")),
		planningpublic.NewProcurement(procurementpublic.NewRequests(Procurement(pool))),
		planningpublic.NewServices(servicespublic.New(Services(pool))),
		approvals)
}

// PlanningNotifications builds the InitiativeStatusChanged notification consumer.
func PlanningNotifications(pool *pgxpool.Pool, notifier planningapp.Notifier) *planningapp.Notifications {
	return planningapp.NewNotifications(planningrepository.New(pool), orgpublic.NewWorkDirectory(orgrepository.New(pool)), notifier)
}
