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

	"github.com/MagicalWig34653/turaco/backend/internal/modules/assets/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/assets/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/assets/transport"
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

type dir struct{ active map[string]bool }

func (d dir) pick(ids []string) map[string]bool {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = d.active[id]
	}
	return out
}
func (d dir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	return d.pick(ids), nil
}
func (d dir) ActiveTeams(_ context.Context, ids []string) (map[string]bool, error) {
	return d.pick(ids), nil
}
func (d dir) ActiveLocations(_ context.Context, ids []string) (map[string]bool, error) {
	return d.pick(ids), nil
}
func (d dir) UserNames(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (d dir) TeamNames(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (d dir) LocationNames(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

type products map[string]application.ProductInfo

func (p products) Products(_ context.Context, ids []string) (map[string]application.ProductInfo, error) {
	out := map[string]application.ProductInfo{}
	for _, id := range ids {
		if v, ok := p[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

const (
	holder   = "00000000-0000-7000-8000-0000000000a1"
	stranger = "00000000-0000-7000-8000-0000000000b2"
	admin    = "00000000-0000-7000-8000-0000000000c3"
	product  = "00000000-0000-7000-8000-0000000000d4"
)

func serve(t *testing.T, a authorization.Authenticator) http.Handler {
	t.Helper()
	pool := dbtest.Pool(t)
	svc := application.NewService(repository.New(pool), dir{active: map[string]bool{holder: true}},
		products{product: {ID: product, Name: "Mouse", Active: true, AssetManaged: true}}, nil)
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	transport.Register(mux, svc, a, logger)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM assets.assets WHERE product_id = $1::uuid`, product)
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform.audit_events WHERE actor_id = $1::uuid`, admin)
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform.outbox_events WHERE actor_id = $1::uuid`, admin)
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

func TestPermissionsAndOwnershipOverHTTP(t *testing.T) {
	manage := serve(t, as(admin, "assets.manage"))
	rec := do(manage, "POST", "/api/v1/assets", `{"productId":"`+product+`","serialNumber":"HTTP-1","assetTag":"HTTP-T1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var created struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	rec = do(manage, "POST", "/api/v1/assets/"+created.ID+"/assign", `{"assigneeType":"user","assigneeId":"`+holder+`"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"assigned"`) {
		t.Fatalf("assign = %d %s", rec.Code, rec.Body)
	}
	if rec := do(manage, "POST", "/api/v1/assets/"+created.ID+"/assign", `{"assigneeType":"user","assigneeId":"`+holder+`"}`); rec.Code != http.StatusConflict {
		t.Errorf("assigning twice = %d", rec.Code)
	}
	if rec := do(manage, "POST", "/api/v1/assets/"+created.ID+"/return", `{"expectedVersion":99}`); rec.Code != http.StatusConflict {
		t.Errorf("stale version = %d", rec.Code)
	}

	// The holder reads the asset and finds it under my-assets; nothing else.
	asHolder := serve(t, as(holder))
	if rec := do(asHolder, "GET", "/api/v1/assets/"+created.ID, ""); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "returnedAt\":\"") {
		t.Errorf("holder detail = %d %s", rec.Code, rec.Body)
	}
	if rec := do(asHolder, "GET", "/api/v1/my-assets", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), created.ID) {
		t.Errorf("my assets = %d %s", rec.Code, rec.Body)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/assets"}, {"GET", "/api/v1/assets/lookup?code=HTTP-T1"}, {"POST", "/api/v1/assets"},
		{"PATCH", "/api/v1/assets/" + created.ID}, {"POST", "/api/v1/assets/" + created.ID + "/return"},
	} {
		if rec := do(asHolder, c.method, c.path, `{}`); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as a plain user = %d, want 403", c.method, c.path, rec.Code)
		}
	}
	asStranger := serve(t, as(stranger))
	if rec := do(asStranger, "GET", "/api/v1/assets/"+created.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("stranger detail = %d, want 404", rec.Code)
	}
	if rec := do(asStranger, "GET", "/api/v1/my-assets", ""); strings.Contains(rec.Body.String(), created.ID) {
		t.Error("my-assets leaked another user's asset")
	}
	if rec := do(serve(t, fakeAuth{}), "GET", "/api/v1/my-assets", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d", rec.Code)
	}

	view := serve(t, as(stranger, "assets.view"))
	if rec := do(view, "GET", "/api/v1/assets/lookup?code=http-t1", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), created.ID) {
		t.Errorf("lookup = %d %s", rec.Code, rec.Body)
	}
	if rec := do(view, "GET", "/api/v1/assets?status=assigned&assigneeId="+holder, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), created.ID) {
		t.Errorf("filtered list = %d %s", rec.Code, rec.Body)
	}
	if rec := do(view, "GET", "/api/v1/assets?status=weird", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown status filter = %d", rec.Code)
	}
}

func TestValidationErrorsOverHTTP(t *testing.T) {
	h := serve(t, as(admin, "assets.manage"))
	for name, c := range map[string]struct{ method, path, body string }{
		"bad date":          {"POST", "/api/v1/assets", `{"productId":"` + product + `","purchasedAt":"01.02.2026"}`},
		"unknown product":   {"POST", "/api/v1/assets", `{"productId":"00000000-0000-7000-8000-0000000000ff"}`},
		"unknown field":     {"POST", "/api/v1/assets", `{"productId":"` + product + `","status":"available","surprise":1}`},
		"update no version": {"PATCH", "/api/v1/assets/00000000-0000-7000-8000-0000000000ee", `{"notes":"x"}`},
	} {
		if rec := do(h, c.method, c.path, c.body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := do(h, "GET", "/api/v1/assets/00000000-0000-7000-8000-0000000000ee", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown asset = %d", rec.Code)
	}
}
