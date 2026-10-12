// Package scopetest is the shared adversarial harness every module runs when it adopts Location scope (ADR-0040,
// F16 design L9 and L15). A module marks a permission scopable only together with a module test that calls Run for
// it; the adoption table in platform/authorization lists those tests. Import it only from _test.go files.
//
// The harness builds Principals directly (the evaluator's result shape) and drives the module through a Subject the
// module implements over its real application or HTTP layer. Fixtures: Site A with area A1, Site B, objects at A, A1,
// B and without Location, and callers that hold the module's permissions globally, scoped at Site A, scoped view
// only, scoped with an empty set (after a revocation or a Location that no longer exists) and not at all.
package scopetest

import (
	"slices"
	"sort"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

// Locations are three Locations of the module's fixture: SiteA with AreaA1 directly below it, and SiteB in another
// subtree. They must be fresh for the test run so that no other object sits at Site A.
type Locations struct {
	SiteA, AreaA1, SiteB string
}

// Subject adapts one adopting module to the harness. Every method uses the module's real code path for the
// Principal; fixture problems are reported through t.Fatal.
type Subject interface {
	// Locations creates (or returns fresh) Locations for this run.
	Locations(t *testing.T) Locations
	// User returns a User id the module accepts as a caller (for example a fresh organization User).
	User(t *testing.T, name string) string
	// Create creates one object at locationID ("" for no Location) with privileged fixture code and returns its id.
	Create(t *testing.T, locationID string) string
	// List returns every id the Principal sees through the module's list or query (all pages).
	List(t *testing.T, p authorization.Principal) []string
	// Count returns the count the module reports for the same list (sidebar, Saved View or overview counts).
	Count(t *testing.T, p authorization.Principal) int
	// Get reports whether the detail read finds the object for the Principal (false means 404).
	Get(t *testing.T, p authorization.Principal, id string) bool
	// Mutate attempts one representative mutation of the object and reports whether it was allowed.
	Mutate(t *testing.T, p authorization.Principal, id string) bool
}

// Mover is implemented by modules where a caller sets an object's Location (create into or move to a Location).
type Mover interface {
	// Move attempts to set the object's Location to locationID ("" clears it) and reports whether it was allowed.
	Move(t *testing.T, p authorization.Principal, id, locationID string) bool
}

// Surface is one further read path that must apply the same scope: search provider, My Work source, overview,
// Saved View execution, AI tool, export, relationship labels, notification recipients and so on. Visible returns the
// object ids the Principal learns about through it (for notification fan-out: the objects the User is notified of).
type Surface struct {
	Name    string
	Visible func(t *testing.T, p authorization.Principal) []string
}

// Config names the module's scopable permissions and its further read surfaces.
type Config struct {
	// View is the scopable read permission (for example assets.view); Manage the scopable write permission.
	View, Manage string
	Surfaces     []Surface
}

// reporter is the part of testing.T the assertions use; the harness's own tests record failures through it.
type reporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// Run executes the adversarial scope checks for one module.
func Run(t *testing.T, cfg Config, s Subject) {
	t.Helper()
	if cfg.View == "" || cfg.Manage == "" {
		t.Fatal("scopetest: Config needs View and Manage")
	}
	check(t, t, cfg, s)
}

// Principals are the callers the harness uses.
type Principals struct {
	Global, ScopedA, ScopedViewA, Emptied, None authorization.Principal
}

// NewPrincipals builds the harness callers for the permissions and Locations. ScopedA holds view and manage at Site A
// (expanded: Site A and area A1); ScopedViewA only view there; Emptied holds a scoped entry with no Location.
func NewPrincipals(cfg Config, loc Locations, users func(name string) string) Principals {
	atA := []string{loc.SiteA, loc.AreaA1}
	sort.Strings(atA)
	scoped := func(perms ...string) map[string]authorization.LocationScope {
		out := map[string]authorization.LocationScope{}
		for _, p := range perms {
			out[p] = authorization.LocationScope{Locations: slices.Clone(atA)}
		}
		return out
	}
	return Principals{
		Global:      authorization.Principal{UserID: users("Global holder"), Permissions: map[string]struct{}{cfg.View: {}, cfg.Manage: {}}},
		ScopedA:     authorization.Principal{UserID: users("Site A lead"), Permissions: map[string]struct{}{}, Scopes: scoped(cfg.View, cfg.Manage)},
		ScopedViewA: authorization.Principal{UserID: users("Site A viewer"), Permissions: map[string]struct{}{}, Scopes: scoped(cfg.View)},
		Emptied: authorization.Principal{UserID: users("Emptied scope"), Permissions: map[string]struct{}{},
			Scopes: map[string]authorization.LocationScope{cfg.View: {}, cfg.Manage: {}}},
		None: authorization.Principal{UserID: users("No rights"), Permissions: map[string]struct{}{}},
	}
}

func check(r reporter, t *testing.T, cfg Config, s Subject) {
	r.Helper()
	loc := s.Locations(t)
	ps := NewPrincipals(cfg, loc, func(name string) string { return s.User(t, name) })
	atA, atA1, atB, none := s.Create(t, loc.SiteA), s.Create(t, loc.AreaA1), s.Create(t, loc.SiteB), s.Create(t, "")
	all := []string{atA, atA1, atB, none}
	inScope := []string{atA, atA1}
	outOfScope := []string{atB, none}

	// Global holders keep seeing everything.
	if got := s.List(t, ps.Global); !containsAll(got, all) {
		r.Errorf("scopetest: global holder list misses objects: got %v, want all of %v", got, all)
	}
	for _, id := range all {
		if !s.Get(t, ps.Global, id) {
			r.Errorf("scopetest: global holder cannot read %s", id)
		}
	}

	// Scoped callers see exactly Site A's subtree: never Site B, never an object without Location.
	for name, p := range map[string]authorization.Principal{"scoped": ps.ScopedA, "scoped view": ps.ScopedViewA} {
		got := s.List(t, p)
		if !sameSet(got, inScope) {
			r.Errorf("scopetest: %s list = %v, want exactly %v", name, got, inScope)
		}
		if n := s.Count(t, p); n != len(inScope) {
			r.Errorf("scopetest: %s count = %d, want %d", name, n, len(inScope))
		}
		for _, id := range inScope {
			if !s.Get(t, p, id) {
				r.Errorf("scopetest: %s caller cannot read in-scope %s", name, id)
			}
		}
		for _, id := range outOfScope {
			if s.Get(t, p, id) {
				r.Errorf("scopetest: %s caller reads out-of-scope %s (must be 404)", name, id)
			}
		}
		for _, sf := range cfg.Surfaces {
			if leaked := intersect(sf.Visible(t, p), outOfScope); len(leaked) > 0 {
				r.Errorf("scopetest: surface %s leaks %v to the %s caller", sf.Name, leaked, name)
			}
		}
	}

	// Callers without a holding, or whose scope is empty, learn nothing about the fixture objects.
	for name, p := range map[string]authorization.Principal{"no rights": ps.None, "emptied scope": ps.Emptied} {
		if leaked := intersect(s.List(t, p), all); len(leaked) > 0 {
			r.Errorf("scopetest: %s caller lists %v", name, leaked)
		}
		for _, id := range all {
			if s.Get(t, p, id) {
				r.Errorf("scopetest: %s caller reads %s", name, id)
			}
		}
		for _, sf := range cfg.Surfaces {
			if leaked := intersect(sf.Visible(t, p), all); len(leaked) > 0 {
				r.Errorf("scopetest: surface %s leaks %v to the %s caller", sf.Name, leaked, name)
			}
		}
	}

	// No oracle: more data at Site B or without Location changes nothing a scoped caller sees.
	before, beforeCount := s.List(t, ps.ScopedA), s.Count(t, ps.ScopedA)
	extraB, extraNone := s.Create(t, loc.SiteB), s.Create(t, "")
	if after := s.List(t, ps.ScopedA); !sameSet(after, before) || s.Count(t, ps.ScopedA) != beforeCount {
		r.Errorf("scopetest: scoped answers change with out-of-scope data (oracle): %v -> %v", before, after)
	}
	outOfScope = append(outOfScope, extraB, extraNone)

	// Mutations: only in scope and only with the scoped manage permission.
	for _, id := range outOfScope {
		if s.Mutate(t, ps.ScopedA, id) {
			r.Errorf("scopetest: scoped caller mutated out-of-scope %s", id)
		}
		if s.Mutate(t, ps.None, id) || s.Mutate(t, ps.Emptied, id) {
			r.Errorf("scopetest: caller without scope mutated %s", id)
		}
	}
	if s.Mutate(t, ps.ScopedViewA, atA1) {
		r.Errorf("scopetest: scoped view-only caller mutated %s", atA1)
	}
	if !s.Mutate(t, ps.ScopedA, atA1) {
		r.Errorf("scopetest: scoped manager cannot mutate in-scope %s", atA1)
	}

	// Location changes: a scoped caller cannot move an object out of scope or to no Location.
	if m, ok := s.(Mover); ok {
		if m.Move(t, ps.ScopedA, atA1, loc.SiteB) {
			r.Errorf("scopetest: scoped caller moved %s to Site B", atA1)
		}
		if m.Move(t, ps.ScopedA, atA1, "") {
			r.Errorf("scopetest: scoped caller cleared the Location of %s", atA1)
		}
		if m.Move(t, ps.ScopedA, atB, loc.SiteA) {
			r.Errorf("scopetest: scoped caller pulled %s from Site B into scope", atB)
		}
		if !m.Move(t, ps.ScopedA, atA1, loc.SiteA) {
			r.Errorf("scopetest: scoped caller cannot move %s within scope", atA1)
		}
	}
}

func containsAll(got, want []string) bool {
	for _, w := range want {
		if !slices.Contains(got, w) {
			return false
		}
	}
	return true
}

// sameSet reports whether got lists exactly the ids of want, each once (a row listed twice is a defect too).
func sameSet(got, want []string) bool {
	x, y := slices.Clone(got), slices.Clone(want)
	sort.Strings(x)
	sort.Strings(y)
	if len(slices.Compact(slices.Clone(x))) != len(x) {
		return false
	}
	return slices.Equal(x, y)
}

func intersect(got, forbidden []string) []string {
	var out []string
	for _, id := range got {
		if slices.Contains(forbidden, id) {
			out = append(out, id)
		}
	}
	return out
}
