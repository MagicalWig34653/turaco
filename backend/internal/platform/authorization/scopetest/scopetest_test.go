package scopetest

import (
	"fmt"
	"slices"
	"sort"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

// memModule is an in-memory module that enforces scope the way an adopting module must (LocationScope.Allows for
// single objects); the flaw switches break it in the ways the harness has to catch.
type memModule struct {
	view, manage string
	seq          int
	objects      map[string]string // id -> Location ("" for none)
	order        []string
	parent       map[string]string // Location tree: id -> parent ("" for a Site)
	inactive     map[string]bool
	flaw         string
}

func newMem(flaw string) *memModule {
	return &memModule{view: "assets.view", manage: "assets.manage", objects: map[string]string{}, parent: map[string]string{},
		inactive: map[string]bool{}, flaw: flaw}
}

func (m *memModule) next(prefix string) string {
	m.seq++
	return fmt.Sprintf("%s-%d", prefix, m.seq)
}

func (m *memModule) Locations(*testing.T) Locations {
	l := Locations{SiteA: m.next("site-a"), AreaA1: m.next("area-a1"), AreaA2Inactive: m.next("area-a2"), SiteB: m.next("site-b"), AreaB1: m.next("area-b1")}
	m.parent[l.SiteA], m.parent[l.AreaA1], m.parent[l.AreaA2Inactive] = "", l.SiteA, l.SiteA
	m.parent[l.SiteB], m.parent[l.AreaB1] = "", l.SiteB
	m.inactive[l.AreaA2Inactive] = true
	return l
}

func (m *memModule) Expand(_ *testing.T, roots []string) []string {
	out := []string{}
	for id := range m.parent {
		for cur := id; ; cur = m.parent[cur] {
			if slices.Contains(roots, cur) {
				out = append(out, id)
				break
			}
			if cur == "" || m.parent[cur] == "" {
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func (m *memModule) MoveLocation(_ *testing.T, id, parentID string) { m.parent[id] = parentID }

func (m *memModule) User(_ *testing.T, name string) string { return m.next("user") }
func (m *memModule) Create(_ *testing.T, loc string) string {
	id := m.next("obj")
	m.objects[id] = loc
	m.order = append(m.order, id)
	return id
}

func (m *memModule) canView(p authorization.Principal, loc string) bool {
	switch m.flaw {
	case "list-ignores-scope":
		return p.HasAnywhere(m.view) || p.HasAnywhere(m.manage)
	case "unlocated-visible":
		if loc == "" && p.HasAnywhere(m.view) {
			return true
		}
	case "unlocated-visible-to-managers":
		if loc == "" && p.HasAnywhere(m.manage) {
			return true
		}
	case "inactive-hidden":
		if m.inactive[loc] && !p.Has(m.view) {
			return false
		}
	}
	return p.ScopeOf(m.view).Allows(loc) || p.ScopeOf(m.manage).Allows(loc)
}

func (m *memModule) List(_ *testing.T, p authorization.Principal) []string {
	out := []string{}
	for _, id := range m.order {
		if m.canView(p, m.objects[id]) {
			out = append(out, id)
		}
	}
	return out
}

func (m *memModule) Count(t *testing.T, p authorization.Principal) int {
	if m.flaw == "count-ignores-scope" && p.HasAnywhere(m.view) {
		return len(m.order)
	}
	return len(m.List(t, p))
}

func (m *memModule) Get(_ *testing.T, p authorization.Principal, id string) bool {
	loc, ok := m.objects[id]
	if m.flaw == "detail-by-has" {
		return ok && p.HasAnywhere(m.view)
	}
	return ok && m.canView(p, loc)
}

func (m *memModule) Mutate(_ *testing.T, p authorization.Principal, id string) bool {
	loc, ok := m.objects[id]
	switch m.flaw {
	case "mutate-ignores-scope":
		return ok && p.HasAnywhere(m.manage)
	case "mutate-by-view-scope":
		// Checks the Location against the view scope and the manage permission anywhere.
		return ok && p.ScopeOf(m.view).Allows(loc) && p.HasAnywhere(m.manage)
	}
	return ok && p.ScopeOf(m.manage).Allows(loc)
}

func (m *memModule) Move(_ *testing.T, p authorization.Principal, id, loc string) bool {
	cur, ok := m.objects[id]
	s := p.ScopeOf(m.manage)
	allowed := ok && s.Allows(cur) && s.Allows(loc)
	if m.flaw == "move-checks-source-only" {
		allowed = ok && s.Allows(cur)
	}
	if allowed {
		m.objects[id] = loc
	}
	return allowed
}

func (m *memModule) CreateAs(t *testing.T, p authorization.Principal, loc string) bool {
	allowed := p.ScopeOf(m.manage).Allows(loc)
	if m.flaw == "create-ignores-scope" {
		allowed = p.HasAnywhere(m.manage)
	}
	if allowed {
		m.Create(t, loc)
	}
	return allowed
}

// surface is a further read path over the module's list; the count flaw makes its count ignore scope.
func (m *memModule) surface() Surface {
	return Surface{Name: "search", Visible: m.List, Count: func(t *testing.T, p authorization.Principal) int {
		if m.flaw == "surface-count-leak" && p.HasAnywhere(m.view) {
			return len(m.order)
		}
		return len(m.List(t, p))
	}}
}

type recorder struct{ errors []string }

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func TestHarnessPassesACorrectModule(t *testing.T) {
	m := newMem("")
	Run(t, Config{View: m.view, Manage: m.manage, Surfaces: []Surface{m.surface()}}, m)
}

func TestHarnessCatchesLeaks(t *testing.T) {
	for _, flaw := range []string{"list-ignores-scope", "unlocated-visible", "unlocated-visible-to-managers", "inactive-hidden",
		"count-ignores-scope", "detail-by-has", "mutate-ignores-scope", "mutate-by-view-scope", "move-checks-source-only",
		"create-ignores-scope", "surface-leak", "surface-count-leak"} {
		t.Run(flaw, func(t *testing.T) {
			m := newMem(flaw)
			cfg := Config{View: m.view, Manage: m.manage, Surfaces: []Surface{m.surface()}}
			if flaw == "surface-leak" {
				cfg.Surfaces = []Surface{{Name: "my-work", Visible: func(*testing.T, authorization.Principal) []string { return m.order }}}
			}
			r := &recorder{}
			check(r, t, cfg, m)
			if len(r.errors) == 0 {
				t.Fatalf("the harness did not detect %q", flaw)
			}
		})
	}
}
