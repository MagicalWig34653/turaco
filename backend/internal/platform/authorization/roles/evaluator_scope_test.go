package roles

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

// Location scope of role assignments (ADR-0040, F16 design L4 and L15). No registry permission is scopable in
// slice L-A, so the tests make the wave-1 candidates scopable through the evaluator's scopable hook.

var testScopable = map[string]bool{"tickets.view": true, "tickets.manage": true, "assets.view": true, "assets.manage": true, "tasks.view": true}

func scopableForTest(p string) bool { return testScopable[p] }

// locations is a test Location tree: Site A with area A1 (and A1a below it), Site B with area B1.
type locations struct{ siteA, areaA1, areaA1a, siteB, areaB1 string }

func (f *fixture) locations() locations {
	l := locations{siteA: f.newID(), areaA1: f.newID(), areaA1a: f.newID(), siteB: f.newID(), areaB1: f.newID()}
	f.dir.parent[l.siteA] = ""
	f.dir.parent[l.areaA1] = l.siteA
	f.dir.parent[l.areaA1a] = l.areaA1
	f.dir.parent[l.siteB] = ""
	f.dir.parent[l.areaB1] = l.siteB
	f.svc.eval.scopable = scopableForTest
	return l
}

// assignScoped writes a Location-scoped assignment directly: the operations to create one arrive with slice L-B.
func (f *fixture) assignScoped(roleID, subjectType, subjectID, locationID string) string {
	f.t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `
		INSERT INTO platform.role_assignments (role_id, subject_type, subject_id, scope, scope_location_id, created_by)
		VALUES ($1, $2, $3, 'location', $4, '{"actor":"cli","osUser":"tester"}') RETURNING id::text`,
		roleID, subjectType, subjectID, locationID).Scan(&id); err != nil {
		f.t.Fatalf("insert scoped assignment: %v", err)
	}
	return id
}

func (f *fixture) access(userID string) authorization.Access {
	f.t.Helper()
	a, err := f.svc.eval.Access(context.Background(), userID)
	if err != nil {
		f.t.Fatal(err)
	}
	return a
}

func keys(m map[string]struct{}) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestScopedAssignmentYieldsOnlyScopedHoldings(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l := f.locations()
	lead := f.user("Site lead")
	role := f.role("sitelead", "tickets.view", "assets.view", "organization.view", "tasks.work")
	asg := f.assignScoped(role.ID, SubjectUser, lead, l.siteA)

	acc := f.access(lead)
	if len(acc.Permissions) != 0 {
		t.Fatalf("global set = %v, want empty: a scoped assignment never grants globally", keys(acc.Permissions))
	}
	want := []string{l.siteA, l.areaA1, l.areaA1a}
	sort.Strings(want)
	for _, p := range []string{"tickets.view", "assets.view"} {
		if got := acc.Scopes[p]; got.Global || !reflect.DeepEqual(got.Locations, want) {
			t.Fatalf("scope of %s = %+v, want %v", p, got, want)
		}
	}
	for _, p := range []string{"organization.view", "tasks.work"} {
		if _, ok := acc.Scopes[p]; ok {
			t.Fatalf("global-only %s took effect through a scoped assignment", p)
		}
	}
	// The pre-scope reads used by every module that has not adopted scope see nothing.
	if perms, _ := f.svc.eval.Permissions(ctx, lead); len(perms) != 0 {
		t.Fatalf("Permissions = %v", keys(perms))
	}
	if ids, _ := f.svc.eval.RoleIDs(ctx, lead); len(ids) != 0 {
		t.Fatalf("RoleIDs = %v: a scoped role must not turn into a whole-Queue role grant", ids)
	}
	p := authorization.Principal{UserID: lead, Permissions: acc.Permissions}.WithScopes(acc.Scopes)
	if p.Has("tickets.view") || !p.ScopeOf("tickets.view").Allows(l.areaA1a) || p.ScopeOf("tickets.view").Allows(l.siteB) || p.ScopeOf("tickets.view").Allows("") {
		t.Fatalf("principal scope wrong: %+v", p.ScopeOf("tickets.view"))
	}
	grants, err := f.svc.eval.RoleGrants(ctx, lead)
	if err != nil || len(grants) != 1 || grants[0].RoleID != role.ID || grants[0].Global || !reflect.DeepEqual(grants[0].Locations, want) {
		t.Fatalf("RoleGrants = %+v, %v", grants, err)
	}

	ex, err := f.svc.eval.Explain(ctx, lead)
	if err != nil {
		t.Fatal(err)
	}
	if len(ex.Permissions) != 0 || len(ex.Scoped) != 2 || len(ex.Inert) != 1 {
		t.Fatalf("explain = %+v", ex)
	}
	if ex.Inert[0].AssignmentID != asg || !reflect.DeepEqual(ex.Inert[0].Permissions, []string{"organization.view", "tasks.work"}) {
		t.Fatalf("inert = %+v", ex.Inert)
	}
	if r := ex.Scoped[0].Roots; len(r) != 1 || r[0].RootLocationID != l.siteA || !reflect.DeepEqual(r[0].GrantedBy, []string{asg}) {
		t.Fatalf("roots = %+v", r)
	}
	if ex.Roles[0].Scope != ScopeLocation || ex.Roles[0].ScopeLocationID != l.siteA {
		t.Fatalf("role grant = %+v", ex.Roles[0])
	}

	// The guards see global holdings only: a role held only at a Location is not "held" for assigning it, even by a
	// caller who manages roles globally.
	mgr := f.role("mgr", "platform.roles.manage")
	f.assign(mgr, lead)
	other := f.user("Other")
	if _, err := f.svc.AssignRole(ctx, f.actor(lead), f.corr, AssignInput{RoleID: role.ID, SubjectType: SubjectUser, SubjectID: other}); !errors.Is(err, ErrRoleNotHeld) && !errors.Is(err, ErrGrantExceedsHolder) {
		t.Fatalf("assigning a role held only at a Location: err = %v", err)
	}
}

func TestGlobalAndScopedCombine(t *testing.T) {
	f := newFixture(t)
	l := f.locations()
	u := f.user("Both")
	viewer := f.role("viewer", "tickets.view")
	worker := f.role("worker", "tickets.manage", "assets.view")
	f.assign(viewer, u)
	f.assignScoped(worker.ID, SubjectUser, u, l.siteB)
	f.assignScoped(worker.ID, SubjectUser, u, l.areaA1) // the same role at a second Location: union
	f.assignScoped(viewer.ID, SubjectUser, u, l.siteA)  // shadowed by the global assignment

	acc := f.access(u)
	if !reflect.DeepEqual(keys(acc.Permissions), []string{"tickets.view"}) {
		t.Fatalf("global = %v", keys(acc.Permissions))
	}
	if _, ok := acc.Scopes["tickets.view"]; ok {
		t.Fatal("a globally held permission must not also have a scoped entry")
	}
	want := []string{l.areaA1, l.areaA1a, l.siteB, l.areaB1}
	sort.Strings(want)
	if got := acc.Scopes["tickets.manage"].Locations; !reflect.DeepEqual(got, want) {
		t.Fatalf("tickets.manage = %v, want %v", got, want)
	}
	if acc.Scopes["tickets.manage"].Allows(l.siteA) {
		t.Fatal("scope at area A1 must not cover its parent Site A")
	}
	grants, err := f.svc.eval.RoleGrants(context.Background(), u)
	if err != nil || len(grants) != 2 {
		t.Fatalf("RoleGrants = %+v, %v", grants, err)
	}
	for _, g := range grants {
		switch g.RoleID {
		case viewer.ID:
			if !g.Global || g.Locations != nil {
				t.Fatalf("viewer = %+v", g)
			}
		case worker.ID:
			if g.Global || !reflect.DeepEqual(g.Locations, want) {
				t.Fatalf("worker = %+v", g)
			}
		}
	}
	if ids, _ := f.svc.eval.RoleIDs(context.Background(), u); !reflect.DeepEqual(ids, []string{viewer.ID}) {
		t.Fatalf("RoleIDs = %v", ids)
	}
}

func TestScopedAssignmentEdgeCasesFailClosed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l := f.locations()
	role := f.role("edge", "tickets.view")

	// Directory Group subject with scope.
	member := f.user("Member")
	g := f.group("Site group")
	f.dir.groupsOf[member] = []string{g}
	f.assignScoped(role.ID, SubjectDirectoryGroup, g, l.areaB1)
	if got := f.access(member).Scopes["tickets.view"].Locations; !reflect.DeepEqual(got, []string{l.areaB1}) {
		t.Fatalf("group scope = %v", got)
	}

	// Expired and revoked scoped assignments grant nothing.
	expired := f.user("Expired")
	id := f.assignScoped(role.ID, SubjectUser, expired, l.siteA)
	if _, err := f.pool.Exec(ctx, `UPDATE platform.role_assignments SET created_at = now() - interval '2 days', expires_at = now() - interval '1 day' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	revoked := f.user("Revoked")
	id = f.assignScoped(role.ID, SubjectUser, revoked, l.siteA)
	if _, err := f.pool.Exec(ctx, `UPDATE platform.role_assignments SET revoked_at = now(), revoked_by = '{"actor":"cli"}' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{expired, revoked} {
		if acc := f.access(u); len(acc.Scopes) != 0 || len(acc.Permissions) != 0 {
			t.Fatalf("%s: %+v", f.dir.users[u], acc)
		}
	}

	// An unknown Location (never existed, or not a Location at all) covers nothing, also in Explain and RoleGrants.
	ghost := f.user("Ghost scope")
	f.assignScoped(role.ID, SubjectUser, ghost, f.newID())
	if acc := f.access(ghost); len(acc.Scopes) != 0 {
		t.Fatalf("unknown Location: %+v", acc.Scopes)
	}
	if ex, _ := f.svc.eval.Explain(ctx, ghost); len(ex.Scoped) != 0 {
		t.Fatalf("explain unknown Location: %+v", ex.Scoped)
	}
	if rg, _ := f.svc.eval.RoleGrants(ctx, ghost); len(rg) != 0 {
		t.Fatalf("RoleGrants unknown Location: %+v", rg)
	}

	// A move takes effect on the next evaluation: area B1 moves under Site A.
	lead := f.user("Lead A")
	f.assignScoped(role.ID, SubjectUser, lead, l.siteA)
	if f.access(lead).Scopes["tickets.view"].Allows(l.areaB1) {
		t.Fatal("B1 covered before the move")
	}
	f.dir.parent[l.areaB1] = l.siteA
	if !f.access(lead).Scopes["tickets.view"].Allows(l.areaB1) {
		t.Fatal("B1 not covered after the move")
	}

	// A failing Location tree fails the evaluation (no silent partial access); without scoped holdings it is unused.
	f.dir.subtreeErr = errors.New("tree down")
	if _, err := f.svc.eval.Access(ctx, lead); err == nil {
		t.Fatal("tree error swallowed")
	}
	plain := f.user("Plain")
	f.assign(role, plain)
	if acc, err := f.svc.eval.Access(ctx, plain); err != nil || len(acc.Permissions) != 1 {
		t.Fatalf("global-only user needs the tree: %+v %v", acc, err)
	}
}

func TestScopedAssignmentsOfRestrictedAccounts(t *testing.T) {
	f := newFixture(t)
	l := f.locations()
	// Make the high-risk platform.roles.manage and the ceiling permission tasks.work scopable for this test only, to
	// prove the exclusions run before the scope split.
	f.svc.eval.scopable = func(p string) bool { return testScopable[p] || p == "platform.roles.manage" || p == "tasks.work" }
	role := f.role("restricted", "tickets.view", "platform.roles.manage", "tasks.work")

	local := f.user("Local")
	f.dir.localOrigin[local] = true
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO platform.local_credentials(user_id, login_name, kind, enabled) VALUES ($1, $2, 'local', false)`,
		local, "scoped."+f.pfx); err != nil {
		t.Fatal(err)
	}
	f.assignScoped(role.ID, SubjectUser, local, l.siteA)
	acc := f.access(local)
	if _, ok := acc.Scopes["platform.roles.manage"]; ok {
		t.Fatal("a local account holds a high-risk permission through a scoped assignment")
	}
	if _, ok := acc.Scopes["tickets.view"]; !ok {
		t.Fatal("local account lost its non-high-risk scoped permission")
	}

	ext := f.user("External")
	f.dir.external[ext] = true
	f.assignScoped(role.ID, SubjectUser, ext, l.siteA)
	if acc := f.access(ext); len(acc.Scopes) != 0 || len(acc.Permissions) != 0 {
		t.Fatalf("external account with a scoped assignment: %+v", acc)
	}
	if rg, _ := f.svc.eval.RoleGrants(context.Background(), ext); len(rg) != 0 {
		t.Fatalf("external RoleGrants: %+v", rg)
	}
}

func TestAdministratorRoleIsNeverScoped(t *testing.T) {
	f := newFixture(t)
	l := f.locations()
	admin, err := f.svc.GetRoleByKey(context.Background(), AdministratorRoleKey)
	if err != nil {
		t.Fatal(err)
	}
	u := f.user("Would-be site admin")
	_, err = f.pool.Exec(context.Background(), `
		INSERT INTO platform.role_assignments (role_id, subject_type, subject_id, scope, scope_location_id, created_by)
		VALUES ($1, 'user', $2, 'location', $3, '{"actor":"cli"}')`, admin.ID, u, l.siteA)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "ACC02" {
		t.Fatalf("scoped administrator assignment err = %v, want ACC02", err)
	}
	// Defence in depth: should such a row exist anyway, it yields nothing effective.
	r := reduce([]grant{{roleID: admin.ID, builtIn: true, assignmentID: "x", scope: ScopeLocation, scopeLocationID: l.siteA}}, false, false, scopableForTest)
	if len(r.global) != 0 || len(r.scoped) != 0 || len(r.inert["x"]) == 0 {
		t.Fatalf("scoped administrator grant: global=%d scoped=%v", len(r.global), r.scoped)
	}
}

func TestRoleAssignmentScopeConstraints(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l := f.locations()
	role := f.role("constraints", "tickets.view")
	u := f.user("Subject")
	ins := func(scope string, loc any) error {
		_, err := f.pool.Exec(ctx, `INSERT INTO platform.role_assignments (role_id, subject_type, subject_id, scope, scope_location_id, created_by)
			VALUES ($1, 'user', $2, $3, $4, '{"actor":"cli"}')`, role.ID, u, scope, loc)
		return err
	}
	for _, tc := range []struct {
		scope string
		loc   any
	}{{"location", nil}, {"global", l.siteA}, {"site", l.siteA}} {
		if err := ins(tc.scope, tc.loc); err == nil {
			t.Fatalf("scope %q with location %v accepted", tc.scope, tc.loc)
		}
	}
	// One global and several Locations for the same role and subject; never twice the same.
	for _, step := range []struct {
		scope string
		loc   any
		ok    bool
	}{{"global", nil, true}, {"global", nil, false}, {"location", l.siteA, true}, {"location", l.siteB, true}, {"location", l.siteA, false}} {
		err := ins(step.scope, step.loc)
		if (err == nil) != step.ok {
			t.Fatalf("insert %s %v: err = %v, want ok=%v", step.scope, step.loc, err, step.ok)
		}
	}
	// Every pre-existing assignment is global without a Location (migration 000096 rewrote nothing).
	var bad int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.role_assignments WHERE scope = 'global' AND scope_location_id IS NOT NULL`).Scan(&bad); err != nil || bad != 0 {
		t.Fatalf("global assignments with a Location: %d (%v)", bad, err)
	}
}

// The evaluator reads the Location tree once per distinct set of scoped roots, never per permission or role.
func TestScopeExpansionIsBounded(t *testing.T) {
	f := newFixture(t)
	l := f.locations()
	u := f.user("Many roles")
	for i := 0; i < 5; i++ {
		r := f.role(fmt.Sprintf("bulk%d", i), "tickets.view", "tickets.manage", "assets.view", "assets.manage", "tasks.view")
		f.assignScoped(r.ID, SubjectUser, u, l.siteA)
	}
	f.dir.subtreeCalls = 0
	f.access(u)
	if f.dir.subtreeCalls != 1 {
		t.Fatalf("SubtreeIDs calls = %d, want 1", f.dir.subtreeCalls)
	}
	plain := f.user("No scope")
	f.assign(f.role("plainrole", "tickets.view"), plain)
	f.dir.subtreeCalls = 0
	f.access(plain)
	if f.dir.subtreeCalls != 0 {
		t.Fatalf("SubtreeIDs called %d times for a User without scoped holdings", f.dir.subtreeCalls)
	}
}

// Randomized fixtures: Explain agrees with Access for global and scoped holdings (L15).
func TestExplainAgreesWithAccessRandomized(t *testing.T) {
	dir := &fakeDirectory{parent: map[string]string{"s1": "", "a1": "s1", "a2": "a1", "s2": "", "b1": "s2"}}
	e := &Evaluator{groups: dir, scopable: scopableForTest}
	perms := []string{"tickets.view", "tickets.manage", "assets.view", "assets.manage", "tasks.view", "organization.view", "tasks.work", "platform.roles.manage", "no.such.perm"}
	locs := []string{"s1", "a1", "a2", "s2", "b1", "ghost"}
	rng := rand.New(rand.NewPCG(1, 40))
	for round := 0; round < 300; round++ {
		var grants []grant
		for i := 0; i < 1+rng.IntN(5); i++ {
			g := grant{roleID: fmt.Sprintf("r%d", rng.IntN(3)), assignmentID: fmt.Sprintf("as%d-%d", round, i), scope: ScopeGlobal, builtIn: rng.IntN(25) == 0}
			for _, p := range perms {
				if rng.IntN(3) == 0 {
					g.perms = append(g.perms, p)
				}
			}
			if rng.IntN(2) == 0 {
				g.scope, g.scopeLocationID = ScopeLocation, locs[rng.IntN(len(locs))]
			}
			grants = append(grants, g)
		}
		local, external := rng.IntN(5) == 0, rng.IntN(6) == 0
		ex, r := explain(grants, local, external, e.scopable)
		scopes, err := e.expand(context.Background(), r.scoped)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]struct{}{}
		for _, p := range ex.Permissions {
			got[p.Name] = struct{}{}
			if len(p.GrantedBy) == 0 {
				t.Fatalf("round %d: %s without grant path", round, p.Name)
			}
		}
		if !reflect.DeepEqual(got, r.global) {
			t.Fatalf("round %d: explain global %v != %v", round, keys(got), keys(r.global))
		}
		for _, sp := range ex.Scoped {
			if _, held := r.global[sp.Name]; held || !testScopable[sp.Name] {
				t.Fatalf("round %d: %s scoped although global or not scopable", round, sp.Name)
			}
			if external {
				t.Fatalf("round %d: external account with scoped holding %s", round, sp.Name)
			}
			if local && sp.Risk == RiskHigh {
				t.Fatalf("round %d: local account with high-risk %s", round, sp.Name)
			}
			var roots []string
			for _, root := range sp.Roots {
				roots = append(roots, root.RootLocationID)
				if len(root.GrantedBy) == 0 {
					t.Fatalf("round %d: root without grant path", round)
				}
			}
			if !reflect.DeepEqual(roots, r.scoped[sp.Name]) {
				t.Fatalf("round %d: roots %v != %v", round, roots, r.scoped[sp.Name])
			}
		}
		for p, s := range scopes {
			if !slices.IsSorted(s.Locations) || len(s.Locations) == 0 || slices.Contains(s.Locations, "ghost") {
				t.Fatalf("round %d: expanded %s = %v", round, p, s.Locations)
			}
		}
		for _, in := range ex.Inert {
			for _, p := range in.Permissions {
				if _, held := r.global[p]; held {
					t.Fatalf("round %d: %s inert although held globally", round, p)
				}
			}
		}
	}
}
