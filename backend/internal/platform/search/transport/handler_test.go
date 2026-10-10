package transport

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/search"
)

type src struct {
	typ  string
	need string
}

func (s src) Type() string   { return s.typ }
func (s src) Module() string { return s.typ }
func (s src) Search(_ context.Context, p authorization.Principal, q string, _ int) ([]search.Hit, error) {
	if s.need != "" && !p.Has(s.need) {
		return nil, search.ErrNotAllowed
	}
	return []search.Hit{{ID: "1", Title: q}}, nil
}

type auth struct {
	p  authorization.Principal
	ok bool
}

func (a auth) Authenticate(*http.Request) (authorization.Principal, bool, error) {
	return a.p, a.ok, nil
}

func setup(t *testing.T, a auth) (http.Handler, *handler) {
	t.Helper()
	svc := search.New()
	svc.Register(src{typ: "ticket"}, src{typ: "user", need: "organization.view"})
	mux := http.NewServeMux()
	Register(mux, svc, a, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return mux, nil
}

func get(h http.Handler, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestSearchEndpoint(t *testing.T) {
	h, _ := setup(t, auth{p: authorization.Principal{UserID: "u1"}, ok: true})
	rec := get(h, "/api/v1/search?q=Etikett")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") == "" {
		t.Fatalf("status %d headers %v", rec.Code, rec.Header())
	}
	var body struct {
		Items       []map[string]any `json:"items"`
		Unavailable []string         `json:"unavailable"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0]["type"] != "ticket" || body.Unavailable == nil {
		t.Errorf("a caller without organization.view must not receive user hits: %s", rec.Body)
	}
	for _, target := range []string{"/api/v1/search?q=a", "/api/v1/search", "/api/v1/search?q=ab&types=x", "/api/v1/search?q=ab&limit=0", "/api/v1/search?q=ab&limit=11"} {
		if rec := get(h, target); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", target, rec.Code)
		}
	}
	h, _ = setup(t, auth{p: authorization.Principal{UserID: "u1", Permissions: map[string]struct{}{"organization.view": {}}}, ok: true})
	rec = get(h, "/api/v1/search?q=ab&types=user")
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Items) != 1 || body.Items[0]["type"] != "user" {
		t.Errorf("type filter: %s", rec.Body)
	}
}

func TestSearchRequiresSignInAndLimitsRate(t *testing.T) {
	h, _ := setup(t, auth{})
	if rec := get(h, "/api/v1/search?q=ab"); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", rec.Code)
	}
	hd := &handler{recent: map[string][]time.Time{}, now: time.Now}
	for i := 0; i < rateBurst; i++ {
		if !hd.allow("u") {
			t.Fatalf("request %d refused", i)
		}
	}
	if hd.allow("u") || !hd.allow("other") {
		t.Error("the burst is per user")
	}
	hd.now = func() time.Time { return time.Now().Add(rateWindow + time.Second) }
	if !hd.allow("u") {
		t.Error("the window must slide")
	}
}
