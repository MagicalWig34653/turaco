package wiring

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	servicedeskrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
	servicedesktransport "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/transport"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/views"
)

// cookieAuth authenticates the in-process follow-up call exactly like a session would: from the cookie of the
// original request. "uid=<user>; perms=a|b".
type cookieAuth struct{}

func (cookieAuth) Authenticate(r *http.Request) (authorization.Principal, bool, error) {
	p := authorization.Principal{Permissions: map[string]struct{}{}}
	for _, part := range strings.Split(r.Header.Get("Cookie"), ";") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "uid":
			p.UserID = v
		case "perms":
			for _, perm := range strings.Split(v, "|") {
				if perm != "" {
					p.Permissions[perm] = struct{}{}
				}
			}
		}
	}
	return p, p.UserID != "", nil
}

type openDirectory struct{}

func (openDirectory) CurrentTeamIDs(context.Context, string) ([]string, error) { return nil, nil }
func (openDirectory) GroupIDsOfUser(context.Context, string) ([]string, error) { return nil, nil }
func (openDirectory) ActiveTeams(context.Context, []string) (map[string]bool, error) {
	return map[string]bool{}, nil
}
func (openDirectory) UserNames(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (openDirectory) TeamNames(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (openDirectory) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

type allOn struct{}

func (allOn) Enabled(context.Context, string) (bool, error) { return true, nil }

type noDevice struct{}

func (noDevice) Snapshot(context.Context, string, string) (map[string]any, error) {
	return nil, servicedeskapp.ErrNotFound
}

func cookieFor(user string, perms ...string) http.Header {
	return http.Header{"Cookie": {"uid=" + user + "; perms=" + strings.Join(perms, "|")}}
}

// A shared View runs through the module's own query endpoint as the VIEWER: the viewer's row scope applies (an
// employee sees only their own tickets even in a View a colleague with wider access shared), and a condition on a
// field the viewer may not use evaluates to "no rows" with a warning.
func TestSharedViewRunsWithTheViewersOwnScopeAndFields(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	const (
		alice = "00000000-0000-7000-8000-0000000007a1" // employee
		bob   = "00000000-0000-7000-8000-0000000007b2" // employee
		agent = "00000000-0000-7000-8000-0000000007c3" // tickets.view
	)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	deskSvc := servicedeskapp.NewService(servicedeskrepository.New(pool), openDirectory{}, noDevice{}).WithQueryEngine(QueryEngine(pool))
	servicedesktransport.Register(mux, deskSvc, cookieAuth{}, logger)
	resources, routes := ViewResources()
	svc, err := views.NewService(pool, openDirectory{}, views.NewHTTPRunner(mux, routes), allOn{}, resources)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM views.saved_views WHERE owner_user_id = ANY($1::uuid[])`, []string{alice, bob, agent})
		_, _ = pool.Exec(ctx, `DELETE FROM servicedesk.tickets WHERE reporter_user_id = ANY($1::uuid[])`, []string{alice, bob, agent})
	})
	createTicket := func(user, title string) {
		req := httptest.NewRequest("POST", "/api/v1/tickets", bytes.NewBufferString(`{"title":"`+title+`"}`))
		req.Header = cookieFor(user)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		httpx.Middleware(logger, mux).ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create ticket: %d %s", rec.Code, rec.Body)
		}
	}
	createTicket(alice, "wiringprinter alice")
	createTicket(bob, "wiringprinter bob")
	createTicket(bob, "unrelated bob")

	caller := func(user string, perms ...string) views.Caller {
		m := map[string]struct{}{}
		for _, p := range perms {
			m[p] = struct{}{}
		}
		return views.Caller{UserID: user, Permissions: m, CorrelationID: "wiring-" + user[len(user)-4:], Header: cookieFor(user, perms...)}
	}
	titles := func(out views.ResultsOutput) []string {
		var items []struct{ Title string }
		if err := json.Unmarshal(out.Items, &items); err != nil {
			t.Fatalf("items: %v %s", err, out.Items)
		}
		var got []string
		for _, it := range items {
			got = append(got, it.Title)
		}
		return got
	}

	// The agent (wide scope) saves "wiringprinter" tickets and shares it with the employees.
	def := views.Definition{Filter: &query.Filter{V: 1, Root: &query.Node{Type: "condition", Field: "title", Op: "contains", Value: json.RawMessage(`"wiringprinter"`)}}}
	agentC := caller(agent, "tickets.view")
	v, err := svc.Create(ctx, agentC, views.CreateInput{Resource: "tickets", Name: "Printers", Definition: def})
	if err != nil {
		t.Fatalf("create view: %v", err)
	}
	v, err = svc.SetShares(ctx, caller(agent, "tickets.view", views.PermShare), v.ID, v.Version, []views.ShareInput{
		{SubjectType: "user", SubjectID: bob, Level: "use"}, {SubjectType: "user", SubjectID: alice, Level: "use"}})
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	all, err := svc.Results(ctx, agentC, v.ID, views.ResultsInput{Count: true})
	if err != nil || len(titles(all)) < 2 {
		t.Fatalf("agent results: %v %v", err, titles(all))
	}
	got, err := svc.Results(ctx, caller(bob), v.ID, views.ResultsInput{})
	if err != nil {
		t.Fatalf("bob results: %v", err)
	}
	if len(titles(got)) != 1 || titles(got)[0] != "wiringprinter bob" {
		t.Errorf("bob must see only his own matching ticket, got %v", titles(got))
	}

	// The agent adds a condition on a staff-only field (queue). The employees' catalog does not offer it, so the
	// condition cannot be used by them: it evaluates to no rows (never dropped) and is reported.
	queueDef := views.Definition{Filter: &query.Filter{V: 1, Root: &query.Node{Type: "group", Logic: "and", Children: []query.Node{
		{Type: "condition", Field: "title", Op: "contains", Value: json.RawMessage(`"wiringprinter"`)},
		{Type: "condition", Field: "queue", Op: "is_empty"}}}}}
	qv, err := svc.Update(ctx, agentC, v.ID, views.UpdateInput{ExpectedVersion: v.Version, Definition: &queueDef})
	if err != nil {
		t.Fatalf("update with queue condition: %v", err)
	}
	staff, err := svc.Results(ctx, agentC, qv.ID, views.ResultsInput{})
	if err != nil || len(titles(staff)) < 2 {
		t.Fatalf("agent results with queue condition: %v %v", err, titles(staff))
	}
	emp, err := svc.Results(ctx, caller(bob), qv.ID, views.ResultsInput{})
	if err != nil {
		t.Fatalf("bob results: %v", err)
	}
	if len(titles(emp)) != 0 || len(emp.Warnings) == 0 || emp.Warnings[0].Code != "query.field_unavailable" {
		t.Errorf("an unusable staff-only condition must give no rows and a warning, got %v %+v", titles(emp), emp.Warnings)
	}

	// The employee cannot save a View with that field in the first place: the module's own validation rules.
	_, err = svc.Create(ctx, caller(alice), views.CreateInput{Resource: "tickets", Name: "Sneaky", Definition: queueDef})
	var re *views.RunError
	if !errors.As(err, &re) || re.Code != "query.invalid_filter" {
		t.Errorf("an employee saved a view over a staff-only field: %v", err)
	}
	// A task View needs the tasks permission; a signed-in employee has none.
	_, err = svc.Create(ctx, caller(alice), views.CreateInput{Resource: "tasks", Name: "Tasks"})
	if !errors.Is(err, views.ErrForbidden) {
		t.Errorf("tasks view without permission: %v", err)
	}
}

// The registered resources must match the catalogs and the permissions their routes require.
func TestViewResourcesMatchTheModules(t *testing.T) {
	resources, routes := ViewResources()
	if len(resources) != 3 || len(routes) != 3 {
		t.Fatalf("resources %d routes %d", len(resources), len(routes))
	}
	for _, r := range resources {
		rt, ok := routes[r.Key]
		if !ok || !strings.HasPrefix(rt.QueryPath, "/api/v1/") || !strings.HasSuffix(rt.QueryPath, "/query") || !strings.HasSuffix(rt.FieldsPath, "/fields") {
			t.Errorf("resource %s has no valid route: %+v", r.Key, rt)
		}
	}
}
