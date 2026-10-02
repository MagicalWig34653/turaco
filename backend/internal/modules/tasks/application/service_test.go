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

// memStore is an in-memory Store: it applies the same contract as the
// PostgreSQL store (decide on the current state, version bump, NoChange).
type memStore struct {
	tasks   map[string]Task
	seq     int
	changes []Change
}

func newMemStore() *memStore { return &memStore{tasks: map[string]Task{}} }

func (m *memStore) Insert(_ context.Context, _ Caller, n NewTask) (Task, error) {
	m.seq++
	t := Task{
		ID: id(m.seq), Title: n.Title, Description: n.Description, Status: StatusOpen, Priority: n.Priority,
		DueAt: n.DueAt, AssignedUserID: n.AssignedUserID, AssignedTeamID: n.AssignedTeamID,
		CreatedByUserID: n.CreatedBy, Version: 1,
	}
	m.tasks[t.ID] = t
	return t, nil
}

func (m *memStore) Get(_ context.Context, taskID string) (Task, error) {
	t, ok := m.tasks[taskID]
	if !ok {
		return Task{}, ErrNotFound
	}
	return t, nil
}

func (m *memStore) Change(_ context.Context, _ Caller, taskID string, decide func(Task) (Change, error)) (Task, error) {
	cur, ok := m.tasks[taskID]
	if !ok {
		return Task{}, ErrNotFound
	}
	ch, err := decide(cur)
	if err != nil {
		return Task{}, err
	}
	if ch.NoChange {
		return cur, nil
	}
	ch.Next.Version = cur.Version + 1
	m.tasks[taskID] = ch.Next
	m.changes = append(m.changes, ch)
	return ch.Next, nil
}

func (m *memStore) List(_ context.Context, q ListQuery) (Result[Task], error) {
	var out []Task
	for _, t := range m.tasks {
		if q.Mine != nil {
			mine := t.AssignedUserID != nil && *t.AssignedUserID == q.Mine.UserID
			for _, team := range q.Mine.TeamIDs {
				if t.AssignedTeamID != nil && *t.AssignedTeamID == team {
					mine = true
				}
			}
			if !mine {
				continue
			}
		}
		if len(q.Statuses) > 0 && !contains(q.Statuses, t.Status) {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return Result[Task]{Items: out}, nil
}

func id(n int) string {
	return "00000000-0000-7000-8000-" + strings.Repeat("0", 11) + string(rune('a'+n-1))
}

type fakeDir struct {
	activeUsers map[string]bool
	activeTeams map[string]bool
	teamsOf     map[string][]string
	membersOf   map[string][]string
}

func (d fakeDir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, i := range ids {
		if d.activeUsers[i] {
			out[i] = true
		}
	}
	return out, nil
}
func (d fakeDir) ActiveTeams(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, i := range ids {
		if d.activeTeams[i] {
			out[i] = true
		}
	}
	return out, nil
}
func (d fakeDir) UserNames(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, i := range ids {
		out[i] = "name-" + i
	}
	return out, nil
}
func (d fakeDir) TeamNames(_ context.Context, ids []string) (map[string]string, error) {
	return d.UserNames(nil, ids)
}
func (d fakeDir) CurrentMemberIDs(_ context.Context, teamID string) ([]string, error) {
	return d.membersOf[teamID], nil
}
func (d fakeDir) CurrentTeamIDs(_ context.Context, userID string) ([]string, error) {
	return d.teamsOf[userID], nil
}

const (
	manager = "00000000-0000-7000-8000-0000000000a1"
	worker  = "00000000-0000-7000-8000-0000000000b1"
	other   = "00000000-0000-7000-8000-0000000000c1"
	viewer  = "00000000-0000-7000-8000-0000000000d1"
	teamX   = "00000000-0000-7000-8000-0000000000e1"
	teamY   = "00000000-0000-7000-8000-0000000000e2"
)

var (
	pManager = Principal{UserID: manager, Manage: true}
	pWorker  = Principal{UserID: worker, Work: true}
	pOther   = Principal{UserID: other, Work: true}
	pViewer  = Principal{UserID: viewer, ViewAll: true}
)

func newSvc() (*Service, *memStore) {
	st := newMemStore()
	dir := fakeDir{
		activeUsers: map[string]bool{manager: true, worker: true, other: true, viewer: true},
		activeTeams: map[string]bool{teamX: true, teamY: true},
		teamsOf:     map[string][]string{worker: {teamX}},
	}
	fixed := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	return NewService(st, dir, func() time.Time { return fixed }), st
}

func callerOf(p Principal) Caller {
	return Caller{Actor: audit.UserActor(p.UserID), CorrelationID: "corr"}
}

func strp(s string) *string { return &s }

func mustCreate(t *testing.T, s *Service, in CreateInput) TaskView {
	t.Helper()
	v, err := s.Create(context.Background(), callerOf(pManager), pManager, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return v
}

func TestCreateRequiresManageAndValidates(t *testing.T) {
	s, st := newSvc()
	ctx := context.Background()
	for _, p := range []Principal{pWorker, pViewer} {
		if _, err := s.Create(ctx, callerOf(p), p, CreateInput{Title: "x"}); !errors.Is(err, ErrForbidden) {
			t.Errorf("%+v: err = %v, want ErrForbidden", p, err)
		}
	}
	bad := map[string]CreateInput{
		"empty title":    {Title: " "},
		"control chars":  {Title: "a\x00b"},
		"long title":     {Title: strings.Repeat("x", 201)},
		"bad priority":   {Title: "x", Priority: "asap"},
		"long desc":      {Title: "x", Description: strings.Repeat("x", 10001)},
		"control in des": {Title: "x", Description: "a\x07b"},
	}
	for name, in := range bad {
		var inv *InvalidInputError
		if _, err := s.Create(ctx, callerOf(pManager), pManager, in); !errors.As(err, &inv) {
			t.Errorf("%s: err = %v, want InvalidInputError", name, err)
		}
	}
	if len(st.tasks) != 0 {
		t.Errorf("%d tasks stored by rejected requests", len(st.tasks))
	}
	v, err := s.Create(ctx, callerOf(pManager), pManager, CreateInput{Title: "  Replace toner \n", Description: "line1\nline2"})
	if err != nil || v.Title != "Replace toner" || v.Priority != PriorityNormal || v.Status != StatusOpen || v.Version != 1 {
		t.Fatalf("create = %+v %v", v, err)
	}
	if v.CreatedByUserID == nil || *v.CreatedByUserID != manager {
		t.Errorf("created by = %v, want the actor", v.CreatedByUserID)
	}
}

func TestCreateRejectsInactiveAssignees(t *testing.T) {
	s, _ := newSvc()
	ctx := context.Background()
	for name, in := range map[string]CreateInput{
		"unknown user": {Title: "x", AssignedUserID: strp("00000000-0000-7000-8000-0000000000ff")},
		"unknown team": {Title: "x", AssignedTeamID: strp("00000000-0000-7000-8000-0000000000fe")},
		"bad id":       {Title: "x", AssignedUserID: strp("not-a-uuid")},
	} {
		if _, err := s.Create(ctx, callerOf(pManager), pManager, in); !errors.Is(err, ErrAssigneeInvalid) {
			t.Errorf("%s: err = %v, want ErrAssigneeInvalid", name, err)
		}
	}
}

func TestLifecycleTransitions(t *testing.T) {
	type step struct {
		op     Operation
		reason string
		want   string // resulting status, or "" for an invalid transition
	}
	cases := []struct {
		name  string
		steps []step
	}{
		{"happy path", []step{{OpStart, "", StatusInProgress}, {OpComplete, "", StatusCompleted}}},
		{"complete from open", []step{{OpComplete, "", StatusCompleted}}},
		{"block and unblock", []step{{OpBlock, "waiting for parts", StatusBlocked}, {OpUnblock, "", StatusOpen}, {OpStart, "", StatusInProgress}}},
		{"start from blocked", []step{{OpBlock, "r", StatusBlocked}, {OpStart, "", StatusInProgress}}},
		{"cannot complete blocked", []step{{OpBlock, "r", StatusBlocked}, {OpComplete, "", ""}}},
		{"cannot start started", []step{{OpStart, "", StatusInProgress}, {OpStart, "", ""}}},
		{"cannot unblock open", []step{{OpUnblock, "", ""}}},
		{"cancel then nothing", []step{{OpCancel, "duplicate", StatusCancelled}, {OpStart, "", ""}, {OpComplete, "", ""}, {OpBlock, "r", ""}, {OpCancel, "r", ""}}},
		{"reopen completed", []step{{OpComplete, "", StatusCompleted}, {OpReopen, "was not done", StatusOpen}}},
		{"reopen cancelled", []step{{OpCancel, "x", StatusCancelled}, {OpReopen, "needed after all", StatusOpen}}},
		{"cannot reopen open", []step{{OpReopen, "r", ""}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newSvc()
			v := mustCreate(t, s, CreateInput{Title: "t"})
			for i, st := range c.steps {
				got, err := s.Transition(context.Background(), callerOf(pManager), pManager, v.ID, nil, st.op, st.reason)
				if st.want == "" {
					var tr *InvalidTransitionError
					if !errors.As(err, &tr) {
						t.Fatalf("step %d %s: err = %v, want InvalidTransitionError", i, st.op, err)
					}
					continue
				}
				if err != nil || got.Status != st.want {
					t.Fatalf("step %d %s: status=%q err=%v, want %q", i, st.op, got.Status, err, st.want)
				}
			}
		})
	}
}

func TestStateInvariantsAfterTransitions(t *testing.T) {
	s, _ := newSvc()
	ctx := context.Background()
	v := mustCreate(t, s, CreateInput{Title: "t"})
	b, _ := s.Transition(ctx, callerOf(pManager), pManager, v.ID, nil, OpBlock, "  parts missing ")
	if b.StatusReason == nil || *b.StatusReason != "parts missing" || b.CompletedAt != nil {
		t.Errorf("blocked = %+v", b.Task)
	}
	u, _ := s.Transition(ctx, callerOf(pManager), pManager, v.ID, nil, OpUnblock, "")
	if u.StatusReason != nil {
		t.Errorf("unblocked task keeps reason %q", *u.StatusReason)
	}
	c, _ := s.Transition(ctx, callerOf(pManager), pManager, v.ID, nil, OpComplete, "")
	if c.CompletedAt == nil || !c.CompletedAt.Equal(time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)) || c.CompletedByUserID == nil || *c.CompletedByUserID != manager {
		t.Errorf("completed = %+v", c.Task)
	}
	r, _ := s.Transition(ctx, callerOf(pManager), pManager, v.ID, nil, OpReopen, "again")
	if r.CompletedAt != nil || r.CompletedByUserID != nil || r.StatusReason != nil || r.Status != StatusOpen {
		t.Errorf("reopened = %+v", r.Task)
	}
	for _, op := range []Operation{OpBlock, OpCancel, OpReopen} {
		var inv *InvalidInputError
		if _, err := s.Transition(ctx, callerOf(pManager), pManager, v.ID, nil, op, " "); !errors.As(err, &inv) {
			t.Errorf("%s without reason: err = %v, want InvalidInputError", op, err)
		}
	}
}

func TestCompleteEmitsEventAndAssignEmitsEvent(t *testing.T) {
	s, st := newSvc()
	ctx := context.Background()
	v := mustCreate(t, s, CreateInput{Title: "t"})
	if _, err := s.Assign(ctx, callerOf(pManager), pManager, v.ID, nil, strp(worker), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transition(ctx, callerOf(pManager), pManager, v.ID, nil, OpComplete, ""); err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, ch := range st.changes {
		for _, e := range ch.Events {
			types = append(types, e.Type)
		}
	}
	if strings.Join(types, ",") != "TaskAssigned,TaskCompleted" {
		t.Errorf("events = %v", types)
	}
	ev := st.changes[0].Events[0].Payload
	if ev["taskId"] != v.ID || *(ev["assignedUserId"].(*string)) != worker || ev["previousUserId"].(*string) != nil {
		t.Errorf("assigned payload = %+v", ev)
	}
}

func TestAssignAndUnassignAreIdempotent(t *testing.T) {
	s, st := newSvc()
	ctx := context.Background()
	v := mustCreate(t, s, CreateInput{Title: "t"})
	if _, err := s.Unassign(ctx, callerOf(pManager), pManager, v.ID, nil); err != nil || len(st.changes) != 0 {
		t.Fatalf("unassigning an unassigned task must be a no-op: %v, %d changes", err, len(st.changes))
	}
	a1, _ := s.Assign(ctx, callerOf(pManager), pManager, v.ID, nil, strp(worker), strp(teamX))
	a2, err := s.Assign(ctx, callerOf(pManager), pManager, v.ID, nil, strp(worker), strp(teamX))
	if err != nil || a2.Version != a1.Version || len(st.changes) != 1 {
		t.Fatalf("repeated assign: version %d -> %d, %d changes, err %v", a1.Version, a2.Version, len(st.changes), err)
	}
	var inv *InvalidInputError
	if _, err := s.Assign(ctx, callerOf(pManager), pManager, v.ID, nil, nil, nil); !errors.As(err, &inv) {
		t.Errorf("assign without user and team: %v", err)
	}
	un, err := s.Unassign(ctx, callerOf(pManager), pManager, v.ID, nil)
	if err != nil || un.AssignedUserID != nil || un.AssignedTeamID != nil {
		t.Errorf("unassign = %+v %v", un.Task, err)
	}
	// Terminal tasks cannot be (re)assigned.
	if _, err := s.Transition(ctx, callerOf(pManager), pManager, v.ID, nil, OpCancel, "x"); err != nil {
		t.Fatal(err)
	}
	var tr *InvalidTransitionError
	if _, err := s.Assign(ctx, callerOf(pManager), pManager, v.ID, nil, strp(worker), nil); !errors.As(err, &tr) {
		t.Errorf("assign cancelled task: %v", err)
	}
}

func TestUpdateDetails(t *testing.T) {
	s, st := newSvc()
	ctx := context.Background()
	due := time.Date(2026, 11, 1, 9, 0, 0, 0, time.FixedZone("x", 3600))
	v := mustCreate(t, s, CreateInput{Title: "old", Description: "d", DueAt: &due})
	if v.DueAt == nil || v.DueAt.Location() != time.UTC {
		t.Errorf("due date must be stored in UTC: %v", v.DueAt)
	}
	title, prio := "new", PriorityHigh
	u, err := s.UpdateDetails(ctx, callerOf(pManager), pManager, v.ID, nil, UpdateInput{Title: &title, Priority: &prio, ClearDueAt: true})
	if err != nil || u.Title != "new" || u.Priority != PriorityHigh || u.DueAt != nil || u.Description == nil {
		t.Fatalf("update = %+v %v", u.Task, err)
	}
	last := st.changes[len(st.changes)-1]
	if got := last.Metadata["changedFields"].([]string); strings.Join(got, ",") != "title,priority,dueAt" {
		t.Errorf("changed fields = %v", got)
	}
	empty := ""
	c, _ := s.UpdateDetails(ctx, callerOf(pManager), pManager, v.ID, nil, UpdateInput{Description: &empty})
	if c.Description != nil {
		t.Errorf("empty description must clear: %v", *c.Description)
	}
	before := len(st.changes)
	if _, err := s.UpdateDetails(ctx, callerOf(pManager), pManager, v.ID, nil, UpdateInput{Title: &title}); err != nil || len(st.changes) != before {
		t.Errorf("unchanged update must be a no-op: %v", err)
	}
	var inv *InvalidInputError
	if _, err := s.UpdateDetails(ctx, callerOf(pManager), pManager, v.ID, nil, UpdateInput{}); !errors.As(err, &inv) {
		t.Errorf("empty update: %v", err)
	}
	if _, err := s.Transition(ctx, callerOf(pManager), pManager, v.ID, nil, OpComplete, ""); err != nil {
		t.Fatal(err)
	}
	var tr *InvalidTransitionError
	if _, err := s.UpdateDetails(ctx, callerOf(pManager), pManager, v.ID, nil, UpdateInput{Title: strp("late")}); !errors.As(err, &tr) {
		t.Errorf("update completed task: %v", err)
	}
}

func TestOptimisticVersion(t *testing.T) {
	s, _ := newSvc()
	ctx := context.Background()
	v := mustCreate(t, s, CreateInput{Title: "t"})
	stale, current := 1, 1
	if _, err := s.Transition(ctx, callerOf(pManager), pManager, v.ID, &current, OpStart, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transition(ctx, callerOf(pManager), pManager, v.ID, &stale, OpBlock, "r"); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale version: %v, want ErrVersionConflict", err)
	}
	if _, err := s.Assign(ctx, callerOf(pManager), pManager, v.ID, &stale, strp(worker), nil); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale assign: %v", err)
	}
}

// The authorization matrix: who sees and who works which task.
func TestVisibilityAndAuthority(t *testing.T) {
	s, _ := newSvc()
	ctx := context.Background()
	own := mustCreate(t, s, CreateInput{Title: "own", AssignedUserID: strp(worker)})
	viaTeam := mustCreate(t, s, CreateInput{Title: "team", AssignedTeamID: strp(teamX)})
	foreign := mustCreate(t, s, CreateInput{Title: "foreign", AssignedUserID: strp(other)})
	foreignTeam := mustCreate(t, s, CreateInput{Title: "foreign team", AssignedTeamID: strp(teamY)})
	unassigned := mustCreate(t, s, CreateInput{Title: "unassigned"})

	for _, tk := range []TaskView{own, viaTeam} {
		if _, err := s.Get(ctx, pWorker, tk.ID); err != nil {
			t.Errorf("worker must see %q: %v", tk.Title, err)
		}
	}
	for _, tk := range []TaskView{foreign, foreignTeam, unassigned} {
		if _, err := s.Get(ctx, pWorker, tk.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("worker must not see %q (want ErrNotFound): %v", tk.Title, err)
		}
		// A task the caller cannot see must not reveal whether an operation would be allowed.
		if _, err := s.Transition(ctx, callerOf(pWorker), pWorker, tk.ID, nil, OpStart, ""); !errors.Is(err, ErrNotFound) {
			t.Errorf("worker start on %q: %v, want ErrNotFound", tk.Title, err)
		}
	}
	// A worker works assigned tasks ...
	for _, tk := range []TaskView{own, viaTeam} {
		if _, err := s.Transition(ctx, callerOf(pWorker), pWorker, tk.ID, nil, OpStart, ""); err != nil {
			t.Errorf("worker start %q: %v", tk.Title, err)
		}
	}
	// ... but cannot reassign, edit, cancel or reopen them.
	if _, err := s.Assign(ctx, callerOf(pWorker), pWorker, own.ID, nil, strp(other), nil); !errors.Is(err, ErrForbidden) {
		t.Errorf("worker assign: %v, want ErrForbidden", err)
	}
	if _, err := s.UpdateDetails(ctx, callerOf(pWorker), pWorker, own.ID, nil, UpdateInput{Title: strp("x")}); !errors.Is(err, ErrForbidden) {
		t.Errorf("worker update: %v, want ErrForbidden", err)
	}
	if _, err := s.Transition(ctx, callerOf(pWorker), pWorker, own.ID, nil, OpCancel, "x"); !errors.Is(err, ErrForbidden) {
		t.Errorf("worker cancel: %v, want ErrForbidden", err)
	}
	if _, err := s.Transition(ctx, callerOf(pWorker), pWorker, own.ID, nil, OpComplete, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transition(ctx, callerOf(pWorker), pWorker, own.ID, nil, OpReopen, "x"); !errors.Is(err, ErrForbidden) {
		t.Errorf("worker reopen: %v, want ErrForbidden", err)
	}
	// A viewer sees everything and changes nothing.
	for _, tk := range []TaskView{own, foreign, unassigned} {
		if _, err := s.Get(ctx, pViewer, tk.ID); err != nil {
			t.Errorf("viewer must see %q: %v", tk.Title, err)
		}
		if _, err := s.Transition(ctx, callerOf(pViewer), pViewer, tk.ID, nil, OpStart, ""); !errors.Is(err, ErrForbidden) {
			t.Errorf("viewer start %q: %v, want ErrForbidden", tk.Title, err)
		}
	}
	// A manager sees and works everything.
	if _, err := s.Transition(ctx, callerOf(pManager), pManager, foreign.ID, nil, OpStart, ""); err != nil {
		t.Errorf("manager start: %v", err)
	}
	// A principal without any task permission sees nothing.
	none := Principal{UserID: other}
	if _, err := s.Get(ctx, none, foreign.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("no permission: %v, want ErrNotFound", err)
	}
	if res, err := s.List(ctx, none, ListFilter{}); err != nil || len(res.Items) != 0 {
		t.Errorf("no permission list: %d items, %v", len(res.Items), err)
	}
}

func TestListScopesByPermission(t *testing.T) {
	s, _ := newSvc()
	ctx := context.Background()
	mustCreate(t, s, CreateInput{Title: "own", AssignedUserID: strp(worker)})
	mustCreate(t, s, CreateInput{Title: "team", AssignedTeamID: strp(teamX)})
	mustCreate(t, s, CreateInput{Title: "foreign", AssignedUserID: strp(other)})
	mustCreate(t, s, CreateInput{Title: "unassigned"})

	count := func(p Principal, f ListFilter) int {
		res, err := s.List(ctx, p, f)
		if err != nil {
			t.Fatal(err)
		}
		return len(res.Items)
	}
	if n := count(pWorker, ListFilter{}); n != 2 {
		t.Errorf("worker list = %d, want 2 (own and via team)", n)
	}
	if n := count(pViewer, ListFilter{}); n != 4 {
		t.Errorf("viewer list = %d, want 4", n)
	}
	if n := count(pManager, ListFilter{}); n != 4 {
		t.Errorf("manager list = %d, want 4", n)
	}
	// A broad filter must not widen a worker's scope.
	if n := count(pWorker, ListFilter{AssignedUserID: other}); n != 2 {
		// the store fake ignores field filters; the restriction to "mine" must still hold
		t.Errorf("worker with foreign filter = %d, want at most own tasks", n)
	}
	if res, _ := s.MyWork(ctx, pWorker, Page{}); len(res.Items) != 2 {
		t.Errorf("my work = %d, want 2", len(res.Items))
	}
	mine := Principal{UserID: manager, Manage: true}
	if res, _ := s.MyWork(ctx, mine, Page{}); len(res.Items) != 0 {
		t.Errorf("manager my work = %d, want 0 (nothing assigned to the manager)", len(res.Items))
	}
	var inv *InvalidInputError
	if _, err := s.List(ctx, pViewer, ListFilter{Statuses: []string{"done"}}); !errors.As(err, &inv) {
		t.Errorf("unknown status: %v", err)
	}
	if _, err := s.List(ctx, pViewer, ListFilter{Priority: "asap"}); !errors.As(err, &inv) {
		t.Errorf("unknown priority: %v", err)
	}
}

func TestMyWorkExcludesFinishedTasks(t *testing.T) {
	s, _ := newSvc()
	ctx := context.Background()
	a := mustCreate(t, s, CreateInput{Title: "a", AssignedUserID: strp(worker)})
	b := mustCreate(t, s, CreateInput{Title: "b", AssignedUserID: strp(worker)})
	if _, err := s.Transition(ctx, callerOf(pWorker), pWorker, a.ID, nil, OpComplete, ""); err != nil {
		t.Fatal(err)
	}
	res, _ := s.MyWork(ctx, pWorker, Page{})
	if len(res.Items) != 1 || res.Items[0].ID != b.ID {
		t.Errorf("my work = %+v, want only the unfinished task", res.Items)
	}
}

func TestViewIncludesAssigneeNames(t *testing.T) {
	s, _ := newSvc()
	v := mustCreate(t, s, CreateInput{Title: "t", AssignedUserID: strp(worker), AssignedTeamID: strp(teamX)})
	if v.AssignedUserName == nil || *v.AssignedUserName != "name-"+worker || v.AssignedTeamName == nil {
		t.Errorf("names = %v %v", v.AssignedUserName, v.AssignedTeamName)
	}
}

func TestCallerIsRequired(t *testing.T) {
	s, st := newSvc()
	ctx := context.Background()
	for name, c := range map[string]Caller{
		"no actor": {CorrelationID: "c"}, "no correlation id": {Actor: audit.UserActor(manager)},
	} {
		if _, err := s.Create(ctx, c, pManager, CreateInput{Title: "t"}); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if len(st.tasks) != 0 {
		t.Error("task created for an invalid caller")
	}
}

func TestSystemActorCreatesTasksWithoutCreator(t *testing.T) {
	s, st := newSvc()
	sys := Caller{Actor: audit.SystemActor("recurrence"), CorrelationID: "corr"}
	v, err := s.Create(context.Background(), sys, pManager, CreateInput{Title: "Monthly backup check"})
	if err != nil || v.CreatedByUserID != nil {
		t.Fatalf("create = %+v %v, want no creating user", v.Task, err)
	}
	if len(st.tasks) != 1 {
		t.Errorf("tasks = %d", len(st.tasks))
	}
}
