package wiring

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/autotask"
	assetsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/application"
	assetspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/public"
	changespublic "github.com/MagicalWig34653/turaco/backend/internal/modules/changes/public"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	servicedeskpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/public"
	servicedeskrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/modules"
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
	org := orgrepository.New(pool)
	return servicedeskapp.NewMajorService(servicedeskrepository.New(pool)).WithTicketAccess(ServiceDesk(pool)).WithDirectory(orgpublic.NewWorkDirectory(org)).
		WithOwnerPermissions(roles.NewEvaluator(pool, orgpublic.NewAuthorizationSubjects(org)))
}

// Problems builds the Problem service.
func Problems(pool *pgxpool.Pool) *servicedeskapp.ProblemService {
	return servicedeskapp.NewProblemService(servicedeskrepository.New(pool), orgpublic.NewWorkDirectory(orgrepository.New(pool))).WithTicketAccess(ServiceDesk(pool))
}

// queueMemberships answers the Queue grant questions with Organization (Teams) and the permission evaluator
// (roles, and the global permissions of an assignee).
type queueMemberships struct {
	teams *orgpublic.WorkDirectory
	eval  *roles.Evaluator
}

// external reports whether the User is an external account: its Teams and roles never open Queues (review rule R2),
// also when staff assign a ticket to it.
func (m queueMemberships) external(ctx context.Context, userID string) (bool, error) {
	ext, err := m.teams.ActiveExternals(ctx, []string{userID})
	return ext[userID], err
}

func (m queueMemberships) TeamIDs(ctx context.Context, userID string) ([]string, error) {
	if ext, err := m.external(ctx, userID); err != nil || ext {
		return nil, err
	}
	return m.teams.CurrentTeamIDs(ctx, userID)
}

func (m queueMemberships) RoleIDs(ctx context.Context, userID string) ([]string, error) {
	if ext, err := m.external(ctx, userID); err != nil || ext {
		return nil, err
	}
	return m.eval.RoleIDs(ctx, userID)
}

func (m queueMemberships) Permissions(ctx context.Context, userID string) (map[string]struct{}, error) {
	return m.eval.Permissions(ctx, userID)
}

// externalAccounts adapts the Organization work directory to the Service Desk participant directory.
type externalAccounts struct{ dir *orgpublic.WorkDirectory }

func (e externalAccounts) ActiveExternals(ctx context.Context, ids []string) (map[string]bool, error) {
	return e.dir.ActiveExternals(ctx, ids)
}

func (e externalAccounts) SearchExternals(ctx context.Context, text string, limit int) ([]servicedeskapp.ExternalAccount, error) {
	list, err := e.dir.SearchExternals(ctx, text, limit)
	if err != nil {
		return nil, err
	}
	out := make([]servicedeskapp.ExternalAccount, len(list))
	for i, a := range list {
		out[i] = servicedeskapp.ExternalAccount{UserID: a.UserID, DisplayName: a.DisplayName, PartyName: a.PartyName}
	}
	return out, nil
}

// ServiceDesk builds the ticket service over the other modules' public contracts.
func ServiceDesk(pool *pgxpool.Pool) *servicedeskapp.Service {
	org := orgrepository.New(pool)
	dir := orgpublic.NewWorkDirectory(org)
	members := queueMemberships{teams: dir, eval: roles.NewEvaluator(pool, orgpublic.NewAuthorizationSubjects(org))}
	moduleSwitch := modules.NewService(pool, modules.Options{})
	return servicedeskapp.NewService(servicedeskrepository.New(pool), dir, deviceAdapter{assetspublic.New(Assets(pool))}).
		WithSLA(func(ctx context.Context) (bool, error) { return moduleSwitch.Enabled(ctx, "sla") }).
		WithQueryEngine(QueryEngine(pool)).WithMemberships(members).WithChanges(changespublic.NewChanges(Changes(pool)), Relationships(), pool).
		WithExternalAccounts(servicedeskrepository.New(pool), externalAccounts{dir}).
		WithCallbackTasks(servicedeskpublic.NewCallbackTasks(taskspublicCreator(pool, dir, servicedeskapp.TaskContextType)))
}

// gatewayAdapter adapts the Autotask adapter to the Service Desk port.
type gatewayAdapter struct{ g autotask.Gateway }

func (a gatewayAdapter) Push(ctx context.Context, t servicedeskapp.TicketPayload, externalID string) (string, error) {
	id, err := a.g.Upsert(ctx, autotask.Ticket{Reference: t.Reference, Title: t.Title, Description: t.Description, Status: t.Status,
		Priority: t.Priority, Resolution: t.Resolution}, externalID)
	var ae *autotask.Error
	if errors.As(err, &ae) {
		return "", &servicedeskapp.GatewayError{Message: ae.Message, Permanent: ae.Permanent}
	}
	return id, err
}

// ExternalSync builds the Autotask synchronization of tickets. enabled switches the outbound
// consumers and the inbound handler on; the gateway is the placeholder until a REST client exists.
func ExternalSync(pool *pgxpool.Pool, gateway autotask.Gateway, enabled bool) *servicedeskapp.ExternalSync {
	return servicedeskapp.NewExternalSync(ServiceDesk(pool), servicedeskrepository.New(pool), pool, gatewayAdapter{gateway}, enabled)
}
