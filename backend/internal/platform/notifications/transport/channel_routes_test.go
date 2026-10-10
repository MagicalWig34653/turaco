package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

type permAuth struct {
	user  string
	perms []string
	ok    bool
}

func (a permAuth) Authenticate(*http.Request) (authorization.Principal, bool, error) {
	p := authorization.Principal{UserID: a.user, Permissions: map[string]struct{}{}}
	for _, x := range a.perms {
		p.Permissions[x] = struct{}{}
	}
	return p, a.ok, nil
}

func TestChannelRoutesAPI(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	var admin string
	if err := pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	both := map[string]notifications.EmailText{"en": {Subject: "S %s", Intro: "I", Action: "A"}, "de": {Subject: "S %s", Intro: "I", Action: "A"}}
	text := notifications.ChannelText{Headline: "h", Action: "a"}
	reg, err := notifications.NewRegistry(
		notifications.Category{Name: "tapi.declared", Owner: "x", LinkType: "t", LinkPath: "/t/{id}", Email: both,
			Broadcast: &notifications.Broadcast{Texts: map[string]map[string]notifications.ChannelText{"declared": {"en": text, "de": text}}}},
		notifications.Category{Name: "tapi.personal", Owner: "x", Email: both})
	if err != nil {
		t.Fatal(err)
	}
	svc := notifications.NewService(pool, reg).WithChannelPosts(notifications.ChannelOptions{
		DestinationKeys: func() []string { return []string{"tapi-infra"} }, Mode: "fake"})
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform.notification_channel_routes WHERE category = 'tapi.declared'`)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE actor_id = $1::uuid`, admin)
	})
	do := func(a authorization.Authenticator, method, target, body string) *httptest.ResponseRecorder {
		mux := http.NewServeMux()
		RegisterChannelRoutes(mux, svc, a, slog.New(slog.NewTextHandler(io.Discard, nil)))
		rec := httptest.NewRecorder()
		rec.Header().Set("X-Request-ID", "req-1")
		var rd io.Reader
		if body != "" {
			rd = bytes.NewBufferString(body)
		}
		mux.ServeHTTP(rec, httptest.NewRequest(method, target, rd))
		return rec
	}
	manager := permAuth{user: admin, perms: []string{PermTeamsManage}, ok: true}

	for name, a := range map[string]authorization.Authenticator{
		"anonymous":  permAuth{ok: false},
		"no rights":  permAuth{user: admin, ok: true},
		"other perm": permAuth{user: admin, perms: []string{"platform.health.view"}, ok: true},
	} {
		want := http.StatusForbidden
		if name == "anonymous" {
			want = http.StatusUnauthorized
		}
		for _, call := range [][3]string{{"GET", "/api/v1/integrations/teams/channel-routes", ""}, {"POST", "/api/v1/integrations/teams/channel-routes", `{"category":"tapi.declared","destinationKey":"tapi-infra"}`}} {
			if rec := do(a, call[0], call[1], call[2]); rec.Code != want {
				t.Errorf("%s %s: status %d, want %d", name, call[0], rec.Code, want)
			}
		}
	}

	if rec := do(manager, "POST", "/api/v1/integrations/teams/channel-routes", `{"category":"tapi.personal","destinationKey":"tapi-infra"}`); rec.Code != 400 || !bytes.Contains(rec.Body.Bytes(), []byte("category_not_broadcastable")) {
		t.Errorf("personal: %d %s", rec.Code, rec.Body)
	}
	if rec := do(manager, "POST", "/api/v1/integrations/teams/channel-routes", `{"category":"tapi.declared","destinationKey":"other"}`); rec.Code != 400 || !bytes.Contains(rec.Body.Bytes(), []byte("unknown_destination")) {
		t.Errorf("unknown destination: %d %s", rec.Code, rec.Body)
	}
	if rec := do(manager, "POST", "/api/v1/integrations/teams/channel-routes", `{"category":"tapi.declared","destinationKey":"tapi-infra","url":"https://x"}`); rec.Code != 400 {
		t.Errorf("unknown field: %d", rec.Code)
	}
	rec := do(manager, "POST", "/api/v1/integrations/teams/channel-routes", `{"category":"tapi.declared","destinationKey":"tapi-infra"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var created routeDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.ID == "" || created.CreatedBy != admin {
		t.Fatalf("created = %+v err=%v", created, err)
	}
	if rec := do(manager, "POST", "/api/v1/integrations/teams/channel-routes", `{"category":"tapi.declared","destinationKey":"tapi-infra"}`); rec.Code != http.StatusConflict {
		t.Errorf("duplicate: %d", rec.Code)
	}
	rec = do(manager, "GET", "/api/v1/integrations/teams/channel-routes", "")
	var list struct {
		Items        []routeDTO `json:"items"`
		Mode         string     `json:"mode"`
		Destinations []string   `json:"destinations"`
		Categories   []string   `json:"categories"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != 200 {
		t.Fatalf("list: %d %v", rec.Code, err)
	}
	if list.Mode != "fake" || len(list.Destinations) != 1 || len(list.Categories) != 1 || list.Categories[0] != "tapi.declared" {
		t.Errorf("list = %+v", list)
	}
	if rec := do(manager, "DELETE", "/api/v1/integrations/teams/channel-routes/"+created.ID, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}
	if rec := do(manager, "DELETE", "/api/v1/integrations/teams/channel-routes/"+created.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("second delete: %d", rec.Code)
	}
}
