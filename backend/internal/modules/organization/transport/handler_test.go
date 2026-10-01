package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

type fakeAuth struct {
	perms map[string]struct{}
	ok    bool
}

func (f fakeAuth) Authenticate(*http.Request) (authorization.Principal, bool, error) {
	return authorization.Principal{UserID: "u", Permissions: f.perms}, f.ok, nil
}

func with(perms ...string) fakeAuth {
	m := map[string]struct{}{}
	for _, p := range perms {
		m[p] = struct{}{}
	}
	return fakeAuth{perms: m, ok: true}
}

// fakeReader records the last inputs and returns configured results/error.
type fakeReader struct {
	err        error
	nextCursor string
	userF      application.UserFilter
	nameF      application.NameFilter
	page       application.Page
	id         string
	runF       application.RunFilter
	runs       []application.DirectorySyncRun
	identities []application.ExternalIdentity
}

var now = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func (f *fakeReader) ListUsers(_ context.Context, x application.UserFilter) (application.Result[application.User], error) {
	f.userF = x
	return application.Result[application.User]{NextCursor: f.nextCursor}, f.err
}
func (f *fakeReader) GetUser(_ context.Context, id string) (application.User, error) {
	f.id = id
	return application.User{ID: id, DisplayName: "Ada", Status: "active", UpdatedAt: now}, f.err
}
func (f *fakeReader) ListTeams(_ context.Context, x application.NameFilter) (application.Result[application.Team], error) {
	f.nameF = x
	return application.Result[application.Team]{Items: []application.Team{{ID: "t1", Name: "T", Active: true, UpdatedAt: now}}}, f.err
}
func (f *fakeReader) GetTeam(_ context.Context, id string) (application.Team, error) {
	return application.Team{ID: id}, f.err
}
func (f *fakeReader) ListTeamMembers(_ context.Context, id string, p application.Page) (application.Result[application.TeamMember], error) {
	f.id, f.page = id, p
	return application.Result[application.TeamMember]{}, f.err
}
func (f *fakeReader) ListLocations(_ context.Context, x application.NameFilter) (application.Result[application.Location], error) {
	f.nameF = x
	return application.Result[application.Location]{}, f.err
}
func (f *fakeReader) GetLocation(_ context.Context, id string) (application.Location, error) {
	return application.Location{ID: id}, f.err
}
func (f *fakeReader) ListDirectoryGroups(_ context.Context, x application.NameFilter) (application.Result[application.DirectoryGroup], error) {
	f.nameF = x
	return application.Result[application.DirectoryGroup]{}, f.err
}
func (f *fakeReader) GetDirectoryGroup(_ context.Context, id string) (application.DirectoryGroup, error) {
	return application.DirectoryGroup{ID: id, LastObservedAt: now}, f.err
}
func (f *fakeReader) ListDirectoryGroupMembers(_ context.Context, id string, p application.Page) (application.Result[application.DirectoryGroupMember], error) {
	f.id, f.page = id, p
	return application.Result[application.DirectoryGroupMember]{}, f.err
}

func (f *fakeReader) ListUserExternalIdentities(context.Context, string) ([]application.ExternalIdentity, error) {
	return f.identities, f.err
}
func (f *fakeReader) ListDirectorySyncRuns(_ context.Context, x application.RunFilter) (application.Result[application.DirectorySyncRun], error) {
	f.runF = x
	return application.Result[application.DirectorySyncRun]{Items: f.runs, NextCursor: f.nextCursor}, f.err
}
func (f *fakeReader) GetDirectorySyncRun(_ context.Context, id string) (application.DirectorySyncRun, error) {
	f.id = id
	return application.DirectorySyncRun{ID: id, ProviderKey: "ad", Trigger: "manual", StartedAt: now, Outcome: "running"}, f.err
}

// fakeSyncer records manual sync requests.
type fakeSyncer struct {
	calls   int
	actor   string
	key     string
	jobID   string
	created bool
	err     error
}

func (f *fakeSyncer) RequestDirectorySync(_ context.Context, actor, key string) (string, bool, error) {
	f.calls++
	f.actor, f.key = actor, key
	return f.jobID, f.created, f.err
}

func serve(t *testing.T, r application.Reader, a authorization.Authenticator, method, target string) (*httptest.ResponseRecorder, *bytes.Buffer) {
	t.Helper()
	return serveSync(t, r, &fakeSyncer{}, "ad", a, method, target)
}

func serveSync(t *testing.T, r application.Reader, s application.DirectorySyncRequester, providerKey string, a authorization.Authenticator, method, target string) (*httptest.ResponseRecorder, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	mux := http.NewServeMux()
	Register(mux, r, s, providerKey, a, slog.New(slog.NewTextHandler(logs, nil)))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, nil)
	rec.Header().Set("X-Request-ID", "req-1")
	mux.ServeHTTP(rec, req)
	return rec, logs
}

var orgRoutes = []string{"/api/v1/users", "/api/v1/users/x", "/api/v1/teams", "/api/v1/teams/x", "/api/v1/teams/x/members", "/api/v1/locations", "/api/v1/locations/x"}
var dirRoutes = []string{"/api/v1/directory-groups", "/api/v1/directory-groups/x", "/api/v1/directory-groups/x/members"}

func TestAuthorization(t *testing.T) {
	for _, route := range append(append([]string{}, orgRoutes...), dirRoutes...) {
		isDir := strings.Contains(route, "directory")
		own, other := "organization.view", "organization.directory.view"
		if isDir {
			own, other = other, own
		}
		t.Run(route, func(t *testing.T) {
			if rec, _ := serve(t, &fakeReader{}, authorization.DenyAll{}, "GET", route); rec.Code != 401 {
				t.Errorf("DenyAll = %d", rec.Code)
			}
			if rec, _ := serve(t, &fakeReader{}, with(), "GET", route); rec.Code != 403 {
				t.Errorf("no permission = %d", rec.Code)
			}
			if rec, _ := serve(t, &fakeReader{}, with(other), "GET", route); rec.Code != 403 {
				t.Errorf("other group permission = %d", rec.Code)
			}
			if strings.HasSuffix(route, "/members") && isDir {
				if rec, _ := serve(t, &fakeReader{}, with(own), "GET", route); rec.Code != 403 {
					t.Errorf("directory members with directory permission only = %d", rec.Code)
				}
				own = "organization.view,organization.directory.view"
			}
			rec, _ := serve(t, &fakeReader{}, with(strings.Split(own, ",")...), "GET", route)
			if rec.Code != 200 {
				t.Fatalf("allowed = %d %s", rec.Code, rec.Body)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestNonGETIs405(t *testing.T) {
	rec, _ := serve(t, &fakeReader{}, with("organization.view"), "POST", "/api/v1/users")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestItemsNeverNull(t *testing.T) {
	rec, _ := serve(t, &fakeReader{}, with("organization.view"), "GET", "/api/v1/users")
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if string(body["items"]) != "[]" {
		t.Errorf("items = %s", body["items"])
	}
	if _, present := body["nextCursor"]; present {
		t.Error("nextCursor should be omitted when empty")
	}
}

func TestNextCursorAndDTO(t *testing.T) {
	rec, _ := serve(t, &fakeReader{nextCursor: "abc"}, with("organization.view"), "GET", "/api/v1/users")
	if !strings.Contains(rec.Body.String(), `"nextCursor":"abc"`) {
		t.Errorf("body = %s", rec.Body)
	}
	rec, _ = serve(t, &fakeReader{}, with("organization.view"), "GET", "/api/v1/users/u1")
	for _, want := range []string{`"id":"u1"`, `"displayName":"Ada"`, `"updatedAt":"2026-01-02T03:04:05Z"`, `"givenName":null`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("missing %s in %s", want, rec.Body)
		}
	}
}

func TestPaginationParams(t *testing.T) {
	tests := []struct {
		query     string
		wantLimit int
	}{
		{"", application.DefaultLimit},
		{"?limit=10", 10},
		{"?limit=1000", application.MaxLimit},
	}
	for _, tt := range tests {
		fr := &fakeReader{}
		rec, _ := serve(t, fr, with("organization.view"), "GET", "/api/v1/users"+tt.query+"")
		if rec.Code != 200 || fr.userF.Limit != tt.wantLimit {
			t.Errorf("%q: code=%d limit=%d", tt.query, rec.Code, fr.userF.Limit)
		}
	}
	fr := &fakeReader{}
	serve(t, fr, with("organization.view"), "GET", "/api/v1/users?cursor=c1&q=ad&status=active")
	if fr.userF.Cursor != "c1" || fr.userF.Query != "ad" || fr.userF.Status != "active" {
		t.Errorf("filter = %+v", fr.userF)
	}
	fr = &fakeReader{}
	serve(t, fr, with("organization.view"), "GET", "/api/v1/teams/t9/members?limit=5&cursor=z")
	if fr.id != "t9" || fr.page.Limit != 5 || fr.page.Cursor != "z" {
		t.Errorf("members = %q %+v", fr.id, fr.page)
	}
	fr = &fakeReader{}
	serve(t, fr, with("organization.directory.view", "organization.view"), "GET", "/api/v1/directory-groups/g1/members?limit=500")
	if fr.id != "g1" || fr.page.Limit != application.MaxLimit {
		t.Errorf("dir members = %q %+v", fr.id, fr.page)
	}
}

func TestInvalidParams(t *testing.T) {
	long := strings.Repeat("a", 101)
	tests := []struct {
		target, perm, code string
	}{
		{"/api/v1/users?limit=abc", "organization.view", "organization.invalid_limit"},
		{"/api/v1/users?limit=0", "organization.view", "organization.invalid_limit"},
		{"/api/v1/teams?limit=-1", "organization.view", "organization.invalid_limit"},
		{"/api/v1/teams/x/members?limit=x", "organization.view", "organization.invalid_limit"},
		{"/api/v1/users?q=" + long, "organization.view", "organization.invalid_query"},
		{"/api/v1/locations?q=" + long, "organization.view", "organization.invalid_query"},
		{"/api/v1/directory-groups?q=" + long, "organization.directory.view", "organization.invalid_query"},
		{"/api/v1/users?status=bogus", "organization.view", "organization.invalid_status"},
	}
	for _, tt := range tests {
		rec, _ := serve(t, &fakeReader{}, with(tt.perm), "GET", tt.target)
		assertError(t, rec, 400, tt.code)
	}
	if rec, _ := serve(t, &fakeReader{}, with("organization.view"), "GET", "/api/v1/users?q="+strings.Repeat("ä", 100)); rec.Code != 200 {
		t.Errorf("100 multibyte runes = %d", rec.Code)
	}
	for _, s := range []string{"active", "inactive", "departed", "external", "unknown"} {
		if rec, _ := serve(t, &fakeReader{}, with("organization.view"), "GET", "/api/v1/users?status="+s); rec.Code != 200 {
			t.Errorf("status %s = %d", s, rec.Code)
		}
	}
}

func TestErrorMapping(t *testing.T) {
	rec, _ := serve(t, &fakeReader{err: application.ErrNotFound}, with("organization.view"), "GET", "/api/v1/users/x")
	assertError(t, rec, 404, "organization.not_found")
	rec, _ = serve(t, &fakeReader{err: application.ErrNotFound}, with("organization.directory.view", "organization.view"), "GET", "/api/v1/directory-groups/x/members")
	assertError(t, rec, 404, "organization.not_found")
	rec, _ = serve(t, &fakeReader{err: application.ErrInvalidCursor}, with("organization.view"), "GET", "/api/v1/users?cursor=bad")
	assertError(t, rec, 400, "organization.invalid_cursor")

	secret := "pq: password authentication failed for user secret"
	rec, logs := serve(t, &fakeReader{err: errors.New(secret)}, with("organization.view"), "GET", "/api/v1/teams")
	assertError(t, rec, 500, "platform.internal_error")
	if strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("error text leaked: %s", rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"requestId":"req-1"`) {
		t.Errorf("request id missing: %s", rec.Body)
	}
	if !strings.Contains(logs.String(), "password authentication failed") {
		t.Error("error not logged")
	}
}

func assertError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("status = %d, want %d (%s)", rec.Code, status, rec.Body)
		return
	}
	var env struct{ Error struct{ Code string } }
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Error.Code != code {
		t.Errorf("code = %q, want %q (%v)", env.Error.Code, code, err)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control missing on error")
	}
}
