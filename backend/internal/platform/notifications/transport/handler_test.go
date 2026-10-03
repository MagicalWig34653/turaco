package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

type auth struct {
	user string
	ok   bool
}

func (a auth) Authenticate(*http.Request) (authorization.Principal, bool, error) {
	// No permissions at all: notifications are open to every signed-in user.
	return authorization.Principal{UserID: a.user, Permissions: map[string]struct{}{}}, a.ok, nil
}

type env struct {
	t    *testing.T
	pool *pgxpool.Pool
	svc  *notifications.Service
	a, b string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Pool(t)
	e := &env{t: t, pool: pool, svc: notifications.NewService(pool, testRegistry(t))}
	for _, dst := range []*string{&e.a, &e.b} {
		if err := pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		ids := []string{e.a, e.b}
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform.notification_preferences WHERE user_id = ANY($1::uuid[])`, ids)
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform.notifications WHERE recipient_user_id = ANY($1::uuid[])`, ids)
	})
	return e
}

func (e *env) create(user, key string) {
	e.t.Helper()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	err := pgx.BeginFunc(context.Background(), e.pool, func(tx pgx.Tx) error {
		_, err := e.svc.Create(context.Background(), tx, notifications.Intent{
			RecipientUserID: user, Category: "task.assigned", DedupeKey: key + hex.EncodeToString(b),
			Params: map[string]any{"title": "T"}, LinkType: "task", LinkID: e.b,
		})
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) do(a authorization.Authenticator, method, target, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	mux := http.NewServeMux()
	Register(mux, e.svc, a, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-ID", "req-1")
	var rd io.Reader
	if body != "" {
		rd = bytes.NewBufferString(body)
	}
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, rd))
	return rec
}

func TestEveryRouteNeedsASignedInUser(t *testing.T) {
	e := newEnv(t)
	for _, r := range []struct{ method, path, body string }{
		{"GET", "/api/v1/notifications", ""},
		{"GET", "/api/v1/notifications/unread-count", ""},
		{"POST", "/api/v1/notifications/read-all", ""},
		{"POST", "/api/v1/notifications/" + e.b + "/read", ""},
		{"GET", "/api/v1/notifications/preferences", ""},
		{"PUT", "/api/v1/notifications/preferences/task.assigned/email", `{"enabled":false}`},
	} {
		if rec := e.do(authorization.DenyAll{}, r.method, r.path, r.body); rec.Code != 401 {
			t.Errorf("%s %s unauthenticated = %d, want 401", r.method, r.path, rec.Code)
		}
		if rec := e.do(auth{user: e.a, ok: true}, r.method, r.path, r.body); rec.Code == 401 || rec.Code == 403 {
			t.Errorf("%s %s signed in without permissions = %d, want access (ownership is the rule)", r.method, r.path, rec.Code)
		}
	}
}

func TestUsersSeeOnlyTheirOwnNotifications(t *testing.T) {
	e := newEnv(t)
	e.create(e.a, "a1")
	e.create(e.a, "a2")
	e.create(e.b, "b1")

	var list struct {
		Items []struct {
			ID     string         `json:"id"`
			Params map[string]any `json:"params"`
			ReadAt *string        `json:"readAt"`
		} `json:"items"`
	}
	rec := e.do(auth{user: e.a, ok: true}, "GET", "/api/v1/notifications", "")
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Items) != 2 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("notifications must not be cached")
	}
	mine := list.Items[0].ID

	// B cannot mark A's notification: 404, not 403, so ids do not leak.
	if rec := e.do(auth{user: e.b, ok: true}, "POST", "/api/v1/notifications/"+mine+"/read", ""); rec.Code != 404 {
		t.Errorf("foreign mark read = %d, want 404", rec.Code)
	}
	if rec := e.do(auth{user: e.a, ok: true}, "POST", "/api/v1/notifications/"+mine+"/read", ""); rec.Code != 204 {
		t.Errorf("own mark read = %d, want 204", rec.Code)
	}
	var count struct{ Count, Max int }
	rec = e.do(auth{user: e.a, ok: true}, "GET", "/api/v1/notifications/unread-count", "")
	if json.Unmarshal(rec.Body.Bytes(), &count) != nil || count.Count != 1 || count.Max != notifications.MaxUnreadCount {
		t.Errorf("count = %+v (%s)", count, rec.Body)
	}
	if rec := e.do(auth{user: e.a, ok: true}, "GET", "/api/v1/notifications?unread=true", ""); !strings.Contains(rec.Body.String(), `"readAt":null`) || strings.Contains(rec.Body.String(), mine) {
		t.Errorf("unread filter = %s", rec.Body)
	}
	if rec := e.do(auth{user: e.a, ok: true}, "POST", "/api/v1/notifications/read-all", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"marked":1`) {
		t.Errorf("read-all = %d %s", rec.Code, rec.Body)
	}
	// B's notification is untouched by A's read-all.
	rec = e.do(auth{user: e.b, ok: true}, "GET", "/api/v1/notifications/unread-count", "")
	if !strings.Contains(rec.Body.String(), `"count":1`) {
		t.Errorf("b's count = %s", rec.Body)
	}
}

func TestInvalidRequests(t *testing.T) {
	e := newEnv(t)
	a := auth{user: e.a, ok: true}
	for name, c := range map[string]struct {
		method, path, body string
		status             int
	}{
		"bad limit":         {"GET", "/api/v1/notifications?limit=0", "", 400},
		"bad cursor":        {"GET", "/api/v1/notifications?cursor=x", "", 400},
		"bad unread":        {"GET", "/api/v1/notifications?unread=maybe", "", 400},
		"malformed id":      {"POST", "/api/v1/notifications/garbage/read", "", 404},
		"unknown category":  {"PUT", "/api/v1/notifications/preferences/nope/email", `{"enabled":true}`, 404},
		"missing enabled":   {"PUT", "/api/v1/notifications/preferences/task.assigned/email", `{}`, 400},
		"unknown field":     {"PUT", "/api/v1/notifications/preferences/task.assigned/email", `{"enabled":true,"x":1}`, 400},
		"not json":          {"PUT", "/api/v1/notifications/preferences/task.assigned/email", `x`, 400},
		"non-boolean value": {"PUT", "/api/v1/notifications/preferences/task.assigned/email", `{"enabled":"yes"}`, 400},
	} {
		if rec := e.do(a, c.method, c.path, c.body); rec.Code != c.status {
			t.Errorf("%s: status = %d, want %d (%s)", name, rec.Code, c.status, rec.Body)
		}
	}
}

func TestPreferencesRoundTrip(t *testing.T) {
	e := newEnv(t)
	a, b := auth{user: e.a, ok: true}, auth{user: e.b, ok: true}
	if rec := e.do(a, "PUT", "/api/v1/notifications/preferences/task.completed/email", `{"enabled":false}`); rec.Code != 200 {
		t.Fatalf("put = %d %s", rec.Code, rec.Body)
	}
	var prefs struct {
		Items []struct {
			Category string `json:"category"`
			Channel  string `json:"channel"`
			Enabled  bool   `json:"enabled"`
		} `json:"items"`
	}
	rec := e.do(a, "GET", "/api/v1/notifications/preferences", "")
	if json.Unmarshal(rec.Body.Bytes(), &prefs) != nil || len(prefs.Items) != 2 {
		t.Fatalf("prefs = %s", rec.Body)
	}
	for _, p := range prefs.Items {
		if p.Channel != "email" || p.Enabled != (p.Category != "task.completed") {
			t.Errorf("pref = %+v", p)
		}
	}
	rec = e.do(b, "GET", "/api/v1/notifications/preferences", "")
	if strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Errorf("another user's preference leaked: %s", rec.Body)
	}
}

func testRegistry(t *testing.T) *notifications.Registry {
	t.Helper()
	text := notifications.EmailText{Subject: "S: %s", Intro: "I", Action: "A"}
	both := map[string]notifications.EmailText{"en": text, "de": text}
	r, err := notifications.NewRegistry(
		notifications.Category{Name: "task.assigned", Owner: "tasks", LinkType: "task", LinkPath: "/tasks/{id}", Email: both},
		notifications.Category{Name: "task.completed", Owner: "tasks", LinkType: "task", LinkPath: "/tasks/{id}", Email: both},
	)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
