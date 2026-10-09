package views_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/views"
)

// ---- fakes of the three ports ----

type fakeDir struct {
	mu       sync.Mutex
	teams    map[string][]string // user -> current Teams
	groups   map[string][]string // user -> Directory Groups
	inactive map[string]bool     // deactivated users
	live     map[string]bool     // existing active teams
	known    map[string]bool     // existing users
}

func newDir() *fakeDir {
	return &fakeDir{teams: map[string][]string{}, groups: map[string][]string{}, inactive: map[string]bool{}, live: map[string]bool{}, known: map[string]bool{}}
}

func (d *fakeDir) CurrentTeamIDs(_ context.Context, u string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.teams[u]), nil
}

func (d *fakeDir) GroupIDsOfUser(_ context.Context, u string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.groups[u]), nil
}

func (d *fakeDir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = d.known[id] && !d.inactive[id]
	}
	return out, nil
}

func (d *fakeDir) ActiveTeams(_ context.Context, ids []string) (map[string]bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = d.live[id]
	}
	return out, nil
}

func (d *fakeDir) UserNames(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		out[id] = "User " + id[:4]
	}
	return out, nil
}

func (d *fakeDir) setTeams(user string, teams ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.teams[user] = teams
}

func (d *fakeDir) deactivate(user string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.inactive[user] = true
}

type fakeGate struct {
	mu  sync.Mutex
	off map[string]bool
}

func (g *fakeGate) Enabled(_ context.Context, key string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.off[key], nil
}

func (g *fakeGate) set(key string, on bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.off[key] = !on
}

type fakeRunner struct {
	mu      sync.Mutex
	info    query.Info
	queryFn func(c views.Caller, resource string, req query.Request) (views.Result, error)
	calls   []query.Request
	callers []views.Caller
}

func defaultInfo() query.Info {
	return query.Info{Resource: "tickets", Fields: []query.FieldInfo{
		{Key: "status", Type: query.TypeEnum, Filterable: true, Operators: []query.Op{query.OpEquals, query.OpIn}, EnumValues: []string{"open", "closed"}},
		{Key: "title", Type: query.TypeText, Filterable: true, Searchable: true, Operators: []query.Op{query.OpEquals, query.OpContains}},
		{Key: "created_at", Type: query.TypeDateTime, Filterable: true, Sortable: true, Operators: []query.Op{query.OpBefore, query.OpAfter}},
	}}
}

func (r *fakeRunner) Fields(_ context.Context, _ views.Caller, _ string) (query.Info, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.info, nil
}

func (r *fakeRunner) Query(_ context.Context, c views.Caller, resource string, req query.Request) (views.Result, error) {
	r.mu.Lock()
	r.calls = append(r.calls, req)
	r.callers = append(r.callers, c)
	fn := r.queryFn
	r.mu.Unlock()
	if fn != nil {
		return fn(c, resource, req)
	}
	return views.Result{Items: json.RawMessage(`[{"id":"row-1"}]`)}, nil
}

func (r *fakeRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *fakeRunner) lastCall() query.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[len(r.calls)-1]
}

func (r *fakeRunner) setInfo(i query.Info) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.info = i
}

// ---- environment ----

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	svc    *views.Service
	dir    *fakeDir
	gate   *fakeGate
	runner *fakeRunner
	users  []string
}

const (
	moduleTickets = "servicedesk"
	moduleTasks   = "tasks"
)

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Pool(t)
	e := &env{t: t, pool: pool, dir: newDir(), gate: &fakeGate{off: map[string]bool{}}, runner: &fakeRunner{info: defaultInfo()}}
	svc, err := views.NewService(pool, e.dir, e.runner, e.gate, []views.Resource{
		{Key: "tickets", Module: moduleTickets, Group: "tickets"},
		{Key: "tasks", Module: moduleTasks, Group: "tasks", Use: []string{"tasks.view", "tasks.work"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM views.saved_views WHERE owner_user_id = ANY($1::text[]::uuid[])`, e.users)
		_, _ = pool.Exec(ctx, `DELETE FROM views.pins WHERE user_id = ANY($1::text[]::uuid[])`, e.users)
		_, _ = pool.Exec(ctx, `DELETE FROM views.sidebar_state WHERE user_id = ANY($1::text[]::uuid[])`, e.users)
	})
	return e
}

func (e *env) newUser() string {
	var id string
	if err := e.pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	e.users = append(e.users, id)
	e.dir.mu.Lock()
	e.dir.known[id] = true
	e.dir.mu.Unlock()
	return id
}

func (e *env) newID() string {
	var id string
	if err := e.pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// newTeam registers an active Team.
func (e *env) newTeam() string {
	id := e.newID()
	e.dir.mu.Lock()
	e.dir.live[id] = true
	e.dir.mu.Unlock()
	return id
}

// newRole creates a role row (cleaned up with the test) and returns its id.
func (e *env) newRole() string {
	ctx := context.Background()
	var id string
	key := fmt.Sprintf("vt-%s", e.newID()[:20])
	if err := e.pool.QueryRow(ctx, `INSERT INTO platform.roles (key, name) VALUES ($1, 'views test') RETURNING id::text`, key).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() {
		_, _ = e.pool.Exec(ctx, `DELETE FROM platform.role_assignments WHERE role_id = $1::uuid`, id)
		_, _ = e.pool.Exec(ctx, `DELETE FROM platform.roles WHERE id = $1::uuid`, id)
	})
	return id
}

func (e *env) assignRole(roleID, userID string) {
	_, err := e.pool.Exec(context.Background(), `INSERT INTO platform.role_assignments (role_id, subject_type, subject_id, created_by) VALUES ($1::uuid, 'user', $2::uuid, '{"actor":"cli"}')`,
		roleID, userID)
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) revokeRole(roleID, userID string) {
	_, err := e.pool.Exec(context.Background(), `UPDATE platform.role_assignments SET revoked_at = now(), revoked_by = '{"actor":"cli"}' WHERE role_id = $1::uuid AND subject_id = $2::uuid AND revoked_at IS NULL`,
		roleID, userID)
	if err != nil {
		e.t.Fatal(err)
	}
}

func caller(user string, perms ...string) views.Caller {
	m := map[string]struct{}{}
	for _, p := range perms {
		m[p] = struct{}{}
	}
	return views.Caller{UserID: user, Permissions: m, CorrelationID: "test-" + user[:8], Header: http.Header{"Cookie": {"sid=" + user}}}
}

func filterJSON(t *testing.T, raw string) *query.Filter {
	t.Helper()
	f, err := query.DecodeFilter([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// statusFilter is a simple valid filter.
func statusFilter(t *testing.T) views.Definition {
	return views.Definition{Filter: filterJSON(t, `{"v":1,"root":{"type":"condition","field":"status","op":"equals","value":"open"}}`)}
}

func (e *env) create(owner, name string, def views.Definition, perms ...string) views.ViewInfo {
	e.t.Helper()
	v, err := e.svc.Create(context.Background(), caller(owner, perms...), views.CreateInput{Resource: "tickets", Name: name, Definition: def})
	if err != nil {
		e.t.Fatalf("create %q: %v", name, err)
	}
	return v
}

func (e *env) share(owner string, v views.ViewInfo, perms []string, shares ...views.ShareInput) views.ViewInfo {
	e.t.Helper()
	out, err := e.svc.SetShares(context.Background(), caller(owner, perms...), v.ID, v.Version, shares)
	if err != nil {
		e.t.Fatalf("share: %v", err)
	}
	return out
}

var ctx = context.Background()
