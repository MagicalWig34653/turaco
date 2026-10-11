package transport

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

type stubPoster struct{ err error }

func (p stubPoster) PostToChannel(context.Context, string, notifications.ChannelCard) error {
	return p.err
}

func TestChannelTestSendAPI(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	var admin string
	if err := pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	reg, err := notifications.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	const key = "tsend-api"
	svc := notifications.NewService(pool, reg).WithChannelPosts(notifications.ChannelOptions{
		DestinationKeys: func() []string { return []string{key} }, Mode: "fake", Poster: stubPoster{}, BaseURL: "https://turaco.example.org"})
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE action = $1 AND target_id = $2`, notifications.ActionTeamsTestSent, key)
	})
	do := func(a authorization.Authenticator, k string) *httptest.ResponseRecorder {
		mux := http.NewServeMux()
		RegisterChannelRoutes(mux, svc, a, slog.New(slog.NewTextHandler(io.Discard, nil)))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/integrations/teams/destinations/"+k+"/test-send", nil))
		return rec
	}
	if rec := do(permAuth{ok: false}, key); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", rec.Code)
	}
	for name, a := range map[string]permAuth{
		"no rights":  {user: admin, ok: true},
		"other perm": {user: admin, perms: []string{"platform.health.view", "platform.health.probe"}, ok: true},
	} {
		if rec := do(a, key); rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	manager := permAuth{user: admin, perms: []string{PermTeamsManage}, ok: true}
	if rec := do(manager, "other"); rec.Code != http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte("unknown_destination")) {
		t.Errorf("unknown destination: %d %s", rec.Code, rec.Body)
	}
	for i := 0; i < notifications.TestSendsPerWindow; i++ {
		rec := do(manager, key)
		if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"delivered":true`)) {
			t.Fatalf("test send %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	if rec := do(manager, key); rec.Code != http.StatusTooManyRequests || !bytes.Contains(rec.Body.Bytes(), []byte("test_send_rate_limited")) {
		t.Errorf("rate limit: %d %s", rec.Code, rec.Body)
	}
}
