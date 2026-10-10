package transport

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/settings"
)

type auth struct{ p authorization.Principal }

func (a auth) Authenticate(*http.Request) (authorization.Principal, bool, error) {
	return a.p, true, nil
}

func principal(uid string, perms ...string) auth {
	m := map[string]struct{}{}
	for _, p := range perms {
		m[p] = struct{}{}
	}
	return auth{authorization.Principal{UserID: uid, Permissions: m}}
}

func setup(t *testing.T, a func(uid string) authorization.Authenticator) *http.ServeMux {
	t.Helper()
	pool := dbtest.Pool(t)
	var uid string
	if err := pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	clean := func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform.settings`)
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform.audit_events WHERE target_type = 'setting' AND actor_id = $1::uuid`, uid)
	}
	clean()
	t.Cleanup(clean)
	mux := http.NewServeMux()
	Register(mux, settings.New(pool, time.Millisecond), a(uid), slog.New(slog.NewTextHandler(io.Discard, nil)))
	return mux
}

func do(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestPermissions(t *testing.T) {
	viewer := setup(t, func(uid string) authorization.Authenticator { return principal(uid, "platform.health.view") })
	if rec := do(viewer, "GET", "/api/v1/admin/settings", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "auth.session_absolute_timeout") {
		t.Fatalf("viewer get: %d %s", rec.Code, rec.Body)
	}
	if rec := do(viewer, "PUT", "/api/v1/admin/settings/teams.personal_default", `{"value":true,"expectedVersion":0}`); rec.Code != 403 {
		t.Fatalf("viewer put: %d", rec.Code)
	}
	none := setup(t, func(uid string) authorization.Authenticator { return principal(uid) })
	if rec := do(none, "GET", "/api/v1/admin/settings", ""); rec.Code != 403 {
		t.Fatalf("no permission get: %d", rec.Code)
	}
}

func TestPut(t *testing.T) {
	admin := setup(t, func(uid string) authorization.Authenticator {
		return principal(uid, "platform.health.view", "platform.admin")
	})
	path := "/api/v1/admin/settings/teams.personal_default"
	if rec := do(admin, "PUT", path, `{"value":true}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "settings.version_required") {
		t.Fatalf("missing version: %d %s", rec.Code, rec.Body)
	}
	if rec := do(admin, "PUT", path, `{"value":"yes","expectedVersion":0}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "settings.invalid_value") {
		t.Fatalf("bad value: %d %s", rec.Code, rec.Body)
	}
	if rec := do(admin, "PUT", "/api/v1/admin/settings/nope", `{"value":true,"expectedVersion":0}`); rec.Code != 404 {
		t.Fatalf("unknown: %d", rec.Code)
	}
	if rec := do(admin, "PUT", path, `{"value":true,"expectedVersion":0}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"version":1`) {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	if rec := do(admin, "PUT", path, `{"value":false,"expectedVersion":0}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), "settings.version_conflict") {
		t.Fatalf("conflict: %d %s", rec.Code, rec.Body)
	}
	if rec := do(admin, "GET", "/api/v1/admin/settings", ""); !strings.Contains(rec.Body.String(), `"stored":true`) {
		t.Fatalf("list after put: %s", rec.Body)
	}
}
