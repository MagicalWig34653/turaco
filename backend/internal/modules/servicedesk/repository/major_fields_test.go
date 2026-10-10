package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
)

type majorDir struct {
	users, locations map[string]string
}

func (d majorDir) pick(m map[string]string, ids []string) map[string]string {
	out := map[string]string{}
	for _, id := range ids {
		if n, ok := m[id]; ok {
			out[id] = n
		}
	}
	return out
}

func (d majorDir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for id := range d.pick(d.users, ids) {
		out[id] = true
	}
	return out, nil
}
func (d majorDir) UserNames(_ context.Context, ids []string) (map[string]string, error) {
	return d.pick(d.users, ids), nil
}
func (d majorDir) ActiveLocations(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for id := range d.pick(d.locations, ids) {
		out[id] = true
	}
	return out, nil
}
func (d majorDir) LocationNames(_ context.Context, ids []string) (map[string]string, error) {
	return d.pick(d.locations, ids), nil
}

func TestMajorIncidentExerciseOwnerDueLocationsAndHiddenTickets(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var loc1, loc2 string
	for _, dst := range []*string{&loc1, &loc2} {
		if err := e.pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	dir := majorDir{users: map[string]string{e.agent: "Ada Agent"}, locations: map[string]string{loc1: "Main Campus", loc2: "North Wing"}}
	svc := application.NewMajorService(repository.New(e.pool)).WithTicketAccess(e.ticketAccess()).WithDirectory(dir)
	t.Cleanup(func() {
		_, _ = e.pool.Exec(ctx, `DELETE FROM servicedesk.tickets WHERE reporter_user_id = ANY($1::uuid[])`, []string{e.alice, e.bob})
		_, _ = e.pool.Exec(ctx, `DELETE FROM servicedesk.major_incidents WHERE declared_by = $1::uuid`, e.agent)
	})
	var inv *application.InvalidInputError
	var tr *application.InvalidTransitionError

	live, err := svc.Declare(ctx, e.c(e.agent), true, "Mail down", "Mail is down", false)
	if err != nil {
		t.Fatal(err)
	}
	drill, err := svc.Declare(ctx, e.c(e.agent), true, "Fire drill", "Exercise only", true)
	if err != nil || !drill.IsExercise {
		t.Fatalf("declare exercise = %+v %v", drill, err)
	}

	// Exercises stay out of the banner list and the briefing/announcement reads, but staff can list them.
	banner, err := svc.List(ctx, e.bob, true, false, application.Page{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	all, _ := svc.List(ctx, e.bob, true, true, application.Page{Limit: 200})
	has := func(items []application.MajorIncident, id string) bool {
		for _, m := range items {
			if m.ID == id {
				return true
			}
		}
		return false
	}
	if has(banner.Items, drill.ID) || !has(banner.Items, live.ID) || !has(all.Items, drill.ID) {
		t.Errorf("exercise filter wrong: banner=%d all=%d", len(banner.Items), len(all.Items))
	}
	b := public.NewBriefing(e.pool)
	open, err := b.OpenMajorIncidents(ctx, public.ReadScope{IncludeDetails: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range open {
		if m.ID == drill.ID {
			t.Error("an exercise reached the briefing counts")
		}
	}
	pub, err := b.PublicOpenIncidents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seenLive := false
	for _, m := range pub {
		if m.ID == drill.ID {
			t.Error("an exercise reached the public announcements")
		}
		seenLive = seenLive || m.ID == live.ID
	}
	if !seenLive {
		t.Error("the live incident must be announced")
	}

	// Owner: explicit, validated, versioned, audited, staff-only.
	if _, err := svc.SetOwner(ctx, e.c(e.agent), false, live.ID, nil, &e.agent); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("owner without permission: %v", err)
	}
	if _, err := svc.SetOwner(ctx, e.c(e.agent), true, live.ID, nil, &e.alice); !errors.As(err, &inv) {
		t.Errorf("owner must be an active user: %v", err)
	}
	stale := live.Version + 5
	if _, err := svc.SetOwner(ctx, e.c(e.agent), true, live.ID, &stale, &e.agent); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale owner change: %v", err)
	}
	v := live.Version
	m, err := svc.SetOwner(ctx, e.c(e.agent), true, live.ID, &v, &e.agent)
	if err != nil || m.OwnerUserID == nil || *m.OwnerUserID != e.agent || m.Version != v+1 {
		t.Fatalf("set owner = %+v %v", m, err)
	}
	if again, _ := svc.SetOwner(ctx, e.c(e.agent), true, live.ID, nil, &e.agent); again.Version != m.Version {
		t.Error("setting the same owner again must not bump the version")
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'servicedesk.major_incident.owner_set'`, e.corr) != 1 {
		t.Error("the owner change is audited once")
	}

	// Next update due: must be in the future; cleared on resolve.
	if _, err := svc.SetNextUpdate(ctx, e.c(e.agent), true, live.ID, nil, ptr(time.Now().Add(-time.Hour))); !errors.As(err, &inv) {
		t.Errorf("past due time: %v", err)
	}
	due := time.Now().Add(2 * time.Hour)
	m, err = svc.SetNextUpdate(ctx, e.c(e.agent), true, live.ID, nil, &due)
	if err != nil || m.NextUpdateDue == nil {
		t.Fatalf("set due = %+v %v", m, err)
	}

	// Locations: replace set through the Organization contract; unknown ones are refused.
	if _, err := svc.SetLocations(ctx, e.c(e.agent), true, live.ID, nil, []string{e.alice}); !errors.As(err, &inv) {
		t.Errorf("unknown location: %v", err)
	}
	if _, err := svc.SetLocations(ctx, e.c(e.agent), true, live.ID, nil, []string{loc2, loc1, loc1}); err != nil {
		t.Fatal(err)
	}

	// Hidden ticket count: one linked ticket the employee cannot see.
	tk := e.raise() // reported by alice
	if err := svc.LinkTicket(ctx, e.c(e.agent), true, live.ID, tk.ID); err != nil {
		t.Fatal(err)
	}
	staff, err := svc.Get(ctx, e.agent, true, live.ID)
	if err != nil {
		t.Fatal(err)
	}
	if staff.Owner == nil || staff.Owner.Name != "Ada Agent" || len(staff.Locations) != 2 || staff.Locations[0].Name == "" || staff.Incident.NextUpdateDue == nil {
		t.Errorf("staff detail = %+v", staff)
	}
	if staff.HiddenTickets != 1-len(staff.Tickets) {
		t.Errorf("hidden=%d visible=%d", staff.HiddenTickets, len(staff.Tickets))
	}
	emp, err := svc.Get(ctx, e.bob, false, live.ID)
	if err != nil {
		t.Fatal(err)
	}
	if emp.Owner != nil || emp.HiddenTickets != 0 || len(emp.Locations) != 2 {
		t.Errorf("employee must not see owner or hidden counts: %+v", emp)
	}
	if emp.Incident.Tickets != 0 {
		t.Errorf("bob cannot see alice's ticket: %d", emp.Incident.Tickets)
	}

	// Resolve clears the due time; closed incidents are immutable.
	if _, err := svc.Transition(ctx, e.c(e.agent), true, live.ID, nil, application.MOResolve, "Fixed"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetNextUpdate(ctx, e.c(e.agent), true, live.ID, nil, &due); !errors.As(err, &tr) {
		t.Errorf("next update on a resolved incident: %v", err)
	}
	r, _ := svc.Get(ctx, e.agent, true, live.ID)
	if r.Incident.NextUpdateDue != nil {
		t.Error("resolving clears the due time")
	}
	if _, err := svc.Transition(ctx, e.c(e.agent), true, live.ID, nil, application.MOClose, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetOwner(ctx, e.c(e.agent), true, live.ID, nil, nil); !errors.As(err, &tr) {
		t.Errorf("owner of a closed incident: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }
