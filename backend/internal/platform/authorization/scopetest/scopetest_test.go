package scopetest

import (
	"fmt"
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
	flaw         string
}

func newMem(flaw string) *memModule {
	return &memModule{view: "assets.view", manage: "assets.manage", objects: map[string]string{}, flaw: flaw}
}

func (m *memModule) next(prefix string) string {
	m.seq++
	return fmt.Sprintf("%s-%d", prefix, m.seq)
}

func (m *memModule) Locations(*testing.T) Locations {
	return Locations{SiteA: m.next("site-a"), AreaA1: m.next("area-a1"), SiteB: m.next("site-b")}
}
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
	if m.flaw == "mutate-ignores-scope" {
		return ok && p.HasAnywhere(m.manage)
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

type recorder struct{ errors []string }

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func TestHarnessPassesACorrectModule(t *testing.T) {
	m := newMem("")
	Run(t, Config{View: m.view, Manage: m.manage, Surfaces: []Surface{{Name: "search", Visible: m.List}}}, m)
}

func TestHarnessCatchesLeaks(t *testing.T) {
	for _, flaw := range []string{"list-ignores-scope", "unlocated-visible", "count-ignores-scope", "detail-by-has", "mutate-ignores-scope", "move-checks-source-only", "surface-leak"} {
		t.Run(flaw, func(t *testing.T) {
			m := newMem(flaw)
			cfg := Config{View: m.view, Manage: m.manage}
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
