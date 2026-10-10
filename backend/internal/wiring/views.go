package wiring

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	endpointsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	orgapp "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	servicedeskpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/public"
	tasksapp "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/views"
)

// ViewResources declares the resources Saved Views can be created over (ADR-0033, F13 Q-B): the catalog key, the
// ADR-0032 module that owns it, the default sidebar group, the permissions of which at least one is needed to read
// the resource, and the module's own query endpoints. The permissions mirror the route middleware of those
// endpoints (the module still enforces them on every call). Adding a resource to Saved Views means adding its
// catalog and one entry here.
func ViewResources() ([]views.Resource, map[string]views.Route) {
	tickets, tasks, devices := servicedeskapp.TicketCatalog().Key(), tasksapp.TaskCatalog().Key(), endpointsapp.DeviceCatalog().Key()
	users := orgapp.UserCatalog().Key()
	resources := []views.Resource{
		// Everyone sees their own tickets, so any signed-in User may use Ticket Views.
		{Key: tickets, Module: "servicedesk", Group: "tickets"},
		{Key: tasks, Module: "tasks", Group: "tasks", Use: []string{"tasks.view", "tasks.work", "tasks.manage"}},
		{Key: devices, Module: "endpoints", Group: "endpoints", Use: []string{"endpoints.view", "endpoints.manage"}},
		// Users: POST /users/query requires organization.view; HR-adjacent fields are further gated by view_details.
		{Key: users, Module: "organization", Group: "organization", Use: []string{"organization.view"}},
	}
	routes := map[string]views.Route{
		tickets: {FieldsPath: "/api/v1/tickets/fields", QueryPath: "/api/v1/tickets/query"},
		tasks:   {FieldsPath: "/api/v1/tasks/fields", QueryPath: "/api/v1/tasks/query"},
		devices: {FieldsPath: "/api/v1/devices/fields", QueryPath: "/api/v1/devices/query"},
		users:   {FieldsPath: "/api/v1/users/fields", QueryPath: "/api/v1/users/query"},
	}
	return resources, routes
}

// viewDirectory adapts Organization's public contracts to the Views Directory port.
type viewDirectory struct {
	*orgpublic.WorkDirectory
	groups *orgpublic.AuthorizationSubjects
}

func (d viewDirectory) GroupIDsOfUser(ctx context.Context, userID string) ([]string, error) {
	return d.groups.GroupIDsOfUser(ctx, userID)
}

// Views builds the Saved Views service. api is the API router below the module gate: queries of a View run through
// the owning module's own query endpoints, and the views platform checks the module switch itself.
func Views(pool *pgxpool.Pool, api http.Handler, gate views.ModuleGate) (*views.Service, error) {
	org := orgrepository.New(pool)
	resources, routes := ViewResources()
	svc, err := views.NewService(pool, viewDirectory{WorkDirectory: orgpublic.NewWorkDirectory(org), groups: orgpublic.NewAuthorizationSubjects(org)},
		views.NewHTTPRunner(api, routes), gate, resources)
	if err != nil {
		return nil, err
	}
	// Built-in System Views of the sidebar (My open tickets, Unassigned, one per Queue the caller views).
	return svc.WithSystemProviders(servicedeskpublic.NewSystemViews(ServiceDesk(pool))), nil
}
