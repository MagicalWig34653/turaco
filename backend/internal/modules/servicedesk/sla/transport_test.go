package sla

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

type testAuth struct {
	principal authorization.Principal
	ok        bool
}

func (a testAuth) Authenticate(*http.Request) (authorization.Principal, bool, error) {
	return a.principal, a.ok, nil
}
func TestRoutesDenyWithoutPermission(t *testing.T) {
	routes := []struct{ method, path string }{
		{"GET", "/api/v1/sla/policies"}, {"GET", "/api/v1/sla/policies/00000000-0000-0000-0000-000000000000"},
		{"POST", "/api/v1/sla/policies"}, {"PATCH", "/api/v1/sla/policies/00000000-0000-0000-0000-000000000000"},
		{"PUT", "/api/v1/sla/policies/00000000-0000-0000-0000-000000000000/targets"},
		{"POST", "/api/v1/sla/policies/00000000-0000-0000-0000-000000000000/archive"},
		{"POST", "/api/v1/sla/policies/00000000-0000-0000-0000-000000000000/restore"},
		{"GET", "/api/v1/sla/queues"}, {"PUT", "/api/v1/sla/queues/00000000-0000-0000-0000-000000000000/policy"},
	}
	for _, tc := range []struct {
		name string
		auth testAuth
		want int
	}{
		{"anonymous", testAuth{}, 401},
		{"ordinary user", testAuth{authorization.Principal{UserID: "user", Permissions: map[string]struct{}{}}, true}, 403},
		{"unrelated administrator", testAuth{authorization.Principal{UserID: "user", Permissions: map[string]struct{}{"modules.manage": {}}}, true}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			Register(mux, nil, tc.auth, slog.New(slog.NewTextHandler(io.Discard, nil)))
			for _, route := range routes {
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, httptest.NewRequest(route.method, route.path, strings.NewReader("{}")))
				if rec.Code != tc.want {
					t.Errorf("%s %s: got %d want %d", route.method, route.path, rec.Code, tc.want)
				}
			}
		})
	}
}
func TestReadPermissionCannotWrite(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, nil, testAuth{authorization.Principal{UserID: "user", Permissions: map[string]struct{}{"sla.view": {}}}, true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, route := range []struct{ method, path string }{{"POST", "/api/v1/sla/policies"}, {"PUT", "/api/v1/sla/queues/00000000-0000-0000-0000-000000000000/policy"}} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(route.method, route.path, strings.NewReader("{}")))
		if rec.Code != 403 {
			t.Errorf("%s %s: got %d", route.method, route.path, rec.Code)
		}
	}
}
