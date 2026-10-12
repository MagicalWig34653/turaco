package authorization

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/permissions"
)

func TestLocationScopeAllows(t *testing.T) {
	scoped := LocationScope{Locations: []string{"a", "a1", "c"}}
	for _, tc := range []struct {
		scope LocationScope
		loc   string
		want  bool
	}{
		{LocationScope{Global: true}, "anything", true},
		{LocationScope{Global: true}, "", true},
		{scoped, "a1", true},
		{scoped, "b", false},
		{scoped, "", false}, // an object without Location is never covered by a scoped set
		{LocationScope{}, "a", false},
		{LocationScope{}, "", false},
	} {
		if got := tc.scope.Allows(tc.loc); got != tc.want {
			t.Errorf("%+v.Allows(%q) = %v, want %v", tc.scope, tc.loc, got, tc.want)
		}
	}
	if (LocationScope{}).Any() || !scoped.Any() || !(LocationScope{Global: true}).Any() {
		t.Fatal("Any")
	}
}

func TestLocationScopeSQL(t *testing.T) {
	if sql, args := (LocationScope{Global: true}).SQL("t.location_id"); sql != "TRUE" || args != nil {
		t.Fatalf("global: %q %v", sql, args)
	}
	if sql, args := (LocationScope{}).SQL("t.location_id"); sql != "FALSE" || args != nil {
		t.Fatalf("empty: %q %v", sql, args)
	}
	locs := []string{"a", "b"}
	sql, args := (LocationScope{Locations: locs}).SQL("location_id")
	if sql != "location_id = ANY(?::text[]::uuid[])" || len(args) != 1 {
		t.Fatalf("scoped: %q %v", sql, args)
	}
	locs[0] = "mutated"
	if got := args[0].([]string); got[0] != "a" {
		t.Fatal("SQL must copy the Location ids")
	}
	for _, bad := range []string{"", "x; DROP TABLE t", "a.b.c", "t.location_id OR TRUE", "Upper"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("SQL(%q) did not panic", bad)
				}
			}()
			LocationScope{Locations: []string{"a"}}.SQL(bad)
		}()
	}
}

func TestScopeOfKeepsGlobalAndScopedApart(t *testing.T) {
	p := Principal{
		Permissions: map[string]struct{}{"tickets.view": {}},
		scopes: map[string]LocationScope{
			"tickets.view": {Locations: []string{"x"}},
			"assets.view":  {Locations: []string{"a", "a1"}},
			// A Global entry in Scopes is malformed input and must not become a global holding.
			"tasks.view": {Global: true},
		},
	}
	if s := p.ScopeOf("tickets.view"); !s.Global {
		t.Fatalf("global holding: %+v", s)
	}
	if s := p.ScopeOf("assets.view"); s.Global || len(s.Locations) != 2 || p.Has("assets.view") || p.HasAny("assets.view") {
		t.Fatalf("scoped holding leaked into the global meaning: %+v", s)
	}
	if s := p.ScopeOf("tasks.view"); s.Any() {
		t.Fatalf("Global flag inside Scopes was honoured: %+v", s)
	}
	if s := p.ScopeOf("unknown.perm"); s.Any() {
		t.Fatalf("unknown permission: %+v", s)
	}
	// ScopeOf returns a copy.
	p.ScopeOf("assets.view").Locations[0] = "zzz"
	if p.scopes["assets.view"].Locations[0] != "a" {
		t.Fatal("ScopeOf must not expose the principal's slice")
	}
}

// L-B security review gate 6: the scoped map is unexported; WithScopes stores a copy and ScopedPermissions names only
// non-empty, non-global holdings.
func TestWithScopesCopiesAndScopedPermissionsNames(t *testing.T) {
	in := map[string]LocationScope{
		"assets.view":  {Locations: []string{"a"}},
		"tickets.view": {Locations: []string{"b"}},
		"tasks.view":   {},
		"assets.edit":  {Global: true, Locations: []string{"c"}},
	}
	p := Principal{UserID: "u", Permissions: map[string]struct{}{"tickets.view": {}}}.WithScopes(in)
	in["assets.view"].Locations[0] = "zzz"
	in["new.perm"] = LocationScope{Locations: []string{"x"}}
	if got := p.ScopeOf("assets.view").Locations; len(got) != 1 || got[0] != "a" {
		t.Fatalf("WithScopes must copy: %v", got)
	}
	if got := p.ScopedPermissions(); !slices.Equal(got, []string{"assets.view"}) {
		t.Fatalf("ScopedPermissions = %v", got)
	}
	if got := (Principal{}).WithScopes(nil).ScopedPermissions(); got == nil || len(got) != 0 {
		t.Fatalf("empty: %v", got)
	}
}

func withScopable(t *testing.T, names ...string) {
	t.Helper()
	prev := isScopable
	isScopable = func(n string) bool {
		for _, s := range names {
			if s == n {
				return true
			}
		}
		return false
	}
	t.Cleanup(func() { isScopable = prev })
}

func expectPanic(t *testing.T, want string, f func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("no panic, want %q", want)
		}
		if msg, _ := r.(string); !strings.Contains(msg, want) {
			t.Fatalf("panic %v, want %q", r, want)
		}
	}()
	f()
}

func TestRequireScopedRefusesNonScopablePermissionsAtStartup(t *testing.T) {
	// The real registry: no permission is scopable before a module adopts scope (slice L-A).
	for _, p := range permissions.Registry {
		name := p.Name
		if permissions.Scopable(name) {
			continue
		}
		expectPanic(t, "non-scopable", func() { RequireScoped(DenyAll{}, name) })
	}
	expectPanic(t, "unregistered", func() { RequireScoped(DenyAll{}, "no.such.permission") })
	expectPanic(t, "at least one", func() { RequireScoped(DenyAll{}) })
	withScopable(t, "assets.view")
	expectPanic(t, "non-scopable", func() { RequireScoped(DenyAll{}, "assets.view", "platform.admin") })
}

func TestRequireScopedAdmission(t *testing.T) {
	withScopable(t, "assets.view", "assets.manage")
	for _, tc := range []struct {
		name string
		p    Principal
		want int
	}{
		{"global", Principal{UserID: "u", Permissions: map[string]struct{}{"assets.view": {}}}, http.StatusOK},
		{"scoped", Principal{UserID: "u", scopes: map[string]LocationScope{"assets.manage": {Locations: []string{"a"}}}}, http.StatusOK},
		{"scoped but empty", Principal{UserID: "u", scopes: map[string]LocationScope{"assets.view": {}}}, http.StatusForbidden},
		{"other permission scoped", Principal{UserID: "u", scopes: map[string]LocationScope{"tickets.view": {Locations: []string{"a"}}}}, http.StatusForbidden},
		{"nothing", Principal{UserID: "u"}, http.StatusForbidden},
	} {
		var seen Principal
		h := RequireScoped(fixed{p: tc.p, ok: true}, "assets.view", "assets.manage")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen, _ = PrincipalFrom(r.Context())
			w.WriteHeader(http.StatusOK)
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, rec.Code, tc.want)
		}
		if tc.want == http.StatusOK && seen.UserID != "u" {
			t.Errorf("%s: principal not stored", tc.name)
		}
	}
	rec := httptest.NewRecorder()
	RequireScoped(fixed{ok: false}, "assets.view")(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", rec.Code)
	}
}

// A route that has not adopted scope (Require/RequireAny) never admits a caller whose only holding is scoped.
func TestRequireIgnoresScopedHoldings(t *testing.T) {
	p := Principal{UserID: "u", scopes: map[string]LocationScope{"assets.view": {Locations: []string{"a"}}}}
	rec := httptest.NewRecorder()
	Require(fixed{p: p, ok: true}, "assets.view")(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
}

// scopeAdoption is the adoption table of F16 design L3/L9: every scopable permission with the module test that runs
// the authorization/scopetest harness for it, as "<file under backend/internal>:<TestName>" (for example
// "modules/assets/application/scope_test.go:TestAssetsLocationScope"). An adoption slice adds its permissions here in
// the same change that marks them scopable in the registry. Assets (L-E) and Tickets (L-D) adopted scope.
var scopeAdoption = map[string]string{
	"assets.view":    "wiring/assets_scope_test.go:TestAssetsLocationScope",
	"assets.manage":  "wiring/assets_scope_test.go:TestAssetsLocationScope",
	"tickets.view":   "wiring/servicedesk_scope_test.go:TestServiceDeskLocationScope",
	"tickets.manage": "wiring/servicedesk_scope_test.go:TestServiceDeskLocationScope",
}

// scopePrerequisites are the tests of safeguards that must exist before any permission becomes scopable, as
// "<file under backend/internal>:<TestName>". L-B security review gate 1: Location moves are guarded (slice L-C),
// otherwise organization.locations.manage could pull another Site's area below a scoped Site.
var scopePrerequisites = []string{
	"modules/organization/repository/location_move_guard_test.go:TestLocationMoveGuard",
}

// The adoption table accepts entries only while every prerequisite test exists.
func TestScopeAdoptionPrerequisitesExist(t *testing.T) {
	for _, ref := range scopePrerequisites {
		file, test, _ := strings.Cut(ref, ":")
		src, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(file)))
		if err != nil || !strings.Contains(string(src), "func "+test+"(t *testing.T)") {
			t.Errorf("prerequisite %s is missing (%v); no permission may become scopable without it (%d adoption entries)", ref, err, len(scopeAdoption))
		}
	}
}

// Every adoption entry names a test function that exists and calls scopetest.Run (L-A security review item 7).
func TestScopeAdoptionEntriesNameHarnessTests(t *testing.T) {
	checkAdoptionEntries(t, scopeAdoption)
}

// errorReporter is the part of testing.T the adoption check uses.
type errorReporter interface {
	Helper()
	Errorf(format string, args ...any)
}

type failRecorder struct{ failed bool }

func (r *failRecorder) Helper()               {}
func (r *failRecorder) Errorf(string, ...any) { r.failed = true }

func checkAdoptionEntries(t errorReporter, table map[string]string) {
	t.Helper()
	for perm, ref := range table {
		file, test, ok := strings.Cut(ref, ":")
		if !ok || !strings.HasSuffix(file, "_test.go") || !strings.HasPrefix(test, "Test") {
			t.Errorf("%s: adoption entry %q is not <file_test.go>:<TestName>", perm, ref)
			continue
		}
		src, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(file)))
		if err != nil {
			t.Errorf("%s: adoption test file: %v", perm, err)
			continue
		}
		body := string(src)
		start := strings.Index(body, "func "+test+"(t *testing.T)")
		if start < 0 {
			t.Errorf("%s: %s has no test %s", perm, file, test)
			continue
		}
		end := strings.Index(body[start+1:], "\nfunc ")
		fn := body[start:]
		if end >= 0 {
			fn = body[start : start+1+end]
		}
		if !strings.Contains(fn, "scopetest.Run(") {
			t.Errorf("%s: %s does not call scopetest.Run", perm, test)
		}
	}
}

// The adoption check itself rejects entries that name no harness test.
func TestAdoptionCheckRejectsBadEntries(t *testing.T) {
	for _, ref := range []string{
		"no-colon",
		"platform/authorization/scope_test.go:TestMissing",
		"platform/authorization/scope_test.go:TestLocationScopeAllows", // exists, but runs no harness
		"platform/authorization/missing_test.go:TestX",
	} {
		r := &failRecorder{}
		checkAdoptionEntries(r, map[string]string{"x.view": ref})
		if !r.failed {
			t.Errorf("adoption entry %q was accepted", ref)
		}
	}
}

func TestScopableRegistryMatchesAdoptionTable(t *testing.T) {
	scopable := map[string]bool{}
	for _, p := range permissions.Registry {
		if p.Scope != "" && p.Scope != permissions.ScopeGlobal && p.Scope != permissions.ScopeLocation {
			t.Errorf("%s: invalid scope %q", p.Name, p.Scope)
		}
		if p.EffectiveScope() != permissions.ScopeLocation {
			continue
		}
		scopable[p.Name] = true
		if p.Risk == "high" {
			t.Errorf("%s: a high-risk permission is never scopable", p.Name)
		}
		if scopeAdoption[p.Name] == "" {
			t.Errorf("%s: scopable without an entry in the adoption table", p.Name)
		}
	}
	for name := range scopeAdoption {
		if !scopable[name] {
			t.Errorf("%s: in the adoption table but not scopable in the registry", name)
		}
	}
}
