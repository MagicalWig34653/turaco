package authentication

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

// accessPerms is a PermissionLoader that also implements AccessLoader.
type accessPerms struct{ access authorization.Access }

func (a accessPerms) Permissions(context.Context, string) (map[string]struct{}, error) {
	return a.access.Permissions, nil
}
func (a accessPerms) Access(context.Context, string) (authorization.Access, error) { return a.access, nil }

func TestSessionCarriesScopedPermissionsSeparately(t *testing.T) {
	fs := &fakeSessions{sessions: map[string]Session{"tok-secret": testSession()}}
	loader := accessPerms{access: authorization.Access{
		Permissions: map[string]struct{}{"tickets.view": {}},
		Scopes: map[string]authorization.LocationScope{
			"assets.view":  {Locations: []string{"loc-a", "loc-a1"}},
			"tickets.view": {Locations: []string{"loc-b"}}, // held globally: the scoped entry is shadowed
			"tasks.view":   {},                             // empty set: not listed
		},
	}}
	auth := NewSessionAuthenticator(fs, fakeGate{active: true}, loader, false)
	mux := http.NewServeMux()
	Register(mux, fs, auth, nil, false, slog.Default())

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, cookieReq("GET", "/api/v1/auth/session", "tok-secret"))
	var got sessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d, err %v", rec.Code, err)
	}
	if len(got.Permissions) != 1 || got.Permissions[0] != "tickets.view" {
		t.Fatalf("permissions = %v; scoped holdings must not appear in the global list", got.Permissions)
	}
	if len(got.ScopedPermissions) != 1 || len(got.ScopedPermissions["assets.view"]) != 2 {
		t.Fatalf("scopedPermissions = %v", got.ScopedPermissions)
	}

	_, p, ok, err := auth.Resolve(cookieReq("GET", "/api/v1/auth/session", "tok-secret"))
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	if p.Has("assets.view") {
		t.Fatal("a scoped holding must never satisfy Has")
	}
	if !p.ScopeOf("assets.view").Allows("loc-a1") || p.ScopeOf("assets.view").Allows("loc-b") {
		t.Fatalf("scope of assets.view = %+v", p.ScopeOf("assets.view"))
	}
}

func TestSessionWithoutScopesHasEmptyScopedObject(t *testing.T) {
	e := newEnv(false, true, map[string]struct{}{"a.perm": {}})
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, cookieReq("GET", "/api/v1/auth/session", "tok-secret"))
	if !contains(rec.Body.String(), `"scopedPermissions":{}`) {
		t.Fatalf("body = %s", rec.Body)
	}
}
