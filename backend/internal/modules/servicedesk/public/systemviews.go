package public

import (
	"context"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/views"
)

// SystemViews offers the built-in Ticket Views of the sidebar (ADR-0033): "My open tickets", "Unassigned" and one
// entry per Queue the caller views. Callers who see only their own Tickets get none. Each entry is an ordinary
// filter over the tickets catalog, run through the module's own query endpoint as the viewer, so the Queue scope
// and the per-row disclosure rules apply exactly as for a direct query.
type SystemViews struct{ svc *application.Service }

// NewSystemViews builds the provider over the Service Desk service.
func NewSystemViews(svc *application.Service) *SystemViews { return &SystemViews{svc: svc} }

var openStatuses = []string{application.StatusNew, application.StatusOpen, application.StatusInProgress, application.StatusWaiting}

func and(nodes ...query.Node) *query.Filter {
	return &query.Filter{V: 1, Root: &query.Node{Type: "group", Logic: "and", Children: nodes},
		Sort: []query.SortSpec{{Field: "priority", Dir: "asc"}, {Field: "created_at", Dir: "desc"}}}
}

// SystemViews implements views.SystemProvider.
func (v *SystemViews) SystemViews(ctx context.Context, c views.Caller) ([]views.SystemView, error) {
	p := application.Principal{UserID: c.UserID, View: c.Has("tickets.view"), Manage: c.Has("tickets.manage"), QueuesManage: c.Has(application.PermQueuesManage)}
	scope, err := v.svc.SidebarScope(ctx, p)
	if err != nil {
		return nil, err
	}
	if !scope.AnyView {
		return nil, nil
	}
	open := query.Cond("status", query.OpIn, openStatuses)
	out := []views.SystemView{
		{Key: "system:tickets:my-open", Resource: "tickets", Group: "tickets", NameKey: "views.system.my_open_tickets", Position: 0, ScopeKey: scope.Key,
			Definition: views.Definition{Filter: and(open, query.Cond("assignee", query.OpIsMe, nil))}},
		{Key: "system:tickets:unassigned", Resource: "tickets", Group: "tickets", NameKey: "views.system.unassigned_tickets", Position: 1, ScopeKey: scope.Key,
			Definition: views.Definition{Filter: and(open, query.Cond("assignee", query.OpIsEmpty, nil))}},
	}
	// "At risk" (F17 S-E) exists only while the SLA switch is on. It lists open Tickets of the Queues the caller views
	// whose warning is reached or whose target is missed, the most urgent resolution time first. Like every System
	// View it is an ordinary filter run as the viewer; with the switch off the fields would be unreadable.
	next := 2
	if on, err := v.svc.SLAEnabled(ctx); err != nil {
		return nil, err
	} else if on {
		f := and(open, query.Cond("sla_state", query.OpIn, []string{"at_risk", "breached"}))
		f.Sort = []query.SortSpec{{Field: "sla_resolution_due_at", Dir: "asc", Nulls: "last"}, {Field: "created_at", Dir: "desc"}}
		out = append(out, views.SystemView{Key: "system:tickets:at-risk", Resource: "tickets", Group: "tickets", NameKey: "views.system.at_risk_tickets",
			Position: next, ScopeKey: scope.Key, Definition: views.Definition{Filter: f}})
		next++
	}
	for i, q := range scope.Queues {
		out = append(out, views.SystemView{Key: "system:tickets:queue:" + q.ID, Handle: "queue:" + q.ID, Resource: "tickets", Group: "tickets",
			Name: q.Name, Ref: q.ID, Position: next + i, ScopeKey: scope.Key,
			Definition: views.Definition{Filter: and(open, query.Cond("queue", query.OpEquals, q.ID))}})
	}
	return out, nil
}
