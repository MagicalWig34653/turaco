package roles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"
)

// Location-scoped assignment operations and guards (slice L-B; F16 design L5, L11, L12 and the L-A security review
// outcome). The wave-1 candidates are made scopable through the evaluator's test hook (f.locations()).

// scopedRole assigns role to userID at locationID through the CLI (exempt from the actor-relative guards).
func (f *fixture) assignAt(role Role, userID, locationID string) Assignment {
	f.t.Helper()
	a, err := f.svc.AssignRole(context.Background(), f.cli, f.corr, AssignInput{RoleID: role.ID, SubjectType: SubjectUser, SubjectID: userID, ScopeLocationID: locationID})
	if err != nil {
		f.t.Fatalf("assign %s at %s: %v", role.Key, locationID, err)
	}
	return a
}

func TestAssignScopedRoleAndAudit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l := f.locations()
	admin := f.user("Admin")
	f.makeAdmin(admin)
	lead := f.user("Site lead")
	role := f.role("lead", "tickets.view", "organization.view")

	a, err := f.svc.AssignRole(ctx, f.actor(admin), f.corr, AssignInput{RoleID: role.ID, SubjectType: SubjectUser, SubjectID: lead, ScopeLocationID: l.siteA})
	if err != nil {
		t.Fatal(err)
	}
	if a.Scope != ScopeLocation || a.ScopeLocationID != l.siteA {
		t.Fatalf("assignment = %+v", a)
	}
	rows := f.wantAudit(a.ID, "authorization.role.assigned")
	var after map[string]any
	if err := json.Unmarshal(rows[0].after, &after); err != nil {
		t.Fatal(err)
	}
	if after["scope"] != ScopeLocation || after["scopeLocationId"] != l.siteA {
		t.Fatalf("audit after = %v", after)
	}
	acc := f.access(lead)
	if len(acc.Permissions) != 0 || !acc.Scopes["tickets.view"].Allows(l.areaA1a) {
		t.Fatalf("access = %+v", acc)
	}

	// The same role at another Location and globally are separate assignments; the same scope twice is a duplicate.
	if _, err := f.svc.AssignRole(ctx, f.actor(admin), f.corr, AssignInput{RoleID: role.ID, SubjectType: SubjectUser, SubjectID: lead, ScopeLocationID: l.siteA}); !errors.Is(err, ErrDuplicateAssignment) {
		t.Fatalf("duplicate scoped assignment: %v", err)
	}
	atB := f.assignAt(role, lead, l.siteB)
	global := f.assign(role, lead)
	if global.Scope != ScopeGlobal || global.ScopeLocationID != "" {
		t.Fatalf("global = %+v", global)
	}

	// List filters by scope and exact scope Location.
	page, err := f.svc.ListAssignments(ctx, AssignmentFilter{SubjectID: lead, Scope: ScopeLocation})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("scope filter: %+v %v", page.Items, err)
	}
	page, err = f.svc.ListAssignments(ctx, AssignmentFilter{SubjectID: lead, ScopeLocationID: l.siteB})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != atB.ID {
		t.Fatalf("location filter: %+v %v", page.Items, err)
	}
	if _, err := f.svc.ListAssignments(ctx, AssignmentFilter{Scope: "site"}); !isInvalid(err) {
		t.Fatalf("bad scope filter: %v", err)
	}

	// Revocation audits the scope.
	if _, _, err := f.svc.RevokeAssignment(ctx, f.actor(admin), f.corr, atB.ID, false); err != nil {
		t.Fatal(err)
	}
	rows = f.wantAudit(atB.ID, "authorization.role.assigned", "authorization.role.assignment_revoked")
	var before map[string]any
	if err := json.Unmarshal(rows[1].before, &before); err != nil {
		t.Fatal(err)
	}
	if before["scopeLocationId"] != l.siteB {
		t.Fatalf("revocation audit before = %v", before)
	}

	// Labels name the scope Location with its path.
	labels, err := f.svc.LocationLabels(ctx, []string{l.areaA1, "", l.areaA1})
	if err != nil || !reflect.DeepEqual(labels[l.areaA1].Path, []string{"loc-" + l.siteA, "loc-" + l.areaA1}) || !labels[l.areaA1].Active {
		t.Fatalf("labels = %+v, %v", labels, err)
	}
}

func isInvalid(err error) bool {
	var inv *InvalidError
	return errors.As(err, &inv)
}

// Data rules hold for every actor, including the CLI.
func TestScopedAssignmentDataRules(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l := f.locations()
	u := f.user("Subject")
	role := f.role("rules", "tickets.view")
	admin, err := f.svc.GetRoleByKey(ctx, AdministratorRoleKey)
	if err != nil {
		t.Fatal(err)
	}
	assign := func(roleID, subject, loc string) error {
		_, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: roleID, SubjectType: SubjectUser, SubjectID: subject, ScopeLocationID: loc})
		return err
	}
	if err := assign(admin.ID, u, l.siteA); !errors.Is(err, ErrAdminScopeGlobal) {
		t.Errorf("administrator role scoped: %v", err)
	}
	ext := f.user("External")
	f.dir.external[ext] = true
	ceilingRole := f.role("extrole", "tasks.work")
	if err := assign(ceilingRole.ID, ext, l.siteA); !errors.Is(err, ErrExternalScopeUnsupported) {
		t.Errorf("external account scoped: %v", err)
	}
	if err := assign(role.ID, u, f.newID()); !errors.Is(err, ErrScopeLocationInvalid) {
		t.Errorf("unknown Location: %v", err)
	}
	f.dir.inactiveLocations[l.areaB1] = true
	if err := assign(role.ID, u, l.areaB1); !errors.Is(err, ErrScopeLocationInvalid) {
		t.Errorf("inactive Location: %v", err)
	}
	if err := assign(role.ID, u, "not-a-uuid"); !isInvalid(err) {
		t.Errorf("malformed Location id: %v", err)
	}
	// The trigger is the second line: a direct insert of a scoped administrator assignment maps to the same error.
	_, err = f.pool.Exec(ctx, `INSERT INTO platform.role_assignments (role_id, subject_type, subject_id, scope, scope_location_id, created_by)
		VALUES ($1, 'user', $2, 'location', $3, '{"actor":"cli"}')`, admin.ID, u, l.siteA)
	if !isAdminScoped(err) {
		t.Errorf("trigger: %v", err)
	}
	// An inactive Location is refused for new assignments only; existing ones stay effective (F16 L2).
	f.dir.inactiveLocations[l.areaB1] = false
	f.assignAt(role, u, l.areaB1)
	f.dir.inactiveLocations[l.areaB1] = true
	if !f.access(u).Scopes["tickets.view"].Allows(l.areaB1) {
		t.Error("an assignment on a deactivated Location must stay effective")
	}
	if n := len(f.auditRows(u)); n != 0 {
		t.Errorf("refused assignments wrote %d audit rows for the subject", n)
	}
}

// The guard matrix of F16 design L15: actor global versus scoped at the same Location, an ancestor, a descendant, a
// sibling and another Site.
func TestScopedGrantCeilingAndRoleHolding(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l := f.locations()
	sibling := f.newID()
	f.dir.parent[sibling] = l.siteA
	ops := f.role("ops", "tickets.view", "tickets.manage")
	target := f.user("Target")

	actorAt := func(name, loc string) string {
		u := f.user(name)
		if loc == "" {
			f.assign(ops, u)
		} else {
			f.assignAt(ops, u, loc)
		}
		return u
	}
	try := func(actor, loc string) error {
		a, err := f.svc.AssignRole(ctx, f.actor(actor), f.corr, AssignInput{RoleID: ops.ID, SubjectType: SubjectUser, SubjectID: target, ScopeLocationID: loc})
		if err == nil {
			if _, _, err := f.svc.RevokeAssignment(ctx, f.cli, f.corr, a.ID, false); err != nil {
				t.Fatal(err)
			}
		}
		return err
	}
	atSite := actorAt("At Site A", l.siteA)
	atArea := actorAt("At area A1", l.areaA1)
	global := actorAt("Global", "")
	for _, tc := range []struct {
		name, actor, loc string
		want             error
	}{
		{"same Location", atSite, l.siteA, nil},
		{"descendant", atSite, l.areaA1a, nil},
		{"ancestor", atArea, l.siteA, ErrRoleNotHeld},
		{"sibling", atArea, sibling, ErrRoleNotHeld},
		{"other Site", atSite, l.siteB, ErrRoleNotHeld},
		{"scoped actor assigns globally", atSite, "", ErrRoleNotHeld},
		{"global actor at a Location", global, l.areaB1, nil},
		{"global actor globally", global, "", nil},
	} {
		if err := try(tc.actor, tc.loc); !errors.Is(err, tc.want) && !(tc.want == nil && err == nil) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}

	// A global-only permission of the role must be held globally, even though it is inert at the Location.
	mixed := f.role("mixed", "tickets.view", "organization.view")
	lead := f.user("Mixed lead")
	f.assignAt(mixed, lead, l.siteA)
	if err := func() error {
		_, err := f.svc.AssignRole(ctx, f.actor(lead), f.corr, AssignInput{RoleID: mixed.ID, SubjectType: SubjectUser, SubjectID: target, ScopeLocationID: l.areaA1})
		return err
	}(); !errors.Is(err, ErrGrantExceedsHolder) {
		t.Fatalf("inert global-only permission: %v", err)
	}
	f.assign(f.role("orgview", "organization.view"), lead)
	if _, err := f.svc.AssignRole(ctx, f.actor(lead), f.corr, AssignInput{RoleID: mixed.ID, SubjectType: SubjectUser, SubjectID: target, ScopeLocationID: l.areaA1}); err != nil {
		t.Fatalf("with organization.view held globally: %v", err)
	}

	// Holding the role is not enough: the actor must also cover its permissions at the scope (role held at Site A
	// through one role, but tickets.manage only at area A1 through another).
	narrow := f.role("narrow", "tickets.manage")
	view := f.role("viewonly", "tickets.view")
	combo := f.role("combo", "tickets.view", "tickets.manage")
	split := f.user("Split holder")
	f.assignAt(view, split, l.siteA)
	f.assignAt(narrow, split, l.areaA1)
	f.assignAt(combo, split, l.areaA1)
	if _, err := f.svc.AssignRole(ctx, f.actor(split), f.corr, AssignInput{RoleID: combo.ID, SubjectType: SubjectUser, SubjectID: target, ScopeLocationID: l.areaA1a}); err != nil {
		t.Fatalf("covered at A1a: %v", err)
	}

	// High-risk roles stay administrator-only at every scope.
	risky := f.role("risky", "platform.roles.manage")
	holder := f.user("Risky holder")
	f.assignAt(risky, holder, l.siteA)
	if _, err := f.svc.AssignRole(ctx, f.actor(holder), f.corr, AssignInput{RoleID: risky.ID, SubjectType: SubjectUser, SubjectID: target, ScopeLocationID: l.siteA}); !errors.Is(err, ErrHighRiskNeedsAdministrator) {
		t.Fatalf("high-risk scoped: %v", err)
	}
}

func TestScopedRemovalCeiling(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l := f.locations()
	ops := f.role("ops", "tickets.view", "tickets.manage")
	target := f.user("Target")
	asg := f.assignAt(ops, target, l.siteA)
	for _, tc := range []struct {
		name, loc string
		ok        bool
	}{
		{"narrower actor at a descendant", l.areaA1, false},
		{"actor at another Site", l.siteB, false},
		{"actor at the same Location", l.siteA, true},
	} {
		actor := f.user(tc.name)
		f.assignAt(ops, actor, tc.loc)
		_, _, err := f.svc.RevokeAssignment(ctx, f.actor(actor), f.corr, asg.ID, false)
		if tc.ok != (err == nil) || (!tc.ok && !errors.Is(err, ErrGrantExceedsHolder)) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

// Review finding 1: the dominance guard (R1) compares the target's scoped holdings too. This test fails when the
// target side reads global holdings only.
func TestDominanceWithScopedTargets(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l := f.locations()
	g := NewGuards(f.pool, f.dir)
	g.eval.scopable = scopableForTest
	dominance := func(actor, target string) error {
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		return g.RequireDominance(ctx, tx, f.actor(actor), target)
	}
	ops := f.role("ops", "tickets.view")
	target := f.user("Scoped target")
	f.assignAt(ops, target, l.siteA)

	nobody := f.user("Nobody")
	atArea := f.user("At area")
	f.assignAt(ops, atArea, l.areaA1)
	atSiteB := f.user("At Site B")
	f.assignAt(ops, atSiteB, l.siteB)
	atSite := f.user("At Site A")
	f.assignAt(ops, atSite, l.siteA)
	global := f.user("Global")
	f.assign(ops, global)
	for _, tc := range []struct {
		name, actor string
		ok          bool
	}{
		{"no holdings", nobody, false},
		{"narrower scope", atArea, false},
		{"other Site", atSiteB, false},
		{"same scope", atSite, true},
		{"global", global, true},
	} {
		err := dominance(tc.actor, target)
		if tc.ok != (err == nil) || (!tc.ok && !errors.Is(err, ErrDominanceRequired)) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	// A global target is not dominated by a scoped actor.
	globalTarget := f.user("Global target")
	f.assign(ops, globalTarget)
	if err := dominance(atSite, globalTarget); !errors.Is(err, ErrDominanceRequired) {
		t.Errorf("scoped actor over global target: %v", err)
	}
}

// Review finding 2: separation-of-duties checks see scoped holdings.
func TestSoDCountsScopedHoldings(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l := f.locations()
	target := f.user("Scoped operator")
	f.assignAt(f.role("ops", "tickets.manage"), target, l.siteA)
	roleAdmin := f.role("rolemgr", "platform.roles.manage")
	_, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: roleAdmin.ID, SubjectType: SubjectUser, SubjectID: target})
	var sod *SoDRequiredError
	if !errors.As(err, &sod) || !slices.Contains(sod.Rules, "roles_manage_operations") {
		t.Fatalf("SoD over scoped holdings: %v", err)
	}
	ep, err := f.svc.EffectivePermissionsOf(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(ep.Scoped) != 1 || len(ep.Warnings) != 0 {
		t.Fatalf("effective = %+v", ep)
	}
}

// Review finding 4: an expired scoped assignment does not block re-creating it at the same Location.
func TestExpiredScopedAssignmentIsReplaced(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l := f.locations()
	u := f.user("Temporary lead")
	role := f.role("temp", "tickets.view")
	exp := time.Now().Add(time.Hour)
	old, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: role.ID, SubjectType: SubjectUser, SubjectID: u, ScopeLocationID: l.siteA, ExpiresAt: &exp})
	if err != nil {
		t.Fatal(err)
	}
	other := f.assignAt(role, u, l.siteB)
	if _, err := f.pool.Exec(ctx, `UPDATE platform.role_assignments SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 minute' WHERE id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}
	renewed := f.assignAt(role, u, l.siteA)
	if renewed.ID == old.ID {
		t.Fatal("not a new assignment")
	}
	rows := f.wantAudit(old.ID, "authorization.role.assigned", "authorization.role.assignment_expired")
	if rows[1].metadata["scopeLocationId"] != l.siteA || rows[1].metadata["reason"] != "replaced_after_expiry" {
		t.Fatalf("expiry audit = %v", rows[1].metadata)
	}
	// L-B security review gate 5: the record names who triggered the replacement and snapshots the Location.
	trigger, _ := rows[1].metadata["triggeredBy"].(map[string]any)
	if trigger["actor"] != f.cli.System || rows[1].metadata["scopeLocationName"] != "loc-"+l.siteA {
		t.Fatalf("expiry audit trigger and snapshot = %v", rows[1].metadata)
	}
	// The assignment at Site B is untouched.
	page, _ := f.svc.ListAssignments(ctx, AssignmentFilter{SubjectID: u})
	if len(page.Items) != 2 || !slices.ContainsFunc(page.Items, func(a Assignment) bool { return a.ID == other.ID }) {
		t.Fatalf("assignments = %+v", page.Items)
	}
}

func TestHoldersByLocationAndMembersWithScope(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l := f.locations()
	role := f.role("view", "tickets.view", "organization.view")
	glob, atA, atA1, atB := f.user("Global"), f.user("At A"), f.user("At A1"), f.user("At B")
	f.assign(role, glob)
	f.assignAt(role, atA, l.siteA)
	f.assignAt(role, atA1, l.areaA1)
	f.assignAt(role, atB, l.siteB)
	group := f.group("Site A staff")
	if _, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: role.ID, SubjectType: SubjectDirectoryGroup, SubjectID: group, ScopeLocationID: l.siteA}); err != nil {
		t.Fatal(err)
	}
	member := f.user("Group member")
	f.dir.groupsOf[member] = []string{group}

	subjects := func(loc, perm string) []string {
		t.Helper()
		page, err := f.svc.Holders(ctx, perm, loc, Page{Limit: 200})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, h := range page.Items {
			if h.RoleID == role.ID {
				out = append(out, h.SubjectID)
				if (h.Scope == ScopeLocation) == (h.ScopeLocationID == "") {
					t.Fatalf("holder scope = %+v", h)
				}
			}
		}
		sort.Strings(out)
		return out
	}
	sorted := func(ids ...string) []string { sort.Strings(ids); return ids }
	if got := subjects(l.areaA1a, "tickets.view"); !reflect.DeepEqual(got, sorted(glob, atA, atA1, group)) {
		t.Errorf("holders at A1a = %v", got)
	}
	if got := subjects(l.siteA, "tickets.view"); !reflect.DeepEqual(got, sorted(glob, atA, group)) {
		t.Errorf("holders at Site A = %v", got)
	}
	if got := subjects("", "tickets.view"); !reflect.DeepEqual(got, sorted(glob, atA, atA1, atB, group)) {
		t.Errorf("all holders = %v", got)
	}
	// A global-only permission is inert at a Location: only global holders hold it.
	if got := subjects(l.siteA, "organization.view"); !reflect.DeepEqual(got, []string{glob}) {
		t.Errorf("global-only holders = %v", got)
	}
	if _, err := f.svc.Holders(ctx, "tickets.view", f.newID(), Page{}); !isInvalid(err) {
		t.Errorf("unknown Location: %v", err)
	}

	// Review finding 3: members carry the scope through which they hold the role.
	m, err := f.svc.MembersOf(ctx, role.ID)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Member{}
	for _, x := range m.Items {
		byID[x.UserID] = x
	}
	if !byID[glob].Global || byID[atA].Global || !reflect.DeepEqual(byID[atA].ScopeLocationIDs, []string{l.siteA}) {
		t.Errorf("members = %+v", m.Items)
	}
	if mm := byID[member]; mm.Global || mm.Source != SubjectDirectoryGroup || !reflect.DeepEqual(mm.ScopeLocationIDs, []string{l.siteA}) {
		t.Errorf("group member = %+v", mm)
	}
	// A second, global path makes the member global.
	f.assign(role, atA)
	m, _ = f.svc.MembersOf(ctx, role.ID)
	for _, x := range m.Items {
		if x.UserID == atA && (!x.Global || len(x.ScopeLocationIDs) != 0) {
			t.Errorf("member with global and scoped paths = %+v", x)
		}
	}
}

// A role the actor holds at any Location cannot gain permissions through the actor's own edit.
func TestRoleHeldAtLocationIsSelfEditGuarded(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l := f.locations()
	actor := f.user("Editor")
	role := f.role("edited", "tickets.view")
	f.assignAt(role, actor, l.siteA)
	f.assign(f.role("manage", "tickets.view", "tickets.manage"), actor)
	_, err := f.svc.SetRolePermissions(ctx, f.actor(actor), f.corr, role.ID, SetRolePermissionsInput{
		Permissions: []string{"tickets.view", "tickets.manage"}, ExpectedVersion: role.Version})
	if !errors.Is(err, ErrSelfAssignment) {
		t.Fatalf("self edit through a scoped holding: %v", err)
	}
}

// Review finding 5: more scoped roots than the cap are capped (and logged), not a failed session.
func TestScopedRootsAreCapped(t *testing.T) {
	dir := &fakeDirectory{parent: map[string]string{}}
	roots := make([]string, 0, MaxScopedRoots+5)
	for i := 0; i < MaxScopedRoots+5; i++ {
		id := fmt.Sprintf("site-%05d", i)
		dir.parent[id] = ""
		roots = append(roots, id)
	}
	e := &Evaluator{groups: dir, scopable: scopableForTest}
	got, err := e.expand(context.Background(), map[string][]string{"tickets.view": roots})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if n := len(got["tickets.view"].Locations); n != MaxScopedRoots {
		t.Fatalf("expanded %d Locations, want the cap %d", n, MaxScopedRoots)
	}
}
