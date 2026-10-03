package transport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/transport"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type fakeAuth struct {
	user  string
	perms map[string]struct{}
	ok    bool
}

func (f fakeAuth) Authenticate(*http.Request) (authorization.Principal, bool, error) {
	return authorization.Principal{UserID: f.user, Permissions: f.perms}, f.ok, nil
}

func as(user string, perms ...string) fakeAuth {
	m := map[string]struct{}{}
	for _, p := range perms {
		m[p] = struct{}{}
	}
	return fakeAuth{user: user, perms: m, ok: true}
}

type dir struct{}

func (dir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
func (dir) ActiveTeams(context.Context, []string) (map[string]bool, error) {
	return map[string]bool{}, nil
}
func (dir) UserNames(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (dir) TeamNames(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

type noDevice struct{}

func (noDevice) Snapshot(context.Context, string, string) (map[string]any, error) {
	return nil, application.ErrNotFound
}

const (
	alice = "00000000-0000-7000-8000-0000000000a1"
	bob   = "00000000-0000-7000-8000-0000000000b2"
	agent = "00000000-0000-7000-8000-0000000000c3"
)

func serve(t *testing.T, a authorization.Authenticator) http.Handler {
	t.Helper()
	pool := dbtest.Pool(t)
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	transport.Register(mux, application.NewService(repository.New(pool), dir{}, noDevice{}), a, logger)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM servicedesk.tickets WHERE reporter_user_id = ANY($1::uuid[])`, []string{alice, bob, agent})
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE actor_id = ANY($1::uuid[])`, []string{alice, bob, agent})
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE actor_id = ANY($1::uuid[])`, []string{alice, bob, agent})
	})
	return httpx.Middleware(logger, mux)
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestTicketsOverHTTP(t *testing.T) {
	employee := serve(t, as(alice))
	rec := do(employee, "POST", "/api/v1/tickets", `{"title":"Mouse broken","description":"Left click dead"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var tk struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &tk)
	if rec := do(employee, "POST", "/api/v1/tickets", `{"title":"x","priority":"urgent"}`); rec.Code != http.StatusForbidden {
		t.Errorf("employee sets a priority = %d", rec.Code)
	}
	if rec := do(employee, "GET", "/api/v1/tickets?scope=all", ""); rec.Code != http.StatusForbidden {
		t.Errorf("employee lists all = %d", rec.Code)
	}
	if rec := do(employee, "GET", "/api/v1/tickets", ""); !strings.Contains(rec.Body.String(), tk.ID) {
		t.Errorf("my tickets = %d %s", rec.Code, rec.Body)
	}
	if rec := do(employee, "POST", "/api/v1/tickets/"+tk.ID+"/assign", `{"assigneeId":"`+agent+`"}`); rec.Code != http.StatusForbidden {
		t.Errorf("employee assigns = %d", rec.Code)
	}
	if rec := do(employee, "POST", "/api/v1/tickets/"+tk.ID+"/comments", `{"body":"It is the left one"}`); rec.Code != http.StatusCreated {
		t.Errorf("comment = %d %s", rec.Code, rec.Body)
	}
	if rec := do(serve(t, as(bob)), "GET", "/api/v1/tickets/"+tk.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("stranger = %d, want 404", rec.Code)
	}
	if rec := do(serve(t, fakeAuth{}), "GET", "/api/v1/tickets", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d", rec.Code)
	}

	staff := serve(t, as(agent, "tickets.manage", "tickets.view"))
	if rec := do(staff, "POST", "/api/v1/tickets/"+tk.ID+"/assign", `{"assigneeId":"`+agent+`"}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"open"`) {
		t.Errorf("assign = %d %s", rec.Code, rec.Body)
	}
	if rec := do(staff, "POST", "/api/v1/tickets/"+tk.ID+"/resolve", `{"reason":"New mouse handed over"}`); rec.Code != http.StatusOK {
		t.Errorf("resolve = %d %s", rec.Code, rec.Body)
	}
	if rec := do(staff, "POST", "/api/v1/tickets/"+tk.ID+"/resolve", `{"reason":"again"}`); rec.Code != http.StatusConflict {
		t.Errorf("resolve twice = %d", rec.Code)
	}
	if rec := do(staff, "GET", "/api/v1/tickets/"+tk.ID, ""); !strings.Contains(rec.Body.String(), `"allowedOperations"`) {
		t.Errorf("detail = %s", rec.Body)
	}
	if rec := do(staff, "GET", "/api/v1/tickets?scope=all&status=weird", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown status = %d", rec.Code)
	}
	if rec := do(staff, "GET", "/api/v1/tickets/not-a-uuid", ""); rec.Code != http.StatusNotFound {
		t.Errorf("malformed id = %d", rec.Code)
	}
}
