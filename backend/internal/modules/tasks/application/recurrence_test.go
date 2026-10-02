package application

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// memDefs is an in-memory DefinitionStore with the same contract as PostgreSQL.
type memDefs struct {
	defs      map[string]Definition
	seq       int
	changes   []DefinitionChange
	generated []Definition // definitions a task was generated for, with the plan applied
	plans     []Generation
}

func newMemDefs() *memDefs { return &memDefs{defs: map[string]Definition{}} }

func (m *memDefs) Insert(_ context.Context, _ Caller, n NewDefinition) (Definition, error) {
	m.seq++
	next := n.NextRunAt
	d := Definition{
		ID: id(m.seq), Title: n.Title, Description: n.Description, Priority: n.Priority, AssignedUserID: n.AssignedUserID,
		AssignedTeamID: n.AssignedTeamID, DueAfterHours: n.DueAfterHours, Rule: n.Rule, Active: true, NextRunAt: &next,
		CreatedByUserID: n.CreatedBy, Version: 1,
	}
	m.defs[d.ID] = d
	return d, nil
}

func (m *memDefs) Get(_ context.Context, defID string) (Definition, error) {
	d, ok := m.defs[defID]
	if !ok {
		return Definition{}, ErrNotFound
	}
	return d, nil
}

func (m *memDefs) List(_ context.Context, _ Page) (Result[Definition], error) {
	var out []Definition
	for _, d := range m.defs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return Result[Definition]{Items: out}, nil
}

func (m *memDefs) Change(_ context.Context, _ Caller, defID string, decide func(Definition) (DefinitionChange, error)) (Definition, error) {
	cur, ok := m.defs[defID]
	if !ok {
		return Definition{}, ErrNotFound
	}
	ch, err := decide(cur)
	if err != nil {
		return Definition{}, err
	}
	if ch.NoChange {
		return cur, nil
	}
	ch.Next.Version = cur.Version + 1
	m.defs[defID] = ch.Next
	m.changes = append(m.changes, ch)
	return ch.Next, nil
}

func (m *memDefs) Delete(_ context.Context, _ Caller, defID string, expected *int) error {
	cur, ok := m.defs[defID]
	if !ok {
		return ErrNotFound
	}
	if expected != nil && *expected != cur.Version {
		return ErrVersionConflict
	}
	delete(m.defs, defID)
	return nil
}

func (m *memDefs) GenerateDue(_ context.Context, now time.Time, plan func(Definition) (Generation, error)) (bool, bool, error) {
	var due []Definition
	for _, d := range m.defs {
		if d.Active && d.NextRunAt != nil && !d.NextRunAt.After(now) {
			due = append(due, d)
		}
	}
	if len(due) == 0 {
		return false, false, nil
	}
	sort.Slice(due, func(i, j int) bool { return due[i].NextRunAt.Before(*due[j].NextRunAt) })
	d := due[0]
	g, err := plan(d)
	if err != nil {
		return true, false, err
	}
	m.generated = append(m.generated, d)
	m.plans = append(m.plans, g)
	next := g.NextRunAt
	d.NextRunAt = &next
	d.Version++
	m.defs[d.ID] = d
	return true, true, nil
}

var pRecurrence = Principal{UserID: manager, RecurrenceManage: true}

func newRecSvc(now time.Time) (*RecurrenceService, *memDefs, *fakeDir) {
	st := newMemDefs()
	dir := fakeDir{
		activeUsers: map[string]bool{manager: true, worker: true},
		activeTeams: map[string]bool{teamX: true},
	}
	return NewRecurrenceService(st, dir, func() time.Time { return now }), st, &dir
}

func daily() Rule {
	return Rule{Frequency: FreqDaily, Interval: 1, TimeOfDay: "09:00", Timezone: "UTC", StartsOn: "2026-01-01"}
}

var fixedNow = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

func TestEveryOperationNeedsTheRecurrencePermission(t *testing.T) {
	s, st, _ := newRecSvc(fixedNow)
	ctx := context.Background()
	c := callerOf(pManager)
	for _, p := range []Principal{pManager, pWorker, pViewer, {UserID: other}} { // tasks.manage etc. are not enough
		if _, err := s.CreateDefinition(ctx, c, p, DefinitionInput{Title: "x", Rule: daily()}); !errors.Is(err, ErrForbidden) {
			t.Errorf("create %+v: %v", p, err)
		}
		if _, err := s.Get(ctx, p, id(1)); !errors.Is(err, ErrForbidden) {
			t.Errorf("get: %v", err)
		}
		if _, err := s.List(ctx, p, Page{}); !errors.Is(err, ErrForbidden) {
			t.Errorf("list: %v", err)
		}
		if _, err := s.UpdateDefinition(ctx, c, p, id(1), nil, UpdateDefinitionInput{Title: strp("x")}); !errors.Is(err, ErrForbidden) {
			t.Errorf("update: %v", err)
		}
		if _, err := s.Pause(ctx, c, p, id(1), nil); !errors.Is(err, ErrForbidden) {
			t.Errorf("pause: %v", err)
		}
		if _, err := s.Resume(ctx, c, p, id(1), nil); !errors.Is(err, ErrForbidden) {
			t.Errorf("resume: %v", err)
		}
		if err := s.DeleteDefinition(ctx, c, p, id(1), nil); !errors.Is(err, ErrForbidden) {
			t.Errorf("delete: %v", err)
		}
	}
	if len(st.defs) != 0 {
		t.Error("a definition was created without permission")
	}
}

func TestCreateDefinitionValidatesAndSchedules(t *testing.T) {
	s, _, _ := newRecSvc(fixedNow)
	ctx := context.Background()
	c := callerOf(pRecurrence)
	d, err := s.CreateDefinition(ctx, c, pRecurrence, DefinitionInput{Title: "  Check backups ", Rule: daily(), DueAfterHours: intp(24)})
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "Check backups" || d.Priority != PriorityNormal || !d.Active || d.NextRunAt == nil || !d.NextRunAt.Equal(time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("definition = %+v", d)
	}
	if d.CreatedByUserID == nil || *d.CreatedByUserID != manager {
		t.Errorf("created by = %v", d.CreatedByUserID)
	}
	bad := map[string]DefinitionInput{
		"no title":      {Title: " ", Rule: daily()},
		"bad priority":  {Title: "x", Priority: "asap", Rule: daily()},
		"bad rule":      {Title: "x", Rule: Rule{Frequency: "hourly"}},
		"due too small": {Title: "x", Rule: daily(), DueAfterHours: intp(0)},
		"due too large": {Title: "x", Rule: daily(), DueAfterHours: intp(9000)},
		"long desc":     {Title: "x", Rule: daily(), Description: strings.Repeat("x", 10001)},
		"unknown user":  {Title: "x", Rule: daily(), AssignedUserID: strp("00000000-0000-7000-8000-0000000000ff")},
		"unknown team":  {Title: "x", Rule: daily(), AssignedTeamID: strp("00000000-0000-7000-8000-0000000000fe")},
	}
	for name, in := range bad {
		if _, err := s.CreateDefinition(ctx, c, pRecurrence, in); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestPauseResumeAreIdempotentAndResumeSkipsMissedRuns(t *testing.T) {
	now := fixedNow
	st := newMemDefs()
	dir := fakeDir{activeUsers: map[string]bool{manager: true}, activeTeams: map[string]bool{}}
	clock := now
	s := NewRecurrenceService(st, dir, func() time.Time { return clock })
	ctx := context.Background()
	c := callerOf(pRecurrence)
	d, _ := s.CreateDefinition(ctx, c, pRecurrence, DefinitionInput{Title: "t", Rule: daily()})

	p, err := s.Pause(ctx, c, pRecurrence, d.ID, nil)
	if err != nil || p.Active || p.NextRunAt != nil {
		t.Fatalf("pause = %+v %v", p, err)
	}
	before := len(st.changes)
	if _, err := s.Pause(ctx, c, pRecurrence, d.ID, nil); err != nil || len(st.changes) != before {
		t.Errorf("pausing twice must be a no-op: %v", err)
	}
	clock = now.Add(10 * 24 * time.Hour) // ten days paused
	r, err := s.Resume(ctx, c, pRecurrence, d.ID, nil)
	if err != nil || !r.Active || r.NextRunAt == nil {
		t.Fatalf("resume = %+v %v", r, err)
	}
	if want := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC); !r.NextRunAt.Equal(want) {
		t.Errorf("resumed next run = %v, want %v (the first run after now, no catch-up)", r.NextRunAt, want)
	}
	before = len(st.changes)
	if _, err := s.Resume(ctx, c, pRecurrence, d.ID, nil); err != nil || len(st.changes) != before {
		t.Errorf("resuming twice must be a no-op: %v", err)
	}
}

func TestUpdateDefinition(t *testing.T) {
	s, st, _ := newRecSvc(fixedNow)
	ctx := context.Background()
	c := callerOf(pRecurrence)
	d, _ := s.CreateDefinition(ctx, c, pRecurrence, DefinitionInput{Title: "t", Rule: daily(), AssignedUserID: strp(worker), DueAfterHours: intp(5)})

	var inv *InvalidInputError
	if _, err := s.UpdateDefinition(ctx, c, pRecurrence, d.ID, nil, UpdateDefinitionInput{}); !errors.As(err, &inv) {
		t.Errorf("empty update: %v", err)
	}
	if _, err := s.UpdateDefinition(ctx, c, pRecurrence, d.ID, nil, UpdateDefinitionInput{ClearAssignment: true, AssignedUserID: strp(worker)}); !errors.As(err, &inv) {
		t.Errorf("clear and assign: %v", err)
	}
	weekly := Rule{Frequency: FreqWeekly, Interval: 1, Weekday: 1, TimeOfDay: "08:00", Timezone: "UTC", StartsOn: "2026-01-01"}
	u, err := s.UpdateDefinition(ctx, c, pRecurrence, d.ID, nil, UpdateDefinitionInput{Rule: &weekly, ClearAssignment: true, ClearDueAfter: true, Title: strp("new")})
	if err != nil {
		t.Fatal(err)
	}
	if u.Rule.Frequency != FreqWeekly || u.AssignedUserID != nil || u.DueAfterHours != nil || u.Title != "new" {
		t.Errorf("updated = %+v", u)
	}
	if want := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC); u.NextRunAt == nil || !u.NextRunAt.Equal(want) { // next Monday 08:00
		t.Errorf("a changed rule reschedules from now: %v, want %v", u.NextRunAt, want)
	}
	last := st.changes[len(st.changes)-1]
	if got := strings.Join(last.Metadata["changedFields"].([]string), ","); got != "assignment,dueAfterHours,rule" && got != "title,assignment,dueAfterHours,rule" {
		t.Errorf("changed fields = %s", got)
	}
	// Unchanged values are a no-op and never bump the version.
	n := len(st.changes)
	if _, err := s.UpdateDefinition(ctx, c, pRecurrence, d.ID, nil, UpdateDefinitionInput{Title: strp("new"), Rule: &weekly}); err != nil || len(st.changes) != n {
		t.Errorf("no-op update: %v", err)
	}
	// A paused definition keeps no next run when its rule changes.
	if _, err := s.Pause(ctx, c, pRecurrence, d.ID, nil); err != nil {
		t.Fatal(err)
	}
	monthly := Rule{Frequency: FreqMonthly, Interval: 1, DayOfMonth: 1, TimeOfDay: "08:00", Timezone: "UTC", StartsOn: "2026-01-01"}
	if p, err := s.UpdateDefinition(ctx, c, pRecurrence, d.ID, nil, UpdateDefinitionInput{Rule: &monthly}); err != nil || p.NextRunAt != nil || p.Active {
		t.Errorf("paused update = %+v %v", p, err)
	}
	stale := 1
	if _, err := s.UpdateDefinition(ctx, c, pRecurrence, d.ID, &stale, UpdateDefinitionInput{Title: strp("late")}); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale version: %v", err)
	}
}

func TestDeleteDefinition(t *testing.T) {
	s, st, _ := newRecSvc(fixedNow)
	ctx := context.Background()
	c := callerOf(pRecurrence)
	d, _ := s.CreateDefinition(ctx, c, pRecurrence, DefinitionInput{Title: "t", Rule: daily()})
	stale := 9
	if err := s.DeleteDefinition(ctx, c, pRecurrence, d.ID, &stale); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale delete: %v", err)
	}
	if err := s.DeleteDefinition(ctx, c, pRecurrence, d.ID, nil); err != nil || len(st.defs) != 0 {
		t.Errorf("delete: %v", err)
	}
	if err := s.DeleteDefinition(ctx, c, pRecurrence, d.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestGenerateDueGeneratesOneTaskPerDefinitionAndSkipsMissedRuns(t *testing.T) {
	clock := fixedNow
	st := newMemDefs()
	dir := fakeDir{activeUsers: map[string]bool{manager: true, worker: true}, activeTeams: map[string]bool{teamX: true}}
	s := NewRecurrenceService(st, dir, func() time.Time { return clock })
	ctx := context.Background()
	c := callerOf(pRecurrence)
	d, _ := s.CreateDefinition(ctx, c, pRecurrence, DefinitionInput{Title: "t", Rule: daily(), AssignedUserID: strp(worker)})

	if n, err := s.GenerateDue(ctx); err != nil || n != 0 {
		t.Fatalf("nothing is due yet: %d %v", n, err)
	}
	clock = fixedNow.Add(5 * 24 * time.Hour) // five runs were missed
	n, err := s.GenerateDue(ctx)
	if err != nil || n != 1 {
		t.Fatalf("generated %d %v, want exactly one task for the oldest due run", n, err)
	}
	got := st.defs[d.ID]
	if want := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC); !got.NextRunAt.Equal(want) { // clock = Oct 7 12:00
		t.Errorf("next run = %v, want %v (first run after now)", got.NextRunAt, want)
	}
	if n, _ := s.GenerateDue(ctx); n != 0 {
		t.Errorf("a second pass generated %d tasks", n)
	}
	// A paused definition never generates.
	clock = clock.Add(48 * time.Hour)
	if _, err := s.Pause(ctx, c, pRecurrence, d.ID, nil); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.GenerateDue(ctx); n != 0 {
		t.Errorf("paused definition generated %d tasks", n)
	}
}

func TestGenerationDropsInactiveAssigneesButStillCreatesTheTask(t *testing.T) {
	clock := fixedNow
	st := newMemDefs()
	dir := fakeDir{activeUsers: map[string]bool{manager: true, worker: true}, activeTeams: map[string]bool{teamX: true}}
	s := NewRecurrenceService(st, dir, func() time.Time { return clock })
	ctx := context.Background()
	c := callerOf(pRecurrence)
	if _, err := s.CreateDefinition(ctx, c, pRecurrence, DefinitionInput{Title: "t", Rule: daily(), AssignedUserID: strp(worker), AssignedTeamID: strp(teamX)}); err != nil {
		t.Fatal(err)
	}
	dir.activeUsers[worker] = false // deactivated since the definition was created
	s = NewRecurrenceService(st, dir, func() time.Time { return clock })
	clock = fixedNow.Add(24 * time.Hour)
	if n, err := s.GenerateDue(ctx); err != nil || n != 1 {
		t.Fatalf("generated %d %v", n, err)
	}
	g := st.plans[0]
	if g.AssignedUserID != nil || g.AssignedTeamID == nil || !g.AssigneeDropped {
		t.Errorf("plan = %+v, want the inactive user dropped and the active team kept", g)
	}
}

func TestSystemActorCanOwnRecurrenceWithoutUser(t *testing.T) {
	s, _, _ := newRecSvc(fixedNow)
	c := Caller{Actor: audit.UserActor(manager), CorrelationID: "c"}
	if _, err := s.CreateDefinition(context.Background(), c, pRecurrence, DefinitionInput{Title: "t", Rule: daily()}); err != nil {
		t.Fatal(err)
	}
}

func intp(n int) *int { return &n }
