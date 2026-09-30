package authorization

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fixed struct {
	p   Principal
	ok  bool
	err error
}

func (f fixed) Authenticate(*http.Request) (Principal, bool, error) { return f.p, f.ok, f.err }

func TestRequireAuthenticatorErrorFailsClosed(t *testing.T) {
	called := false
	h := Require(fixed{err: errors.New("boom"), ok: true}, "organization.view")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError || called {
		t.Fatalf("status = %d, called = %v", rec.Code, called)
	}
}

func TestRequire(t *testing.T) {
	tests := []struct {
		name string
		auth Authenticator
		want int
	}{
		{"default deny", DenyAll{}, http.StatusUnauthorized},
		{"missing permission", fixed{p: Principal{UserID: "u", Permissions: map[string]struct{}{}}, ok: true}, http.StatusForbidden},
		{"allowed", fixed{p: Principal{UserID: "u", Permissions: map[string]struct{}{"organization.view": {}}}, ok: true}, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			h := Require(tt.auth, "organization.view")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if _, ok := PrincipalFrom(r.Context()); !ok {
					t.Error("principal missing from context")
				}
			}))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if called != (tt.want == http.StatusOK) {
				t.Fatalf("handler called = %v", called)
			}
		})
	}
}

func TestRequirePanicsOnUnregisteredPermission(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	Require(DenyAll{}, "does.not.exist")
}
