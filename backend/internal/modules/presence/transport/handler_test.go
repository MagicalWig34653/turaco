package transport_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/presence/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/presence/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/presence/transport"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

type fakeAuth struct {
	user  string
	perms map[string]struct{}
}

func (f fakeAuth) Authenticate(*http.Request) (authorization.Principal, bool, error) {
	return authorization.Principal{UserID: f.user, Permissions: f.perms}, f.user != "", nil
}

func as(user string, perms ...string) fakeAuth {
	m := map[string]struct{}{}
	for _, p := range perms {
		m[p] = struct{}{}
	}
	return fakeAuth{user: user, perms: m}
}

type dir struct{ members []string }

func (d dir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
func (dir) ActiveTeams(context.Context, []string) (map[string]bool, error)     { return nil, nil }
func (dir) ActiveLocations(context.Context, []string) (map[string]bool, error) { return nil, nil }
func (d dir) CurrentTeamIDs(context.Context, string) ([]string, error)         { return []string{newID()}, nil }
func (d dir) CurrentMemberIDs(context.Context, string) ([]string, error)       { return d.members, nil }

func (d dir) MembershipIntervals(context.Context, string, time.Time, time.Time) ([]application.MembershipInterval, error) {
	var out []application.MembershipInterval
	for _, m := range d.members {
		out = append(out, application.MembershipInterval{UserID: m, From: time.Now().AddDate(-1, 0, 0)})
	}
	return out, nil
}

func serve(t *testing.T, enabled bool, a fakeAuth, members ...string) *http.ServeMux {
	pool := dbtest.Pool(t)
	svc := application.NewService(repository.New(pool), dir{members}, application.Config{Enabled: enabled})
	mux := http.NewServeMux()
	transport.Register(mux, svc, authenticator(a), slog.New(slog.DiscardHandler))
	return mux
}

type authenticator fakeAuth

func (a authenticator) Authenticate(r *http.Request) (authorization.Principal, bool, error) {
	return fakeAuth(a).Authenticate(r)
}

func do(mux http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	rec := httptest.NewRecorder()
	httpx.Middleware(slog.New(slog.DiscardHandler), mux).ServeHTTP(rec, req)
	return rec
}

func code(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	return e.Error.Code
}

func TestRoutesAndErrorCodes(t *testing.T) {
	user := newID()
	mux := serve(t, true, as(user, application.PermManageOwn), user)
	// The settings are enabled by other tests in the shared database; make sure the runtime switch is on.
	pool := dbtest.Pool(t)
	if _, err := pool.Exec(context.Background(), `UPDATE presence.settings SET enabled = true, disabled_at = NULL`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	from := now.Format(time.RFC3339)

	if rec := do(mux, "GET", "/api/v1/presence/me/entries?from="+from+"&to="+now.AddDate(0, 0, 40).Format(time.RFC3339), nil); rec.Code != 400 || code(t, rec) != "presence.window_too_large" {
		t.Errorf("window: %d %s", rec.Code, rec.Body)
	}
	if rec := do(mux, "GET", "/api/v1/presence/entries?from="+from+"&to="+now.AddDate(0, 0, 2).Format(time.RFC3339), nil); rec.Code != 403 || code(t, rec) != "presence.not_permitted" {
		t.Errorf("view_entries needed: %d %s", rec.Code, rec.Body)
	}
	if rec := do(mux, "PUT", "/api/v1/presence/settings", map[string]any{"enabled": true, "retentionDays": 30, "expectedVersion": 1}); rec.Code != 403 {
		t.Errorf("settings need admin: %d", rec.Code)
	}
	bad := map[string]any{"kind": "unavailable", "startsAt": now.Add(time.Hour), "endsAt": now.Add(2 * time.Hour), "timezone": "UTC", "recurrence": map[string]any{"frequency": "daily"}}
	if rec := do(mux, "POST", "/api/v1/presence/entries", bad); rec.Code != 400 || code(t, rec) != "presence.invalid_recurrence" {
		t.Errorf("recurrence: %d %s", rec.Code, rec.Body)
	}
	// Create, then cancel with and without a version.
	body := map[string]any{"kind": "unavailable", "startsAt": now.Add(time.Hour), "endsAt": now.Add(2 * time.Hour)}
	rec := do(mux, "POST", "/api/v1/presence/entries", body)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var created struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if rec := do(mux, "POST", "/api/v1/presence/entries/"+created.ID+"/cancel", map[string]any{}); rec.Code != 400 {
		t.Errorf("expectedVersion required: %d", rec.Code)
	}
	if rec := do(mux, "POST", "/api/v1/presence/entries/"+created.ID+"/cancel", map[string]any{"expectedVersion": created.Version + 5}); rec.Code != 409 || code(t, rec) != "presence.version_conflict" {
		t.Errorf("conflict: %d %s", rec.Code, rec.Body)
	}
	if rec := do(mux, "POST", "/api/v1/presence/entries/"+created.ID+"/cancel", map[string]any{"expectedVersion": created.Version}); rec.Code != 200 {
		t.Errorf("cancel: %d %s", rec.Code, rec.Body)
	}
	if rec := do(mux, "GET", fmt.Sprintf("/api/v1/presence/availability?userIds=%s", user), nil); rec.Code != 200 {
		t.Errorf("availability: %d %s", rec.Code, rec.Body)
	}
	if rec := do(mux, "GET", "/api/v1/presence/status", nil); rec.Code != 200 {
		t.Errorf("status: %d", rec.Code)
	}
}

func TestStartupGateMountsOnlyStatus(t *testing.T) {
	user := newID()
	mux := serve(t, false, as(user, application.PermManageOwn, application.PermAdmin), user)
	for _, p := range []string{"/api/v1/presence/settings", "/api/v1/presence/me/entries", "/api/v1/presence/availability"} {
		if rec := do(mux, "GET", p, nil); rec.Code != 404 && rec.Code != 405 {
			t.Errorf("%s while gated off: %d", p, rec.Code)
		}
	}
	rec := do(mux, "GET", "/api/v1/presence/status", nil)
	var st struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if rec.Code != 200 || st.Enabled {
		t.Errorf("status = %d %s", rec.Code, rec.Body)
	}
}
