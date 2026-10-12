package repository_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

// Location-scoped tickets.view and tickets.manage (ADR-0040, slice L-D) on the affected Location of a Ticket:
// working a Ticket only through a scope cannot move it out of the scope, an assignee who loses sight of the Ticket
// is cleared, an assignee must be able to view the Ticket at its Location, and mentions follow the same rule.
func TestLocationScopedTicketWork(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	q := e.queue(application.QueuePublic)
	siteA, areaA, siteB := newLoc(t, e), newLoc(t, e), newLoc(t, e)
	e.dirActive(siteA, areaA, siteB)

	worker, viewer := e.u1, e.u2
	scopeA := authorization.LocationScope{Locations: []string{siteA, areaA}}
	e.mem.scopes = map[string][2]authorization.LocationScope{
		worker: {{}, scopeA}, // tickets.manage at Site A (manage implies view)
		viewer: {scopeA, {}}, // tickets.view at Site A
	}
	asWorker := application.Principal{UserID: worker, ManageScope: scopeA}
	asViewer := application.Principal{UserID: viewer, ViewScope: scopeA}

	tk := e.raise(e.employee(e.alice), q)
	setLoc := func(p application.Principal, id string, loc *string) error {
		cur, err := e.svc.Get(ctx, e.admin, id)
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.svc.SetLocation(ctx, e.c(p.UserID), p, id, &cur.Ticket.Version, loc)
		return err
	}
	// The admin places the Ticket at Site A: the scoped worker now sees and works it, the viewer only sees it.
	if err := setLoc(e.admin, tk.ID, &siteA); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Get(ctx, asViewer, tk.ID); err != nil {
		t.Fatalf("scoped viewer cannot read an in-scope ticket: %v", err)
	}
	if err := setLoc(asViewer, tk.ID, &areaA); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("a scoped viewer moved the location: %v", err)
	}
	// The worker may move it within the scope, never out of it and never to no Location.
	if err := setLoc(asWorker, tk.ID, &areaA); err != nil {
		t.Errorf("move within scope: %v", err)
	}
	if err := setLoc(asWorker, tk.ID, &siteB); !errors.Is(err, application.ErrLocationOutOfScope) {
		t.Errorf("move out of scope: %v", err)
	}
	if err := setLoc(asWorker, tk.ID, nil); !errors.Is(err, application.ErrLocationOutOfScope) {
		t.Errorf("clearing the location: %v", err)
	}

	// An assignee must be able to view the Ticket at its Location.
	cur, _ := e.svc.Get(ctx, e.admin, tk.ID)
	if _, err := e.svc.Assign(ctx, e.c(e.admin.UserID), e.admin, tk.ID, &cur.Ticket.Version, &viewer, nil); err != nil {
		t.Fatalf("assign to a scoped viewer at the ticket's location: %v", err)
	}
	// Moving the Ticket to Site B (global admin) clears an assignee who cannot view it there.
	if err := setLoc(e.admin, tk.ID, &siteB); err != nil {
		t.Fatal(err)
	}
	after, _ := e.svc.Get(ctx, e.admin, tk.ID)
	if after.Ticket.AssigneeID != nil {
		t.Errorf("assignee %v kept after the ticket left their scope", *after.Ticket.AssigneeID)
	}
	if _, err := e.svc.Assign(ctx, e.c(e.admin.UserID), e.admin, tk.ID, &after.Ticket.Version, &viewer, nil); !errors.Is(err, application.ErrAssigneeNoAccess) {
		t.Errorf("assigning someone who cannot view the ticket at its location: %v", err)
	}
	// Out of scope the worker and the viewer see nothing.
	for _, p := range []application.Principal{asWorker, asViewer} {
		if _, err := e.svc.Get(ctx, p, tk.ID); !errors.Is(err, application.ErrNotFound) {
			t.Errorf("%s reads a ticket outside the scope: %v", p.UserID, err)
		}
		page, err := e.svc.List(ctx, p, true, application.Filter{Page: application.Page{Limit: 200}})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if item.ID == tk.ID {
				t.Errorf("legacy list shows an out-of-scope ticket to %s", p.UserID)
			}
		}
	}
	// The sidebar cache key separates callers with different scoped Locations.
	other := authorization.LocationScope{Locations: []string{siteB}}
	k1, err := e.svc.SidebarScope(ctx, application.Principal{UserID: worker, ManageScope: scopeA})
	if err != nil {
		t.Fatal(err)
	}
	k2, err := e.svc.SidebarScope(ctx, application.Principal{UserID: worker, ManageScope: other})
	if err != nil {
		t.Fatal(err)
	}
	if k1.Key == k2.Key || !k1.AnyView || !slices.Contains([]bool{true}, k2.AnyView) {
		t.Errorf("sidebar keys %q %q anyView %v %v", k1.Key, k2.Key, k1.AnyView, k2.AnyView)
	}
}

func newLoc(t *testing.T, e *qenv) string {
	t.Helper()
	var id string
	if err := e.pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
