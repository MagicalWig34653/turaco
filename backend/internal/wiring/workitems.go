package wiring

import (
	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	servicedeskpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/public"
	tasksapp "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	taskspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/workitems"
)

// WorkItems builds My Work (ADR-0033 V8): the sources that contribute work for the signed-in User, in display order.
// A source runs only while its module is on (ADR-0032). Adding a module to My Work means one source in the module's
// public package and one line here.
func WorkItems(tasks *tasksapp.Service, tickets *servicedeskapp.Service, gate workitems.ModuleGate) (*workitems.Service, error) {
	return workitems.New(gate,
		servicedeskpublic.NewAssignedWorkItems(tickets),
		taskspublic.NewWorkItems(tasks),
		servicedeskpublic.NewTeamWorkItems(tickets),
	)
}
