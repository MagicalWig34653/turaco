package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

type fakeTeamStore struct {
	err    error
	caller application.Caller
	name   string
	calls  int
}

func (f *fakeTeamStore) CreateTeam(_ context.Context, c application.Caller, name string) (application.Team, error) {
	f.calls++
	f.caller, f.name = c, name
	return application.Team{ID: "t1", Name: name, Active: true, UpdatedAt: now}, f.err
}
func (f *fakeTeamStore) RenameTeam(_ context.Context, c application.Caller, id, name string) (application.Team, error) {
	f.calls++
	f.caller, f.name = c, name
	return application.Team{ID: id, Name: name, Active: true, UpdatedAt: now}, f.err
}
func (f *fakeTeamStore) SetTeamActive(_ context.Context, _ application.Caller, id string, active bool) (application.Team, error) {
	f.calls++
	return application.Team{ID: id, Active: active, UpdatedAt: now}, f.err
}
func (f *fakeTeamStore) AddTeamMember(_ context.Context, _ application.Caller, _, userID string, _ *string) (application.TeamMember, error) {
	f.calls++
	return application.TeamMember{UserID: userID, Source: "platform", ValidFrom: now}, f.err
}
func (f *fakeTeamStore) RemoveTeamMember(context.Context, application.Caller, string, string) error {
	f.calls++
	return f.err
}

func serveTeams(t *testing.T, store *fakeTeamStore, a authorization.Authenticator, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	RegisterTeams(mux, application.NewTeams(store), a, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-ID", "req-1")
	var rd io.Reader
	if body != "" {
		rd = bytes.NewBufferString(body)
	}
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, rd))
	return rec
}

var teamWrites = []struct{ method, path, body string }{
	{"POST", "/api/v1/teams", `{"name":"x"}`},
	{"PATCH", "/api/v1/teams/t1", `{"name":"x"}`},
	{"POST", "/api/v1/teams/t1/activate", ``},
	{"POST", "/api/v1/teams/t1/deactivate", ``},
	{"POST", "/api/v1/teams/t1/members", `{"userId":"u1"}`},
	{"DELETE", "/api/v1/teams/t1/members/u1", ``},
}

func TestTeamWritesRequireManagePermission(t *testing.T) {
	for _, w := range teamWrites {
		t.Run(w.method+" "+w.path, func(t *testing.T) {
			store := &fakeTeamStore{}
			if rec := serveTeams(t, store, authorization.DenyAll{}, w.method, w.path, w.body); rec.Code != 401 {
				t.Errorf("unauthenticated = %d, want 401", rec.Code)
			}
			// organization.view alone must not allow writes.
			if rec := serveTeams(t, store, with("organization.view", "tasks.manage"), w.method, w.path, w.body); rec.Code != 403 {
				t.Errorf("without organization.teams.manage = %d, want 403", rec.Code)
			}
			if store.calls != 0 {
				t.Errorf("store called %d times without permission", store.calls)
			}
			if rec := serveTeams(t, store, with("organization.teams.manage"), w.method, w.path, w.body); rec.Code >= 400 {
				t.Errorf("with permission = %d, want success: %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestCreateTeamUsesPrincipalAndRequestID(t *testing.T) {
	store := &fakeTeamStore{}
	rec := serveTeams(t, store, with("organization.teams.manage"), "POST", "/api/v1/teams", `{"name":"  Network "}`)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"name":"Network"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if store.caller.Actor.UserID != "u" || store.caller.CorrelationID != "req-1" {
		t.Errorf("caller = %+v, want principal u and request id req-1", store.caller)
	}
}

func TestTeamWriteErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{application.ErrNotFound, 404, "organization.not_found"},
		{application.ErrConflict, 409, "organization.conflict"},
		{application.ErrUserNotActive, 409, "organization.user_not_active"},
		{application.ErrTeamInactive, 409, "organization.team_inactive"},
		{errors.New("secret database detail"), 500, "platform.internal_error"},
	}
	for _, c := range cases {
		rec := serveTeams(t, &fakeTeamStore{err: c.err}, with("organization.teams.manage"), "POST", "/api/v1/teams", `{"name":"x"}`)
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.code) {
			t.Errorf("%v: status=%d body=%s, want %d %s", c.err, rec.Code, rec.Body, c.status, c.code)
		}
		if strings.Contains(rec.Body.String(), "secret database detail") {
			t.Errorf("internal error text leaked: %s", rec.Body)
		}
	}
}

func TestTeamWriteBodyValidation(t *testing.T) {
	for name, body := range map[string]string{
		"not json": `x`, "unknown field": `{"name":"a","admin":true}`, "trailing data": `{"name":"a"}{}`,
		"empty name": `{"name":""}`, "oversized": `{"name":"` + strings.Repeat("a", 5000) + `"}`,
	} {
		store := &fakeTeamStore{}
		rec := serveTeams(t, store, with("organization.teams.manage"), "POST", "/api/v1/teams", body)
		if rec.Code != 400 || store.calls != 0 {
			t.Errorf("%s: status=%d store calls=%d, want 400 and none", name, rec.Code, store.calls)
		}
	}
}
