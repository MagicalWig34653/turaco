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
	"github.com/MagicalWig34653/turaco/backend/internal/platform/health"
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

func setup(t *testing.T, a authorization.Authenticator) (*http.ServeMux, string) {
	t.Helper()
	pool := dbtest.Pool(t)
	var uid string
	if err := pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	reg := health.NewRegistry()
	_ = reg.Register(health.Check{Key: "smtp", Category: health.CategoryIntegration, Run: func(context.Context) health.Result {
		return health.Result{Status: health.StatusNotConfigured, NextStep: &health.NextStep{Kind: "config", ConfigKeys: []string{"SMTP_HOST"}}}
	}})
	_ = reg.Register(health.Check{Key: "database", Category: health.CategorySystem, Run: func(context.Context) health.Result {
		return health.Result{Status: health.StatusOK, Detail: map[string]any{"serverVersion": "18.1"}}
	}})
	derived := false
	items := []health.ItemDef{
		{Key: "zz_teams", Order: 1, Route: "/admin/teams", Derive: func(context.Context) (bool, bool, error) { return derived, false, nil }},
		{Key: "zz_modules", Order: 2, Route: "/admin/modules", Confirmable: true},
	}
	_ = derived
	su := health.NewSetup(pool, items, nil)
	mux := http.NewServeMux()
	Register(mux, reg, su, SystemInfo{Version: "1.2.3", Environment: "development", StartedAt: time.Now()}, a, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform.setup_items WHERE key LIKE 'zz_%'`)
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform.audit_events WHERE target_type = 'setup_item' AND target_id LIKE 'zz_%'`)
	})
	return mux, uid
}

func do(mux *http.ServeMux, method, target, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
	return rec
}

func TestHealthPagesNeedPermissionAndHideNothingSecret(t *testing.T) {
	mux, uid := setup(t, principal("x"))
	for _, p := range []string{"/api/v1/admin/health", "/api/v1/admin/integrations", "/api/v1/admin/system", "/api/v1/admin/setup"} {
		if rec := do(mux, "GET", p, ""); rec.Code != 403 {
			t.Errorf("%s without permission: %d", p, rec.Code)
		}
	}
	mux, uid = setup(t, principal(uid, "platform.health.view"))
	rec := do(mux, "GET", "/api/v1/admin/integrations", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"configKeys":["SMTP_HOST"]`) || strings.Contains(rec.Body.String(), `"database"`) {
		t.Fatalf("integrations: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(mux, "GET", "/api/v1/admin/system", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"version":"1.2.3"`) || !strings.Contains(rec.Body.String(), `"serverVersion":"18.1"`) {
		t.Fatalf("system: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(mux, "GET", "/api/v1/admin/health", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"attention":1`) {
		t.Fatalf("health: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSetupChecklistSkipConfirmAndAuthorization(t *testing.T) {
	mux, uid := setup(t, principal("x"))
	viewer := principal(uid, "platform.health.view")
	mux, uid = setup(t, viewer)
	rec := do(mux, "GET", "/api/v1/admin/setup", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"open":2`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	// A viewer may read but not change.
	if rec := do(mux, "PUT", "/api/v1/admin/setup/items/zz_teams", `{"state":"skipped","reason":"local only"}`); rec.Code != 403 {
		t.Fatalf("viewer put: %d %s", rec.Code, rec.Body.String())
	}
	mux, uid = setup(t, principal(uid, "platform.health.view", "platform.admin"))
	if rec := do(mux, "PUT", "/api/v1/admin/setup/items/nope", `{"state":"skipped","reason":"x"}`); rec.Code != 404 {
		t.Fatalf("unknown: %d", rec.Code)
	}
	if rec := do(mux, "PUT", "/api/v1/admin/setup/items/zz_teams", `{"state":"skipped"}`); rec.Code != 400 {
		t.Fatalf("skip needs a reason: %d", rec.Code)
	}
	if rec := do(mux, "PUT", "/api/v1/admin/setup/items/zz_teams", `{"state":"confirmed"}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), "health.not_confirmable") {
		t.Fatalf("derived items cannot be confirmed: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(mux, "PUT", "/api/v1/admin/setup/items/zz_teams", `{"state":"skipped","reason":"local only","bogus":1}`); rec.Code != 400 {
		t.Fatalf("unknown fields: %d", rec.Code)
	}
	rec = do(mux, "PUT", "/api/v1/admin/setup/items/zz_teams", `{"state":"skipped","reason":"local only"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"state":"skipped"`) || !strings.Contains(rec.Body.String(), `"version":1`) {
		t.Fatalf("skip: %d %s", rec.Code, rec.Body.String())
	}
	// Changing an existing row needs the current version.
	if rec := do(mux, "PUT", "/api/v1/admin/setup/items/zz_teams", `{"state":"cleared"}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), "version_conflict") {
		t.Fatalf("no version: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(mux, "PUT", "/api/v1/admin/setup/items/zz_teams", `{"state":"cleared","expectedVersion":1}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"state":"todo"`) {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(mux, "PUT", "/api/v1/admin/setup/items/zz_modules", `{"state":"confirmed"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"state":"confirmed"`) {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
	}
}
