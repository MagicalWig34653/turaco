package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/briefing/application"
	planningpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/planning/public"
	securitypublic "github.com/MagicalWig34653/turaco/backend/internal/modules/security/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

type fakeAuth struct {
	perms map[string]struct{}
	ok    bool
}

func (f fakeAuth) Authenticate(*http.Request) (authorization.Principal, bool, error) {
	return authorization.Principal{UserID: "00000000-0000-7000-8000-0000000000a1", Permissions: f.perms}, f.ok, nil
}

func with(perms ...string) fakeAuth {
	m := map[string]struct{}{}
	for _, p := range perms {
		m[p] = struct{}{}
	}
	return fakeAuth{perms: m, ok: true}
}

type stub struct {
	item  application.Item
	calls int
	query application.ListQuery
}

func (s *stub) Insert(_ context.Context, _ application.Caller, n application.NewItem) (application.Item, error) {
	s.calls++
	it := s.item
	it.Title = n.Title
	return it, nil
}
func (s *stub) Get(context.Context, string) (application.Item, error) { return s.item, nil }
func (s *stub) Change(_ context.Context, _ application.Caller, _ string, decide func(application.Item) (application.Change, error)) (application.Item, error) {
	s.calls++
	ch, err := decide(s.item)
	if err != nil {
		return application.Item{}, err
	}
	return ch.Next, nil
}
func (s *stub) Delete(_ context.Context, _ application.Caller, _ string, decide func(application.Item) error) error {
	s.calls++
	return decide(s.item)
}
func (s *stub) List(_ context.Context, q application.ListQuery) (application.Result, error) {
	s.query = q
	return application.Result{Items: []application.Item{s.item}}, nil
}

func newStub() *stub {
	return &stub{item: application.Item{
		ID: "00000000-0000-7000-8000-000000000001", Title: "Maintenance", Severity: "info", Status: "draft", Version: 1,
	}}
}

func TestFeedRouteAuthorizationAndShape(t *testing.T) {
	store := newStub()
	store.item.Status = application.StatusPublished
	for _, tc := range []struct {
		auth        fakeAuth
		status      int
		wantEntries int
	}{
		{fakeAuth{ok: false}, http.StatusUnauthorized, 0},
		{with(), http.StatusForbidden, 0},
		{with("briefing.view"), http.StatusOK, 1},
		{with("security.view"), http.StatusOK, 0},
	} {
		mux := http.NewServeMux()
		svc := application.NewService(store, nil)
		feed := application.NewFeedService(svc, application.FeedSources{})
		Register(mux, svc, tc.auth, slog.New(slog.NewTextHandler(io.Discard, nil)), feed)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/briefing/feed", nil))
		if rec.Code != tc.status {
			t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
		}
		if tc.status == http.StatusOK {
			var body struct {
				Entries     []application.FeedEntry   `json:"entries"`
				Truncated   map[string]bool           `json:"truncated"`
				Unavailable []application.FeedFailure `json:"unavailable"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Entries) != tc.wantEntries {
				t.Fatalf("entries = %d, want %d", len(body.Entries), tc.wantEntries)
			}
		}
	}
}

func serve(t *testing.T, s *stub, a authorization.Authenticator, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, application.NewService(s, func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }), a, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-ID", "req-1")
	var rd io.Reader
	if body != "" {
		rd = bytes.NewBufferString(body)
	}
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, rd))
	return rec
}

const itemPath = "/api/v1/briefing-items/00000000-0000-7000-8000-000000000001"

func TestRoutePermissions(t *testing.T) {
	for _, r := range []struct {
		method, path, body string
		managerOnly        bool
	}{
		{"GET", "/api/v1/briefing-items", "", false},
		{"GET", itemPath, "", false},
		{"POST", "/api/v1/briefing-items", `{"title":"x"}`, true},
		{"PATCH", itemPath, `{"expectedVersion":1,"title":"x"}`, true},
		{"DELETE", itemPath, "", true},
		{"POST", itemPath + "/publish", `{}`, true},
		{"POST", itemPath + "/withdraw", `{}`, true},
	} {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			if rec := serve(t, newStub(), authorization.DenyAll{}, r.method, r.path, r.body); rec.Code != 401 {
				t.Errorf("unauthenticated = %d", rec.Code)
			}
			if rec := serve(t, newStub(), with("tasks.manage", "organization.view"), r.method, r.path, r.body); rec.Code != 403 {
				t.Errorf("unrelated permissions = %d, want 403", rec.Code)
			}
			if r.managerOnly {
				s := newStub()
				if rec := serve(t, s, with("briefing.view"), r.method, r.path, r.body); rec.Code != 403 || s.calls != 0 {
					t.Errorf("briefing.view on a manager route = %d (store calls %d)", rec.Code, s.calls)
				}
			}
			if rec := serve(t, newStub(), with("briefing.manage"), r.method, r.path, r.body); rec.Code == 401 || rec.Code == 403 || rec.Code >= 500 {
				t.Errorf("briefing.manage = %d: %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestViewersSeeOnlyPublishedItems(t *testing.T) {
	s := newStub() // a draft
	if rec := serve(t, s, with("briefing.view"), "GET", itemPath, ""); rec.Code != 404 {
		t.Errorf("viewer on a draft = %d, want 404", rec.Code)
	}
	if rec := serve(t, s, with("briefing.manage"), "GET", itemPath, ""); rec.Code != 200 {
		t.Errorf("manager on a draft = %d, want 200", rec.Code)
	}
	if rec := serve(t, s, with("briefing.view"), "GET", "/api/v1/briefing-items?status=draft", ""); rec.Code != 200 || !s.query.PublishedOnly {
		t.Errorf("viewer list = %d, PublishedOnly = %v", rec.Code, s.query.PublishedOnly)
	}
	if rec := serve(t, s, with("briefing.manage"), "GET", "/api/v1/briefing-items?status=draft", ""); rec.Code != 200 || s.query.PublishedOnly || s.query.Status != "draft" {
		t.Errorf("manager list: PublishedOnly = %v, status = %q", s.query.PublishedOnly, s.query.Status)
	}
}

func TestPatchRequiresTheExpectedVersion(t *testing.T) {
	s := newStub()
	rec := serve(t, s, with("briefing.manage"), "PATCH", itemPath, `{"title":"x"}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "expectedVersion") || s.calls != 0 {
		t.Errorf("status=%d calls=%d body=%s", rec.Code, s.calls, rec.Body)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := map[string]struct {
		method, path, body string
		mutate             func(*stub)
		status             int
		code               string
	}{
		"publish a published item": {"POST", itemPath + "/publish", `{}`, func(s *stub) { s.item.Status = "published" }, 409, "briefing.invalid_transition"},
		"stale version":            {"POST", itemPath + "/publish", `{"expectedVersion":9}`, nil, 409, "briefing.version_conflict"},
		"invalid severity":         {"POST", "/api/v1/briefing-items", `{"title":"x","severity":"fatal"}`, nil, 400, "briefing.invalid_request"},
		"unknown field":            {"POST", "/api/v1/briefing-items", `{"title":"x","status":"published"}`, nil, 400, "briefing.invalid_request"},
		"bad limit":                {"GET", "/api/v1/briefing-items?limit=0", ``, nil, 400, "briefing.invalid_limit"},
		"unknown status":           {"GET", "/api/v1/briefing-items?status=archived", ``, nil, 400, "briefing.invalid_request"},
		"bad expected version":     {"DELETE", itemPath + "?expectedVersion=x", ``, nil, 400, "briefing.invalid_request"},
		"delete a published item":  {"DELETE", itemPath, ``, func(s *stub) { s.item.Status = "published" }, 409, "briefing.invalid_transition"},
	}
	for name, c := range cases {
		s := newStub()
		if c.mutate != nil {
			c.mutate(s)
		}
		rec := serve(t, s, with("briefing.manage"), c.method, c.path, c.body)
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.code) {
			t.Errorf("%s: status=%d body=%s, want %d %s", name, rec.Code, rec.Body, c.status, c.code)
		}
	}
}

func TestResponsesAreNotCached(t *testing.T) {
	rec := serve(t, newStub(), with("briefing.view"), "GET", "/api/v1/briefing-items", "")
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
}

type permissionSecurity struct{}

func (permissionSecurity) ApplicableAdvisorySummaries(context.Context, securitypublic.ReadScope) ([]securitypublic.ApplicableSummary, error) {
	return []securitypublic.ApplicableSummary{{ID: "adv", Reference: "ADV-1", Title: "secret advisory", Severity: "critical"}}, nil
}
func (permissionSecurity) AdvisoryFeedHealth(context.Context) ([]securitypublic.FeedHealth, error) {
	return nil, nil
}
func (permissionSecurity) RiskReviewsDuePage(context.Context, securitypublic.ReadScope) (securitypublic.RiskReviewPage, error) {
	return securitypublic.RiskReviewPage{}, nil
}

type permissionPlanning struct{ now time.Time }

func (p permissionPlanning) UpcomingMaintenance(context.Context, time.Time, time.Time, planningpublic.MaintenanceScope) ([]planningpublic.UpcomingMaintenance, bool, error) {
	return []planningpublic.UpcomingMaintenance{{ChangeID: "chg", Reference: "CHG-1", Title: "secret change", WindowStart: p.now.Add(time.Hour)}}, false, nil
}
func (permissionPlanning) DueMilestones(context.Context, time.Time, time.Time, planningpublic.DueMilestoneScope) ([]planningpublic.DueMilestone, error) {
	return nil, nil
}

func TestFeedPermissionTableExactFlagsAndRedaction(t *testing.T) {
	now := time.Now()
	expected := map[string]application.FeedPrincipal{
		"briefing.view": {Briefing: true}, "briefing.manage": {Briefing: true},
		"security.view": {Security: true},
		"planning.view": {Planning: true}, "planning.manage": {Planning: true},
		"changes.view": {Changes: true}, "changes.manage": {Changes: true}, "changes.execute": {Changes: true},
		"tickets.view": {Desk: true, Tickets: true}, "tickets.manage": {Desk: true, Tickets: true, Autotask: true},
		"majorincidents.manage": {Desk: true},
		"endpoints.manage":      {Endpoints: true, Directory: true}, "integrations.intune.manage": {Endpoints: true, Directory: true},
		"deployments.view": {Deployments: true}, "deployments.manage": {Deployments: true}, "deployments.execute": {Deployments: true},
		"organization.directory.sync": {Directory: true},
	}
	if len(feedPermissions()) != len(expected) {
		t.Fatalf("permissions = %v", feedPermissions())
	}
	for _, permission := range feedPermissions() {
		t.Run(permission, func(t *testing.T) {
			want, ok := expected[permission]
			if !ok {
				t.Fatalf("unexpected permission %s", permission)
			}
			want.UserID = "user"
			got := feedPrincipal("user", func(v string) bool { return v == permission })
			if got != want {
				t.Fatalf("flags = %+v, want %+v", got, want)
			}
			feed := application.NewFeedService(nil, application.FeedSources{Security: permissionSecurity{}, Planning: permissionPlanning{now: now}})
			result, err := feed.Feed(context.Background(), got, now)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range result.Entries {
				if entry.Kind == "security_advisory" && !got.Security {
					t.Fatal("security entry leaked")
				}
				if entry.Kind == "maintenance" {
					_, hasTitle := entry.Params["title"]
					if hasTitle != got.Changes {
						t.Fatalf("change title visibility = %v", hasTitle)
					}
				}
				if strings.Contains(fmt.Sprint(entry.Params), "secret advisory") && !got.Security {
					t.Fatal("advisory title leaked")
				}
			}
		})
	}
}
