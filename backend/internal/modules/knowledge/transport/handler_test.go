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

	"github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/transport"
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

const (
	editor   = "00000000-0000-7000-8000-0000000000f1"
	employee = "00000000-0000-7000-8000-0000000000f2"
)

func serve(t *testing.T, a authorization.Authenticator) http.Handler {
	t.Helper()
	pool := dbtest.Pool(t)
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	transport.Register(mux, application.NewService(repository.New(pool)), a, logger)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM knowledge.articles WHERE title LIKE 'http-kb%'`)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE actor_id = $1::uuid`, editor)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE actor_id = $1::uuid`, editor)
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

func TestKnowledgeOverHTTP(t *testing.T) {
	mgr := serve(t, as(editor, "knowledge.manage"))
	rec := do(mgr, "POST", "/api/v1/knowledge-articles", `{"title":"http-kb printer","summary":"s","body":"Turn it off and on again.","audience":"employee"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var a struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	user := serve(t, as(employee))
	if rec := do(user, "GET", "/api/v1/knowledge-articles/"+a.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("draft as employee = %d", rec.Code)
	}
	if rec := do(user, "POST", "/api/v1/knowledge-articles", `{"title":"http-kb x","body":"y"}`); rec.Code != http.StatusForbidden {
		t.Errorf("employee create = %d", rec.Code)
	}
	if rec := do(mgr, "PATCH", "/api/v1/knowledge-articles/"+a.ID, `{"title":"http-kb printer","body":"z"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("update without version = %d", rec.Code)
	}
	if rec := do(mgr, "POST", "/api/v1/knowledge-articles/"+a.ID+"/publish", `{}`); rec.Code != http.StatusOK {
		t.Errorf("publish = %d %s", rec.Code, rec.Body)
	}
	rec = do(user, "GET", "/api/v1/knowledge-articles/"+a.ID, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Turn it off") {
		t.Errorf("published as employee = %d %s", rec.Code, rec.Body)
	}
	rec = do(user, "GET", "/api/v1/knowledge-articles?q=printer", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), a.ID) || strings.Contains(rec.Body.String(), "Turn it off") {
		t.Errorf("search = %d %s (the list must not carry bodies)", rec.Code, rec.Body)
	}
	if rec := do(user, "POST", "/api/v1/knowledge-articles/"+a.ID+"/retire", `{}`); rec.Code != http.StatusForbidden {
		t.Errorf("employee retire = %d", rec.Code)
	}
	if rec := do(user, "GET", "/api/v1/knowledge-articles?status=draft", ""); rec.Code != http.StatusForbidden {
		t.Errorf("employee lists drafts = %d", rec.Code)
	}
	if rec := do(user, "GET", "/api/v1/knowledge-articles/not-a-uuid", ""); rec.Code != http.StatusNotFound {
		t.Errorf("malformed id = %d", rec.Code)
	}
	if rec := do(serve(t, fakeAuth{}), "GET", "/api/v1/knowledge-articles", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d", rec.Code)
	}
}
