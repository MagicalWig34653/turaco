// Package scopetest is the shared adversarial harness every module runs when it adopts Location scope (ADR-0040,
// F16 design L9 and L15). A module marks a permission scopable only together with a module test that calls Run for
// it; the adoption table in platform/authorization lists those tests. Import it only from _test.go files.
//
// The harness builds Principals directly (the evaluator's result shape), expanding scoped roots through the
// module's real Location tree (Subject.Expand), and drives the module through a Subject the module implements over
// its real application or HTTP layer. Fixtures: Site A with an active area A1 and an inactive area A2, Site B with
// area B1 (later moved below Site A), objects at each of them and without Location, and callers that hold the
// module's permissions globally, scoped at Site A, scoped view only, mixed (global view with scoped manage; view at
// Site A with manage at Site B), scoped with an empty set (after a revocation or a Location that no longer exists)
// and not at all.
package scopetest

import (
	"slices"
	"sort"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

// Locations are the Locations of the module's fixture: SiteA with AreaA1 and the inactive AreaA2 directly below it,
// and SiteB with AreaB1 in another subtree. They must be fresh for the test run so that no other object sits there.
type Locations struct {
	SiteA, AreaA1, AreaA2Inactive, SiteB, AreaB1 string
}

// Subject adapts one adopting module to the harness. Every method uses the module's real code path for the
// Principal; fixture problems are reported through t.Fatal.
type Subject interface {
	// Locations creates fresh Locations for this run; AreaA2Inactive is deactivated after it was created.
	Locations(t *testing.T) Locations
	// Expand returns the roots and all their current descendants (organization/public SubtreeIDs), as the evaluator
	// does; the harness builds the scoped Principals from it.
	Expand(t *testing.T, roots []string) []string
	// MoveLocation re-parents a Location with privileged fixture code.
	MoveLocation(t *testing.T, id, parentID string)
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

// Mover is implemented by modules where a caller sets an object's Location (move to a Location).
type Mover interface {
	// Move attempts to set the object's Location to locationID ("" clears it) and reports whether it was allowed.
	Move(t *testing.T, p authorization.Principal, id, locationID string) bool
}

// Creator is implemented by modules where a caller creates objects with a Location.
type Creator interface {
	// CreateAs attempts to create an object at locationID ("" for none) as the Principal and reports whether it was
	// allowed.
	CreateAs(t *testing.T, p authorization.Principal, locationID string) bool
}

// Surface is one further read path that must apply the same scope: search provider, My Work source, overview,
// Saved View execution, AI tool, export, relationship labels, notification recipients and so on. Visible returns the
// object ids the Principal learns about through it (for notification fan-out: the objects the User is notified of).
// Count, when the surface reports a number (sidebar or overview counts), must agree with Visible and must not change
// with out-of-scope data.
type Surface struct {
	Name    string
	Visible func(t *testing.T, p authorization.Principal) []string
	Count   func(t *testing.T, p authorization.Principal) int
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
	// GlobalViewScopedManageA holds View globally and Manage only at Site A.
	GlobalViewScopedManageA authorization.Principal
	// ViewAManageB holds View only at Site A and Manage only at Site B.
	ViewAManageB authorization.Principal
}

// scoped builds a scope map from permission -> expanded Locations.
func scoped(entries map[string][]string) map[string]authorization.LocationScope {
	out := map[string]authorization.LocationScope{}
	for p, locs := range entries {
		l := slices.Clone(locs)
		sort.Strings(l)
		out[p] = authorization.LocationScope{Locations: l}
	}
	return out
}

// NewPrincipals builds the harness callers. atA and atB are the expanded sets of Site A and Site B. ScopedA holds
// view and manage at Site A; ScopedViewA only view there; Emptied holds a scoped entry with no Location.
func NewPrincipals(cfg Config, atA, atB []string, users func(name string) string) Principals {
	return Principals{
		Global: authorization.Principal{UserID: users("Global holder"), Permissions: map[string]struct{}{cfg.View: {}, cfg.Manage: {}}},
		ScopedA: authorization.Principal{UserID: users("Site A lead"), Permissions: map[string]struct{}{}}.
			WithScopes(scoped(map[string][]string{cfg.View: atA, cfg.Manage: atA})),
		ScopedViewA: authorization.Principal{UserID: users("Site A viewer"), Permissions: map[string]struct{}{}}.
			WithScopes(scoped(map[string][]string{cfg.View: atA})),
		Emptied: authorization.Principal{UserID: users("Emptied scope"), Permissions: map[string]struct{}{}}.
			WithScopes(map[string]authorization.LocationScope{cfg.View: {}, cfg.Manage: {}}),
		None: authorization.Principal{UserID: users("No rights"), Permissions: map[string]struct{}{}},
		GlobalViewScopedManageA: authorization.Principal{UserID: users("Global viewer, Site A manager"), Permissions: map[string]struct{}{cfg.View: {}}}.
			WithScopes(scoped(map[string][]string{cfg.Manage: atA})),
		ViewAManageB: authorization.Principal{UserID: users("Site A viewer, Site B manager"), Permissions: map[string]struct{}{}}.
			WithScopes(scoped(map[string][]string{cfg.View: atA, cfg.Manage: atB})),
	}
}

func check(r reporter, t *testing.T, cfg Config, s Subject) {
	r.Helper()
	loc := s.Locations(t)
	atASet, atBSet := s.Expand(t, []string{loc.SiteA}), s.Expand(t, []string{loc.SiteB})
	if !containsAll(atASet, []string{loc.SiteA, loc.AreaA1, loc.AreaA2Inactive}) || slices.Contains(atASet, loc.SiteB) || !slices.Contains(atBSet, loc.AreaB1) {
		r.Errorf("scopetest: fixture Locations wrong: subtree(A) = %v, subtree(B) = %v", atASet, atBSet)
		return
	}
	ps := NewPrincipals(cfg, atASet, atBSet, func(name string) string { return s.User(t, name) })
	atA, atA1, atA2 := s.Create(t, loc.SiteA), s.Create(t, loc.AreaA1), s.Create(t, loc.AreaA2Inactive)
	atB, atB1, none := s.Create(t, loc.SiteB), s.Create(t, loc.AreaB1), s.Create(t, "")
	all := []string{atA, atA1, atA2, atB, atB1, none}
	// An object at an inactive Location below Site A stays in scope (F16 design L2).
	inScope := []string{atA, atA1, atA2}
	outOfScope := []string{atB, atB1, none}

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
			visible := sf.Visible(t, p)
			if leaked := intersect(visible, outOfScope); len(leaked) > 0 {
				r.Errorf("scopetest: surface %s leaks %v to the %s caller", sf.Name, leaked, name)
			}
			if sf.Count != nil {
				if n := sf.Count(t, p); n != len(visible) {
					r.Errorf("scopetest: surface %s counts %d for the %s caller but shows %d", sf.Name, n, name, len(visible))
				}
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
			if sf.Count != nil {
				if n := sf.Count(t, p); n != 0 {
					r.Errorf("scopetest: surface %s counts %d for the %s caller", sf.Name, n, name)
				}
			}
		}
	}

	// Mixed holdings: each permission applies only where it is held.
	if got := s.List(t, ps.GlobalViewScopedManageA); !containsAll(got, all) {
		r.Errorf("scopetest: global viewer with scoped manage misses objects: %v", got)
	}
	for _, id := range outOfScope {
		if s.Mutate(t, ps.GlobalViewScopedManageA, id) {
			r.Errorf("scopetest: global viewer with manage at Site A mutated out-of-scope %s", id)
		}
	}
	if !s.Mutate(t, ps.GlobalViewScopedManageA, atA1) {
		r.Errorf("scopetest: global viewer with manage at Site A cannot mutate %s", atA1)
	}
	if got := s.List(t, ps.ViewAManageB); slices.Contains(got, none) || !containsAll(got, inScope) {
		r.Errorf("scopetest: view at A with manage at B lists %v", got)
	}
	if s.Get(t, ps.ViewAManageB, none) {
		r.Errorf("scopetest: view at A with manage at B reads the object without Location")
	}
	for _, id := range []string{atA, atA1, none} {
		if s.Mutate(t, ps.ViewAManageB, id) {
			r.Errorf("scopetest: view at A with manage at B mutated %s", id)
		}
	}

	// No oracle: more data at Site B or without Location changes nothing a scoped caller sees or counts.
	before, beforeCount := s.List(t, ps.ScopedA), s.Count(t, ps.ScopedA)
	surfaceCounts := map[string]int{}
	for _, sf := range cfg.Surfaces {
		if sf.Count != nil {
			surfaceCounts[sf.Name] = sf.Count(t, ps.ScopedA)
		}
	}
	extraB, extraNone := s.Create(t, loc.SiteB), s.Create(t, "")
	if after := s.List(t, ps.ScopedA); !sameSet(after, before) || s.Count(t, ps.ScopedA) != beforeCount {
		r.Errorf("scopetest: scoped answers change with out-of-scope data (oracle): %v -> %v", before, after)
	}
	for _, sf := range cfg.Surfaces {
		if sf.Count != nil && sf.Count(t, ps.ScopedA) != surfaceCounts[sf.Name] {
			r.Errorf("scopetest: surface %s count changes with out-of-scope data (oracle)", sf.Name)
		}
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

	// Creation: a scoped caller creates only inside scope, never at Site B or without Location.
	if c, ok := s.(Creator); ok {
		if c.CreateAs(t, ps.ScopedA, loc.SiteB) {
			r.Errorf("scopetest: scoped caller created an object at Site B")
		}
		if c.CreateAs(t, ps.ScopedA, "") {
			r.Errorf("scopetest: scoped caller created an object without Location")
		}
		if c.CreateAs(t, ps.ScopedViewA, loc.AreaA1) {
			r.Errorf("scopetest: scoped view-only caller created an object")
		}
		if !c.CreateAs(t, ps.ScopedA, loc.AreaA1) {
			r.Errorf("scopetest: scoped caller cannot create in scope")
		}
		if !c.CreateAs(t, ps.Global, "") {
			r.Errorf("scopetest: global holder cannot create an object without Location")
		}
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

	// A moved Location changes access on the next evaluation: area B1 moves below Site A.
	s.MoveLocation(t, loc.AreaB1, loc.SiteA)
	movedA, movedB := s.Expand(t, []string{loc.SiteA}), s.Expand(t, []string{loc.SiteB})
	after := NewPrincipals(cfg, movedA, movedB, func(name string) string { return s.User(t, name) })
	if !s.Get(t, after.ScopedA, atB1) || !slices.Contains(s.List(t, after.ScopedA), atB1) {
		r.Errorf("scopetest: Site A caller does not see %s after its Location moved below Site A", atB1)
	}
	siteB := authorization.Principal{UserID: s.User(t, "Site B viewer"), Permissions: map[string]struct{}{}}.
		WithScopes(scoped(map[string][]string{cfg.View: movedB, cfg.Manage: movedB}))
	if s.Get(t, siteB, atB1) || slices.Contains(s.List(t, siteB), atB1) || s.Mutate(t, siteB, atB1) {
		r.Errorf("scopetest: Site B caller still reaches %s after its Location moved away", atB1)
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
