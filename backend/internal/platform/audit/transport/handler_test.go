package transport

import (
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
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

type fixedAuth struct {
	p  authorization.Principal
	ok bool
}

func (f fixedAuth) Authenticate(*http.Request) (authorization.Principal, bool, error) {
	return f.p, f.ok, nil
}

func newMux(t *testing.T, auth authorization.Authenticator) (*http.ServeMux, string) {
	t.Helper()
	pool := dbtest.Pool(t)
	mux := http.NewServeMux()
	Register(mux, audit.NewReader(pool), auth, slog.New(slog.NewTextHandler(io.Discard, nil)))
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return mux, "zt" + hex.EncodeToString(b)
}

func get(mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
	return rec
}

func viewer() authorization.Authenticator {
	return fixedAuth{p: authorization.Principal{UserID: "u", Permissions: map[string]struct{}{"platform.audit.view": {}}}, ok: true}
}

func TestAuditEventsRequiresPermission(t *testing.T) {
	for name, tc := range map[string]struct {
		auth authorization.Authenticator
		want int
	}{
		"unauthenticated": {authorization.DenyAll{}, 401},
		"forbidden":       {fixedAuth{p: authorization.Principal{UserID: "u", Permissions: map[string]struct{}{"platform.roles.view": {}}}, ok: true}, 403},
	} {
		mux, _ := newMux(t, tc.auth)
		if rec := get(mux, "/api/v1/audit-events"); rec.Code != tc.want {
			t.Fatalf("%s = %d", name, rec.Code)
		}
	}
}

func TestAuditEventsList(t *testing.T) {
	mux, pfx := newMux(t, viewer())
	pool := dbtest.Pool(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id LIKE $1 || '%'`, pfx)
	})
	var actor string
	if err := pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		var id string
		if err := pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		e := audit.Entry{ID: id, OccurredAt: time.Now().UTC(), Action: pfx + ".x", TargetType: "thing", TargetID: "t", CorrelationID: pfx + "-c", Metadata: []byte(`{"actor":"cli"}`)}
		if i == 0 {
			e.ActorID, e.After = &actor, []byte(`{"a":1}`)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := audit.Insert(ctx, tx, e); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}

	rec := get(mux, "/api/v1/audit-events?actionPrefix="+pfx+"&limit=2")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"nextCursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 2 || body.NextCursor == "" {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if _, ok := body.Items[0]["actorId"]; ok {
		t.Fatalf("NULL actor must be omitted: %v", body.Items[0])
	}
	if _, ok := body.Items[0]["before"]; ok {
		t.Fatalf("NULL before must be omitted: %v", body.Items[0])
	}
	if body.Items[0]["metadata"].(map[string]any)["actor"] != "cli" {
		t.Fatalf("metadata = %v", body.Items[0]["metadata"])
	}
	rec = get(mux, "/api/v1/audit-events?actionPrefix="+pfx+"&limit=2&cursor="+body.NextCursor)
	body.Items, body.NextCursor = nil, ""
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Items) != 1 || body.NextCursor != "" || body.Items[0]["actorId"] != actor || body.Items[0]["after"].(map[string]any)["a"] != float64(1) {
		t.Fatalf("page 2 = %s", rec.Body.String())
	}
}

func TestAuditEventsValidation(t *testing.T) {
	mux, _ := newMux(t, viewer())
	for target, code := range map[string]string{
		"?from=yesterday":                     "audit.invalid_filter",
		"?to=2026-01-01":                      "audit.invalid_filter",
		"?actorId=nope":                       "audit.invalid_filter",
		"?action=" + strings.Repeat("a", 201): "audit.invalid_filter",
		"?targetId=x":                         "audit.invalid_filter",
		"?actionPrefix=a%FF":                  "audit.invalid_filter",
		"?cursor=nope":                        "audit.invalid_cursor",
		"?limit=0":                            "audit.invalid_limit",
		"?limit=abc":                          "audit.invalid_limit",
	} {
		rec := get(mux, "/api/v1/audit-events"+target)
		var e struct{ Error struct{ Code string } }
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		if rec.Code != 400 || e.Error.Code != code {
			t.Fatalf("%s = %d %s", target, rec.Code, rec.Body.String())
		}
	}
	if rec := get(mux, "/api/v1/audit-events?limit=100000&from=2020-01-01T00:00:00Z"); rec.Code != 200 {
		t.Fatalf("clamped limit = %d %s", rec.Code, rec.Body.String())
	}
}
