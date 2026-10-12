package wiring

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	servicedeskpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/public"
	servicedeskrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
)

// SLASweeper builds the job that records SLA warnings and breaches. enabled reports the sla module switch. The
// optional escalation Task goes through the Tasks public contract.
func SLASweeper(pool *pgxpool.Pool, enabled func(context.Context) (bool, error)) *servicedeskapp.SLASweeper {
	dir := orgpublic.NewWorkDirectory(orgrepository.New(pool))
	return servicedeskapp.NewSLASweeper(servicedeskrepository.New(pool), enabled).
		WithEscalationTasks(servicedeskpublic.NewEscalationTasks(taskspublicCreator(pool, dir, servicedeskapp.TaskContextType)))
}

// slaRecipients resolves the recipients of SLA notifications: default Team members (Organization) who can still
// view the Queue (the Service Desk access rules).
type slaRecipients struct {
	dir *orgpublic.WorkDirectory
	svc *servicedeskapp.Service
}

func (r slaRecipients) TeamMemberIDs(ctx context.Context, teamID string) ([]string, error) {
	return r.dir.CurrentMemberIDs(ctx, teamID)
}

func (r slaRecipients) MaySeeQueue(ctx context.Context, tx pgx.Tx, userID, queueID string) (bool, error) {
	return r.svc.MaySeeQueue(ctx, tx, userID, queueID)
}

// SLARecipients is the recipient resolver of the SLA notification consumers.
func SLARecipients(pool *pgxpool.Pool) servicedeskapp.SLARecipients {
	return slaRecipients{dir: orgpublic.NewWorkDirectory(orgrepository.New(pool)), svc: ServiceDesk(pool)}
}
