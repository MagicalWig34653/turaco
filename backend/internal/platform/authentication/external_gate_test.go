package authentication

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/modules"
)

type fakeAccountGate struct {
	state AccountState
	err   error
	calls int
}

func (f *fakeAccountGate) IsActive(context.Context, string) (bool, error) {
	return f.state.Active, f.err
}
func (f *fakeAccountGate) Account(context.Context, string) (AccountState, error) {
	f.calls++
	return f.state, f.err
}

func newExternalGateRig(state AccountState) (*SessionAuthenticator, *fakeAccountGate, http.Handler, *int) {
	gate := &fakeAccountGate{state: state}
	sessions := &fakeSessions{sessions: map[string]Session{"tok": testSession()}}
	a := NewSessionAuthenticator(sessions, gate, fakePerms{perms: map[string]struct{}{"tasks.work": {}}}, false)
	reached := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		// The route's own authentication reuses the principal the gate resolved.
		if p, ok, err := a.Authenticate(r); err != nil || !ok || p.UserID != "u1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := ConfineExternal(a, modules.DefaultIndex().ExternalAllowed, logger)(inner)
	return a, gate, h, &reached
}

func TestPrincipalCarriesTheExternalFlag(t *testing.T) {
	a, _, _, _ := newExternalGateRig(AccountState{Active: true, External: true})
	p, ok, err := a.Authenticate(cookieReq("GET", "/api/v1/tasks", "tok"))
	if err != nil || !ok || !p.External {
		t.Fatalf("principal = %+v %v %v", p, ok, err)
	}
	a2, _, _, _ := newExternalGateRig(AccountState{Active: true})
	if p, ok, _ := a2.Authenticate(cookieReq("GET", "/api/v1/tasks", "tok")); !ok || p.External {
		t.Errorf("an employee is not external: %+v", p)
	}
	// A gate that only answers IsActive (the older contract) yields an internal principal.
	plain := NewSessionAuthenticator(&fakeSessions{sessions: map[string]Session{"tok": testSession()}}, fakeGate{active: true}, fakePerms{}, false)
	if p, ok, _ := plain.Authenticate(cookieReq("GET", "/", "tok")); !ok || p.External {
		t.Errorf("IsActive-only gate: %+v", p)
	}
}

func TestExternalAccountsReachOnlyTheirAllowList(t *testing.T) {
	_, gate, h, reached := newExternalGateRig(AccountState{Active: true, External: true})
	id := "0198a7c0-1111-7222-8333-444455556666"
	allowed := []string{"GET /api/v1/tasks", "GET /api/v1/tasks/" + id, "POST /api/v1/tasks/" + id + "/complete", "GET /api/v1/tickets/" + id,
		"POST /api/v1/tickets/" + id + "/comments", "GET /api/v1/auth/session", "GET /api/v1/notifications/unread-count"}
	for _, a := range allowed {
		method, path, _ := strings.Cut(a, " ")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, cookieReq(method, path, "tok"))
		if rec.Code != http.StatusNoContent {
			t.Errorf("%s = %d, want the route", a, rec.Code)
		}
	}
	if gate.calls != len(allowed) {
		t.Errorf("the gate resolves the account once per request, got %d lookups for %d requests", gate.calls, len(allowed))
	}
	before := *reached
	denied := []string{"GET /api/v1/search?q=x", "GET /api/v1/views", "POST /api/v1/tasks/query", "POST /api/v1/tickets/query", "POST /api/v1/ai/sessions",
		"GET /api/v1/users", "GET /api/v1/tickets", "GET /api/v1/tickets/by-reference", "POST /api/v1/tickets", "GET /api/v1/tickets/" + id + "/history",
		"GET /api/v1/knowledge-articles", "GET /api/v1/me/pins", "GET /api/v1/briefing", "DELETE /api/v1/tickets/" + id + "/participants/" + id,
		"GET /api/v1/tickets/" + id + "%2Fcomments", "GET /api/v1/tasks/" + id + "%2Fstart"}
	for _, d := range denied {
		method, path, _ := strings.Cut(d, " ")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, cookieReq(method, path, "tok"))
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), ErrorCodeNotAvailable) {
			t.Errorf("%s = %d %s, want 404 %s", d, rec.Code, rec.Body.String(), ErrorCodeNotAvailable)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: the answer must not be cached", d)
		}
	}
	if *reached != before {
		t.Error("a denied request reached the route")
	}
	// Outside /api/v1 the gate has no opinion.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, cookieReq("GET", "/health/live", "tok"))
	if rec.Code != http.StatusNoContent {
		t.Errorf("non-API path = %d", rec.Code)
	}
}

func TestEmployeesAndAnonymousRequestsAreNotConfined(t *testing.T) {
	_, _, h, _ := newExternalGateRig(AccountState{Active: true})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, cookieReq("POST", "/api/v1/tickets/query", "tok"))
	if rec.Code != http.StatusNoContent {
		t.Errorf("employee = %d", rec.Code)
	}
	// No cookie, or a token that is not a session: the route answers 401 itself.
	for _, tok := range []string{"", "bogus"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, cookieReq("GET", "/api/v1/search", tok))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("token %q = %d", tok, rec.Code)
		}
	}
	// An external account that is no longer active gets the route's 401, not a hint about the allow-list.
	_, _, inactive, _ := newExternalGateRig(AccountState{Active: false, External: true})
	rec = httptest.NewRecorder()
	inactive.ServeHTTP(rec, cookieReq("GET", "/api/v1/users", "tok"))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expired external account = %d", rec.Code)
	}
}
